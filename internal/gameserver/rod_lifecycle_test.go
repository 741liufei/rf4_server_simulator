package gameserver

import (
	"bytes"
	"testing"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

func TestRodReadKeepsVersionUntilEquipmentChanges(t *testing.T) {
	s, db := newRigTestServer(t)
	rod, err := s.CreateItem(t.Context(), 1000001, "spin_10001_S60L", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	read := func() ([]byte, uint64) {
		t.Helper()
		row := mustItem(t, db, rod.GUID)
		tree, err := s.ItemToTree(t.Context(), row)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := proto.EncodeValue(tree, s.Table())
		if err != nil {
			t.Fatal(err)
		}
		field := tree.Field(fieldIdxByCS(tree.Class, csCreated))
		version := field.U64()
		if version != baitStackVersion(t, row) {
			t.Fatal("reading a rod changed its persisted version")
		}
		return raw, version
	}
	a, version := read()
	time.Sleep(time.Millisecond)
	b, _ := read()
	if !bytes.Equal(a, b) {
		t.Fatal("unchanged rod produced a different inventory snapshot")
	}
	line, err := s.CreateItem(t.Context(), 1000001, "mono_15249", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	mountGearForTest(t, s, newProbeSess(s), rod.GUID, line.GUID)
	c, changed := read()
	if changed <= version || bytes.Equal(c, b) {
		t.Fatal("real equipment change did not advance the rod version")
	}
}

func TestPutAndTakePreserveThreeHookedRods(t *testing.T) {
	s, _ := newRigTestServer(t)
	t.Cleanup(func() { stopFishingRecoveryTimers(s) })
	sess := newProbeSess(s)
	const uid = int64(1000001)
	var rods []string
	var states []*FishState
	var fish []*RolledFish
	for i := range 3 {
		rod, err := s.CreateItem(t.Context(), uid, "tele_10180_4", "backpack", 1)
		if err != nil {
			t.Fatal(err)
		}
		st := s.selectFishingRod(uid, rod.GUID)
		f := &RolledFish{FishGUID: newFishGUID(), GearGUID: rod.GUID, Species: "perch", Grade: 1, WeightG: int64(100 + i), LengthM: .2}
		st.mu.Lock()
		st.Pending, st.CastActive, st.InWater, st.PushSent = f, true, true, true
		st.mu.Unlock()
		rods, states, fish = append(rods, rod.GUID), append(states, st), append(fish, f)
	}
	for i, rod := range rods {
		// Actual 14/11 shape: rod, fish, ileaojclbaa, sequence.
		hooked := dynamicTestCtx(s, rigRequestTree(t, proto.Guid(rod), proto.Guid(fish[i].FishGUID), wiregen.MustMakeObj("ogcmpcbkgep", nil), proto.U32(1)))
		hooked.Main, hooked.Sub, hooked.Sess = 14, 11, sess
		hFishingHooked(hooked)
		put := dynamicTestCtx(s, rigRequestTree(t, proto.Guid(rod)))
		put.Main, put.Sub, put.Sess, put.ExpectResponse = 17, 8, sess, false
		if body, ok := hRodPut(put); !ok || body != nil {
			t.Fatal("rod/put must be handled without a response or inventory rebuild")
		}
	}
	// Taking a rod reads a snapshot but must not end any cast or replace a fish.
	for _, rod := range rods {
		take := dynamicTestCtx(s, rigRequestTree(t, proto.Guid(rod)))
		take.Main, take.Sub, take.Sess = 17, 7, sess
		if body, ok := hRodTake(take); !ok || len(body) == 0 {
			t.Fatal("could not take a parked rod")
		}
	}
	for i, st := range states {
		st.mu.Lock()
		valid := st.Pending == fish[i] && st.Hooked && st.CastActive && st.InWater && !st.Resolved
		st.mu.Unlock()
		if !valid {
			t.Fatalf("rod %d lost its hooked fish while put down", i+1)
		}
	}
	// Actual 14/4 has no fish GUID. It still addresses the first rod while the
	// third is held, so settlement cannot use the held-rod fallback.
	end := dynamicTestCtx(s, rigRequestTree(t, proto.Guid(rods[0]), wiregen.MustMakeObj("ledehpkjcog", nil), proto.U32(2)))
	end.Main, end.Sub, end.Sess = 14, 4, sess
	if body, ok := hFishingEnd(end); !ok || len(body) == 0 {
		t.Fatal("could not settle the addressed rod")
	}
	for _, st := range states[1:] {
		if !st.CastActive || !st.Hooked || st.Pending == nil {
			t.Fatal("landing one rod changed another rod")
		}
	}
}

func TestEmptyRollKeepsOtherRodWaiting(t *testing.T) {
	for _, weather := range []string{"0", "1"} {
		t.Run("weather="+weather, func(t *testing.T) {
			s, _ := newRigTestServer(t)
			t.Cleanup(func() { stopFishingRecoveryTimers(s) })
			sess := registerBiteTestSession(t, s, 702, nil)
			const uid = int64(1000001)
			setWeatherTestTunable(t, s, "fish_weather_enabled", weather)
			setWeatherTestTunable(t, s, "bite_delay_fixed", "60")
			first := s.selectFishingRod(uid, baitTestRod(t, s))
			fish := &RolledFish{FishGUID: newFishGUID(), Species: "perch", WeightG: 100, Grade: 1}
			first.mu.Lock()
			first.Pending, first.CastActive, first.Hooked, first.PushSent = fish, true, true, true
			first.mu.Unlock()
			rod := baitTestRod(t, s)
			baitTestMount(t, s, rod, "worm_01", 2, 350)
			second := s.selectFishingRod(uid, rod)
			second.mu.Lock()
			second.RigType, second.CastActive, second.InWater = 6, true, true
			second.CastTs = time.Now().Add(-time.Minute)
			old := fishingTimerTicket{second.castGeneration, second.biteTicket}
			s.emptyWeatherBiteLocked(sess, second, time.Now())
			timer, due := second.biteTimer, second.BiteAt
			waiting := timer != nil && !second.Resolved && second.Pending == nil && second.biteTicket > old.sequence
			second.mu.Unlock()
			if !waiting {
				t.Fatal("an empty lure roll permanently stopped the second rod")
			}
			s.selectFishingRod(uid, first.GearGuid)
			s.onRodBiteTimerTicket(uid, second, old)
			second.mu.Lock()
			unchanged := second.biteTimer == timer && second.BiteAt.Equal(due) && !second.Resolved
			second.mu.Unlock()
			if !unchanged || first.Pending != fish || !first.Hooked {
				t.Fatal("switching rods or a stale callback changed an independent cast")
			}
		})
	}
}
