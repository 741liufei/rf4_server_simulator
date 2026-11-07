package gameserver

import (
	"testing"
	"time"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

// Exercise real timer closures while another rod is held, then land/keep just
// one fish and restore the two other rods from SQLite.
func TestThreeFishingRodsTimersEventsAndRestore(t *testing.T) {
	s, db := newRigTestServer(t)
	t.Cleanup(func() { stopFishingRecoveryTimers(s) })
	sess := registerBiteTestSession(t, s, 701, nil)
	const uid = int64(1000001)
	setWeatherTestTunable(t, s, "bite_delay_fixed", "60")
	var rods []string
	var states []*FishState
	var fish []*RolledFish
	for i := range 3 {
		rod, err := s.CreateItem(t.Context(), uid, "tele_10180_4", "backpack", 1)
		if err != nil {
			t.Fatal(err)
		}
		st := s.selectFishingRod(uid, rod.GUID)
		if !s.scheduleBite(sess, st) {
			t.Fatal("could not cast another rod")
		}
		f := &RolledFish{FishGUID: newFishGUID(), Species: "perch", WeightG: int64(100 + i*100), Grade: 1, LengthM: .2}
		st.mu.Lock()
		st.InWater, st.CastTs, st.Armed = true, time.Now().Add(-time.Minute), f
		st.mu.Unlock()
		rods, states, fish = append(rods, rod.GUID), append(states, st), append(fish, f)
	}
	if states[0] == states[1] || states[1] == states[2] {
		t.Fatal("rods still share a state")
	}
	for _, st := range states {
		st.mu.Lock()
		if st.biteTimer == nil || !st.CastActive {
			t.Fatal("casting a later rod cancelled an earlier rod")
		}
		s.armBiteTimerLocked(uid, st, time.Now().Add(20*time.Millisecond))
		st.mu.Unlock()
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		ready := true
		for i, st := range states {
			st.mu.Lock()
			ready = ready && st.Pending == fish[i] && st.Pending.GearGUID == rods[i]
			st.mu.Unlock()
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timer callbacks did not deliver each rod's own fish")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The third rod is still held. Both requests explicitly address the first.
	ctx := &Ctx{Sess: sess, Tree: proto.Args524(proto.Guid(rods[0]), proto.Guid(fish[0].FishGUID)),
		Main: 14, Sub: 11, CallID: 710, Log: logging.Discard()}
	hFishingHooked(ctx)
	ctx.Sub, ctx.CallID = 4, 711
	if body, ok := hFishingEnd(ctx); !ok || len(body) == 0 {
		t.Fatal("first rod did not settle")
	}
	ctx.Sub, ctx.CallID = 5, 712
	if body, ok := s.fishingKeep(ctx); !ok || len(body) == 0 {
		t.Fatal("first rod did not enter the keepnet")
	}
	if s.fishingState(uid) != states[2] {
		t.Fatal("background rod events changed the held rod")
	}
	kept, err := db.KeepnetList(t.Context(), uid)
	if err != nil || len(kept) != 1 || kept[0].EchoGUID != fish[0].FishGUID {
		t.Fatalf("wrong fish kept: %+v %v", kept, err)
	}
	stopFishingRecoveryTimers(s)
	restarted := newFishingRecoveryServer(t, db)
	for i, rod := range rods {
		st := restarted.fishingStateForRod(uid, rod)
		st.mu.Lock()
		if i == 0 {
			if st.Pending != nil || !st.Resolved {
				t.Error("kept fish resurrected")
			}
		} else if st.Pending == nil || st.Pending.FishGUID != fish[i].FishGUID || st.Pending.WeightG != fish[i].WeightG || st.Pending.GearGUID != rod || !st.CastActive {
			t.Errorf("rod %d lost or replaced its fish: %+v", i+1, st.Pending)
		}
		st.mu.Unlock()
	}
}
