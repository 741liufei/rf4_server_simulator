package gameserver

import (
	"sort"
	"strings"

	"rf4server/internal/store"
)

// fishingState is the held rod fallback. Timers and messages with a GUID must
// keep their own rod state instead of resolving this pointer again later.
func (s *Server) fishingState(uid int64) *FishState {
	s.fishMu.Lock()
	defer s.fishMu.Unlock()
	if s.fishStates == nil {
		s.fishStates = map[int64]map[string]*FishState{}
	}
	if s.heldFishingRods == nil {
		s.heldFishingRods = map[int64]string{}
	}
	rods := s.fishStates[uid]
	if rods == nil {
		rods = map[string]*FishState{}
		var saved []*store.FishingState
		if s.DB != nil {
			var err error
			saved, err = s.DB.LoadFishingStates(s.dbCtx(), uid)
			if err != nil {
				s.log.Warn("读取多竿状态失败", "uid", uid, "err", err)
			}
		}
		for i, snapshot := range saved {
			key := snapshot.RodGUID
			rods[key] = s.newFishState(uid, snapshot)
			if i == 0 {
				s.heldFishingRods[uid] = key
			}
		}
		if len(rods) == 0 {
			rods[""] = s.newFishState(uid, nil)
		}
		s.fishStates[uid] = rods
	}
	return rods[s.heldFishingRods[uid]]
}

func (s *Server) fishingStatesFor(uid int64) []*FishState {
	s.fishingState(uid)
	s.fishMu.Lock()
	defer s.fishMu.Unlock()
	keys := make([]string, 0, len(s.fishStates[uid]))
	for key := range s.fishStates[uid] {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	states := make([]*FishState, 0, len(keys))
	for _, key := range keys {
		states = append(states, s.fishStates[uid][key])
	}
	return states
}

func (s *Server) fishingStateForRod(uid int64, guid string) *FishState {
	guid = strings.ToLower(strings.ReplaceAll(guid, "-", ""))
	current := s.fishingState(uid)
	if guid == "" || guid == zeroGUID {
		return current
	}
	s.fishMu.Lock()
	st := s.fishStates[uid][guid]
	s.fishMu.Unlock()
	if st != nil {
		return st
	}
	current.mu.Lock()
	// Adopt the initial empty state so settings made before taking the first
	// rod (or legacy snapshots) stay attached to that first rod.
	adopt := current.persist.rodGUID == "" && (current.GearGuid == "" || strings.EqualFold(current.GearGuid, guid))
	if adopt {
		current.persist.rodGUID = guid
		current.GearGuid = guid
		st = current
	} else {
		st = s.newFishState(uid, nil)
		st.persist.rodGUID, st.GearGuid = guid, guid
		st.Loc, st.Spot = current.Loc, current.Spot
		st.PosX, st.PosZ, st.HasPos = current.PosX, current.PosZ, current.HasPos
		st.BoatUntil, st.BoatBoardAt, st.BoatOffAt = current.BoatUntil, current.BoatBoardAt, current.BoatOffAt
	}
	current.mu.Unlock()
	s.fishMu.Lock()
	if existing := s.fishStates[uid][guid]; existing != nil {
		st = existing
	} else {
		if adopt {
			delete(s.fishStates[uid], "")
			if s.heldFishingRods[uid] == "" {
				s.heldFishingRods[uid] = guid
			}
		}
		s.fishStates[uid][guid] = st
	}
	s.fishMu.Unlock()
	return st
}

func (s *Server) selectFishingRod(uid int64, guid string) *FishState {
	st := s.fishingStateForRod(uid, guid)
	s.fishMu.Lock()
	s.heldFishingRods[uid] = st.persist.rodGUID
	s.fishMu.Unlock()
	s.resumeFishingState(st)
	return st
}

// All 14/x messages with a rod carry it as an argument. Match known rods first,
// then a fish GUID for messages which refer to the fish, without changing hand.
func (s *Server) fishingStateForRequest(ctx *Ctx) *FishState {
	uid := ctx.Sess.Userid
	current := s.fishingState(uid)
	guids := deepGUIDs(ctx.Tree)
	s.fishMu.Lock()
	for _, guid := range guids {
		if st := s.fishStates[uid][strings.ToLower(guid)]; st != nil {
			s.fishMu.Unlock()
			s.resumeFishingState(st)
			return st
		}
	}
	s.fishMu.Unlock()
	for _, st := range s.fishingStatesFor(uid) {
		st.mu.Lock()
		matches := false
		for _, guid := range guids {
			if strings.EqualFold(guid, st.GearGuid) || st.Pending != nil && strings.EqualFold(guid, st.Pending.FishGUID) {
				matches = true
				break
			}
		}
		st.mu.Unlock()
		if matches {
			s.resumeFishingState(st)
			return st
		}
	}
	s.resumeFishingState(current)
	return current
}

func (s *Server) fishingStateForFish(uid int64, fish *RolledFish) *FishState {
	if fish != nil && fish.GearGUID != "" {
		return s.fishingStateForRod(uid, fish.GearGUID)
	}
	return s.fishingState(uid)
}
