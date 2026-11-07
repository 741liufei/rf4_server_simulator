package gameserver

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"math/rand"
	"time"
)

const (
	fishHuntDaily = iota
	fishHuntGather
	fishHuntActive
	fishHuntGiant
)

const (
	fishHuntCompWindowSeconds = 10 * 60
	fishHuntSpotResetM        = 20.0
)

type fishHuntState struct {
	UID           int64
	Loc           string
	Spot          uint64
	AnchorX       float64
	AnchorZ       float64
	AnchorKnown   bool
	ActiveSeconds float64
	ValidBites    int64
	Pity          int64
	BigMiss       int64
	CompFish      int64
	CompGrade     int64
	CompUntil     float64
	LastCatchGUID string
	LastActiveTS  float64
	UpdatedTS     float64
}

type fishHuntReadiness struct {
	Stage int
	A     float64
	E     float64
	G     float64
	P     float64
	Q     float64
	Comp  bool
}

func (s *Server) fishHuntEnabled() bool {
	return s != nil && s.DB != nil && s.tunableF("fish_hunt_enabled", 1) != 0
}

func (s *Server) loadFishHuntState(ctx context.Context, uid int64, loc string, spot uint64) *fishHuntState {
	h := &fishHuntState{UID: uid, Loc: loc, Spot: spot}
	if !s.fishHuntEnabled() || uid == 0 || loc == "" {
		return h
	}
	var anchorKnown, compGrade int64
	err := s.DB.SQL.QueryRowContext(ctx, `
		SELECT anchor_x,anchor_z,anchor_known,active_seconds,valid_bites,pity,
		       big_miss,comp_fish,comp_grade,comp_until,last_catch_guid,
		       last_active_ts,updated_ts
		FROM fish_hunt_state WHERE uid=? AND loc=? AND spot=?`,
		uid, loc, int64(spot)).Scan(
		&h.AnchorX, &h.AnchorZ, &anchorKnown, &h.ActiveSeconds, &h.ValidBites, &h.Pity,
		&h.BigMiss, &h.CompFish, &compGrade, &h.CompUntil, &h.LastCatchGUID,
		&h.LastActiveTS, &h.UpdatedTS)
	h.AnchorKnown = anchorKnown != 0
	h.CompGrade = compGrade
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.log.Warn("鱼猎状态读取失败", "uid", uid, "loc", loc, "spot", spot, "err", err)
	}
	return h
}

func (s *Server) saveFishHuntState(ctx context.Context, h *fishHuntState) {
	if !s.fishHuntEnabled() || h == nil || h.UID == 0 || h.Loc == "" {
		return
	}
	h.UpdatedTS = float64(time.Now().Unix())
	_, err := s.DB.SQL.ExecContext(ctx, `
		INSERT INTO fish_hunt_state(
			uid,loc,spot,anchor_x,anchor_z,anchor_known,active_seconds,valid_bites,
			pity,big_miss,comp_fish,comp_grade,comp_until,last_catch_guid,
			last_active_ts,updated_ts)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(uid,loc,spot) DO UPDATE SET
			anchor_x=excluded.anchor_x,anchor_z=excluded.anchor_z,
			anchor_known=excluded.anchor_known,active_seconds=excluded.active_seconds,
			valid_bites=excluded.valid_bites,pity=excluded.pity,
			big_miss=excluded.big_miss,comp_fish=excluded.comp_fish,
			comp_grade=excluded.comp_grade,comp_until=excluded.comp_until,
			last_catch_guid=excluded.last_catch_guid,
			last_active_ts=excluded.last_active_ts,updated_ts=excluded.updated_ts`,
		h.UID, h.Loc, int64(h.Spot), h.AnchorX, h.AnchorZ, boolInt(h.AnchorKnown),
		h.ActiveSeconds, h.ValidBites, h.Pity, h.BigMiss, h.CompFish, h.CompGrade,
		h.CompUntil, h.LastCatchGUID, h.LastActiveTS, h.UpdatedTS)
	if err != nil {
		s.log.Warn("鱼猎状态保存失败", "uid", h.UID, "loc", h.Loc, "spot", h.Spot, "err", err)
	}
}

// huntAnchorGroundbait 只在超过 20 米的新窝点时重置狩猎进程。
// 同一窝点补料只刷新窝账，不清空发窝度。
func (s *Server) huntAnchorGroundbait(ctx context.Context, uid int64, loc string, spot uint64, x, z float64) {
	if !s.fishHuntEnabled() {
		return
	}
	h := s.loadFishHuntState(ctx, uid, loc, spot)
	if h.AnchorKnown && math.Hypot(x-h.AnchorX, z-h.AnchorZ) <= fishHuntSpotResetM {
		return
	}
	h.AnchorX, h.AnchorZ, h.AnchorKnown = x, z, true
	h.ActiveSeconds, h.ValidBites, h.Pity = 0, 0, 0
	h.BigMiss, h.CompFish, h.CompGrade, h.CompUntil = 0, 0, 0, 0
	h.LastActiveTS = float64(time.Now().Unix())
	s.saveFishHuntState(ctx, h)
}

func (s *Server) huntTouch(h *fishHuntState) {
	if h == nil {
		return
	}
	now := float64(time.Now().Unix())
	if h.LastActiveTS > 0 {
		dt := now - h.LastActiveTS
		if dt > 0 && dt <= 60 {
			h.ActiveSeconds += dt
		}
	}
	h.LastActiveTS = now
}

func (s *Server) fishHuntBaitMatch(ctx context.Context, key string, c fishRollContext) float64 {
	best := int64(0)
	for _, bait := range c.baits {
		if bait == "" {
			continue
		}
		for fish, tier := range s.cachedBaitTiers(ctx, c, bait) {
			if fish == key && tier > best {
				best = tier
			}
		}
	}
	switch best {
	case 3:
		return 1
	case 2:
		return .75
	case 1:
		return .5
	default:
		return .35
	}
}

func (s *Server) fishHuntReadiness(ctx context.Context, key string, c fishRollContext, h *fishHuntState) fishHuntReadiness {
	out := fishHuntReadiness{Stage: fishHuntDaily}
	if h == nil || s.DB == nil || !s.groundbaitEnabled() {
		return out
	}
	s.huntTouch(h)
	gb := s.groundbaitSpotAt(ctx, h.UID, h.Loc, c.x, c.z, c.hasPos, h.Spot)
	if gb.units <= 0 {
		return out
	}
	S := s.groundbaitStrength(gb.units)
	overdose := s.groundbaitOverdoseP(gb.units)
	out.A = groundbaitClamp(S*overdose, 0, 1)
	m := s.groundbaitSpeciesMultiplier(ctx, c, gb, key, S, overdose)
	capM := s.tunableF("groundbait_cap", 2.60)
	if capM <= 1.01 {
		capM = 2.60
	}
	out.G = groundbaitClamp((m-1)/(capM-1), 0, 1)

	env := s.fishBiteEnvironmentWeight(ctx, key, c)
	eco := groundbaitClamp((env-0.35)/1.65, 0, 1)
	out.E = groundbaitClamp(.70*eco+.30*s.fishHuntBaitMatch(ctx, key, c), 0, 1)
	out.P = .65*(1-math.Exp(-h.ActiveSeconds/720)) + .35*(1-math.Exp(-float64(h.ValidBites)/12))
	out.P = groundbaitClamp(out.P, 0, 1)
	k := groundbaitClamp(.5+c.skill.EcologyInterest*2+c.skill.TrophyLuck*2, 0, 1)
	out.Q = groundbaitClamp(out.E*out.G*(.25+.75*out.P)*(.90+.10*k), 0, 1)
	out.Comp = h.CompFish > 0 && float64(time.Now().Unix()) <= h.CompUntil

	switch {
	case out.A < .25 || out.E < .45 || out.G < .25:
		out.Stage = fishHuntDaily
	case out.A >= .70 && out.Q >= .85:
		out.Stage = fishHuntGiant
	case out.A >= .55 && out.Q >= .60:
		out.Stage = fishHuntActive
	default:
		out.Stage = fishHuntGather
	}
	return out
}

func fishHuntWeights(stage int, h *fishHuntState, trophyLuck float64, comp bool) (w1, w2, w3, w4 float64, forceTrophy bool) {
	switch stage {
	case fishHuntGather:
		w1, w2, w3, w4 = 54.5, 43, 2, .5
	case fishHuntActive:
		w1, w2, w3, w4 = 30, 61.2, 6.8, 2
	case fishHuntGiant:
		w1, w2, w3, w4 = 10, 64, 20, 6
	default:
		w1, w2, w3, w4 = 74.7, 24, 1, .3
	}
	if stage > fishHuntDaily && h != nil {
		switch {
		case h.Pity >= 50:
			w3 *= 2
		case h.Pity >= 30:
			w3 *= 1.5
		}
		w3 *= 1 + groundbaitClamp(trophyLuck, 0, .5)
		if comp {
			w3 *= 1.3
			if h.CompGrade >= 4 {
				w4 *= 1.5
			}
		}
		if h.Pity >= 60 || h.BigMiss >= 2 {
			forceTrophy = true
		}
	}
	return
}

func chooseHuntGrade(w1, w2, w3, w4 float64, forceTrophy bool) int64 {
	if forceTrophy {
		w1, w2 = 0, 0
		if w3+w4 <= 0 {
			w3 = 1
		}
	}
	total := w1 + w2 + w3 + w4
	if total <= 0 {
		return 1
	}
	r := rand.Float64() * total
	switch {
	case r < w1:
		return 1
	case r < w1+w2:
		return 2
	case r < w1+w2+w3:
		return 3
	default:
		return 4
	}
}

func triangularLog(lo, hi, mode float64) float64 {
	if lo <= 0 {
		lo = 1
	}
	if hi <= lo {
		return lo
	}
	if mode <= lo || mode >= hi {
		mode = math.Sqrt(lo * hi)
	}
	llo, lhi, lm := math.Log(lo), math.Log(hi), math.Log(mode)
	u := rand.Float64()
	f := (lm - llo) / (lhi - llo)
	switch {
	case u < f && lm > llo:
		return math.Exp(llo + math.Sqrt(u*(lhi-llo)*(lm-llo)))
	case lhi > lm:
		return math.Exp(lhi - math.Sqrt((1-u)*(lhi-llo)*(lhi-lm)))
	default:
		return mode
	}
}

func (s *Server) sampleHuntBand(ctx context.Context, key string, grade int64, scale, capG float64, c fishRollContext) int64 {
	lines := s.cachedSettleStatics(ctx, c, key)
	q, t, b := float64(lines[0]), float64(lines[1]), float64(lines[2])
	if q <= 0 || t <= q || b <= t {
		return 0
	}
	var lo, hi, mode float64
	switch grade {
	case 1:
		lo, hi, mode = math.Max(1, .15*q), .98*q, .45*q
	case 2:
		lo, hi, mode = 1.02*q, .98*t, math.Min(1.25*q, .92*t)
	case 3:
		lo, hi, mode = 1.02*t, .98*b, math.Min(1.15*t, .92*b)
	default:
		lo, hi, mode = 1.02*b, 1.60*b, 1.10*b
	}
	if capG > 0 {
		cap := capG / math.Max(scale, .0001)
		if cap < lo {
			return 0
		}
		if hi > cap {
			hi = cap
		}
		if mode > hi {
			mode = hi
		}
	}
	w := triangularLog(lo, hi, mode) * scale
	if w < 1 {
		w = 1
	}
	if capG > 0 && w > capG {
		w = capG
	}
	return int64(math.Round(w))
}

func (s *Server) sampleHuntWeight(ctx context.Context, key string, modeMult, scale, capG float64, c fishRollContext,
	h *fishHuntState) (int64, int64, fishHuntReadiness) {
	readiness := s.fishHuntReadiness(ctx, key, c, h)
	w1, w2, w3, w4, force := fishHuntWeights(readiness.Stage, h, c.skill.TrophyLuck, readiness.Comp)
	if capG > 0 {
		lines := s.cachedSettleStatics(ctx, c, key)
		if capG/math.Max(scale, .0001) < float64(lines[2]) {
			w4 = 0
		}
		if capG/math.Max(scale, .0001) < float64(lines[1]) {
			w3, w4 = 0, 0
		}
	}
	grade := chooseHuntGrade(w1, w2, w3, w4, force)
	weight := int64(0)
	for _, candidate := range []int64{grade, 3, 2, 1} {
		if candidate > grade {
			continue
		}
		if weight = s.sampleHuntBand(ctx, key, candidate, scale, capG, c); weight > 0 {
			break
		}
	}
	if weight <= 0 {
		// 档位线缺失时仍保留旧模型，避免未知鱼种完全不咬钩。
		weight = s.sampleWeightDB(ctx, key, 1, scale, capG, c)
	}
	if modeMult > 1 {
		weight = int64(math.Round(float64(weight) * math.Min(modeMult, 2.0)))
	}
	lines := s.cachedSettleStatics(ctx, c, key)
	if weight > 0 && lines[2] > 0 {
		if maxBlue := int64(math.Round(float64(lines[2]) * 1.60)); weight > maxBlue {
			weight = maxBlue
		}
	}
	if capG > 0 && weight > int64(capG) {
		weight = int64(capG)
	}
	grade = s.rollGradeForRoll(ctx, key, weight, c)
	return weight, grade, readiness
}

func (s *Server) recordHuntBite(ctx context.Context, h *fishHuntState) {
	if !s.fishHuntEnabled() || h == nil {
		return
	}
	h.ValidBites++
	s.saveFishHuntState(ctx, h)
}

// recordHuntCatch 只对同一 FishGUID 计一次，避免 14/4 重放重复推进保底。
func (s *Server) recordHuntCatch(ctx context.Context, uid int64, loc string, spot uint64, f *RolledFish) {
	if f == nil || !s.fishHuntEnabled() {
		return
	}
	h := s.loadFishHuntState(ctx, uid, loc, spot)
	if h.LastCatchGUID == f.FishGUID {
		return
	}
	h.LastCatchGUID = f.FishGUID
	if f.Grade >= 3 {
		h.Pity = 0
		h.BigMiss = 0
		h.CompFish, h.CompGrade, h.CompUntil = 0, 0, 0
		if f.Grade >= 4 {
			h.ActiveSeconds *= .2
			h.ValidBites = int64(float64(h.ValidBites) * .2)
		} else {
			h.ActiveSeconds *= .5
			h.ValidBites = int64(float64(h.ValidBites) * .5)
		}
	} else {
		h.Pity++
	}
	s.saveFishHuntState(ctx, h)
}

func (s *Server) recordHuntEscape(ctx context.Context, uid int64, loc string, spot uint64, f *RolledFish) {
	if f == nil || f.Grade < 3 || !s.fishHuntEnabled() {
		return
	}
	h := s.loadFishHuntState(ctx, uid, loc, spot)
	h.BigMiss++
	h.CompFish = 3
	h.CompGrade = f.Grade
	h.CompUntil = float64(time.Now().Unix() + fishHuntCompWindowSeconds)
	if f.Grade >= 4 {
		h.CompFish = 4
	}
	s.saveFishHuntState(ctx, h)
	s.log.Info("大鱼脱钩补偿", "uid", uid, "loc", loc, "spot", spot,
		"grade", f.Grade, "big_miss", h.BigMiss, "until", h.CompUntil)
}

func fishHuntStageName(stage int) string {
	switch stage {
	case fishHuntGather:
		return "聚鱼"
	case fishHuntActive:
		return "发窝"
	case fishHuntGiant:
		return "巨物发窝"
	default:
		return "日常"
	}
}
