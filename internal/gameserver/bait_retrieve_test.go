package gameserver

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

func TestTelescopicRetrieveReloadsBaitBeforeNextCast(t *testing.T) {
	for _, quantity := range []int64{1, 3} {
		t.Run(fmt.Sprint(quantity), func(t *testing.T) {
			s, db := newRigTestServer(t)
			sess := newProbeSess(s)
			if _, err := db.EnsureProfile(t.Context(), sess.Userid); err != nil {
				t.Fatal(err)
			}
			rod, err := s.CreateItem(t.Context(), sess.Userid, "tele_10012_tl16", "backpack", 1)
			if err != nil {
				t.Fatal(err)
			}
			hook := mountHookForBaitTest(t, s, rod.GUID, 0)
			baitTestMount(t, s, rod.GUID, "worm_01", 1, 350)
			if quantity > 1 {
				spare, err := s.CreateItem(t.Context(), sess.Userid, "worm_01", "backpack", quantity-1)
				if err != nil {
					t.Fatal(err)
				}
				s.setStackCount(t.Context(), spare, quantity-1)
			}
			st := s.stateFor(sess.Userid)
			for caught := int64(1); caught <= quantity; caught++ {
				groups := s.baitedHookSets(t.Context(), rod.GUID)
				if len(groups) != 1 || groups[0].hookGUID != hook {
					t.Fatalf("catch %d has no baited hook: %+v", caught, groups)
				}
				fish := &RolledFish{FishGUID: fmt.Sprintf("%032x", caught), Species: "perch", Grade: 1,
					WeightG: 123, LengthM: .15, HookGUID: hook, BaitsKnown: true,
					BaitGUIDs: strings.Join(groups[0].baitGUIDs, " ")}
				st.mu.Lock()
				st.GearGuid, st.RigType = rod.GUID, 1
				st.Pending, st.Hooked, st.CastActive, st.InWater, st.Resolved = fish, true, true, true, false
				st.mu.Unlock()
				if err := s.consumeHookedBait(sess, fish, rod.GUID); err != nil {
					t.Fatal(err)
				}
				if caught == 1 && len(s.currentBaits(t.Context(), rod.GUID)) != 0 {
					t.Fatal("replacement bait was mounted during the fight")
				}
				for replay := 0; replay < 2; replay++ {
					retrieve := &Ctx{Sess: sess, Log: logging.Discard()}
					body, ok := hFishingEnd(retrieve)
					if !ok || len(body) == 0 {
						t.Fatal("retrieve failed")
					}
					for _, callback := range retrieve.afterResponse {
						callback()
					}
					wantMounted := 1
					if caught == quantity {
						wantMounted = 0
					}
					if got := len(s.currentBaits(t.Context(), rod.GUID)); got != wantMounted {
						t.Fatalf("catch %d retrieve replay %d mounted %d baits, want %d before next cast", caught, replay, got, wantMounted)
					}
				}
				if _, ok := hFishingRelease(&Ctx{Sess: sess, Log: logging.Discard()}); !ok {
					t.Fatal("release failed")
				}
				if err := sess.prepareBaitForCast(st, rod.GUID); err != nil {
					t.Fatal(err)
				}
				var remaining int64
				if err := db.SQL.QueryRow(`SELECT COALESCE(SUM(qty),0) FROM item WHERE uid=? AND item_id='worm_01'`, sess.Userid).Scan(&remaining); err != nil {
					t.Fatal(err)
				}
				if remaining != quantity-caught {
					t.Fatalf("catch %d left %d baits, want %d", caught, remaining, quantity-caught)
				}
			}
		})
	}
}

func TestRetrievePushesReplacementBaitAndRod(t *testing.T) {
	s, db := newRigTestServer(t)
	rodGUID := baitTestRod(t, s)
	rod, err := db.GetItemByGUID(t.Context(), rodGUID)
	if err != nil || rod == nil {
		t.Fatalf("test rod missing: %v", err)
	}
	mountHookForBaitTest(t, s, rod.GUID, 0)
	source, bait := baitTestMount(t, s, rod.GUID, "worm_01", 3, 350)
	before, err := db.GetItemByGUID(t.Context(), source)
	if err != nil || before == nil {
		t.Fatalf("source bait missing: %v", err)
	}
	beforeFields, err := FieldsFromJSON(before.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	versionIndex := fieldIdxByCS(before.WireClass, csCreated)
	previousVersion := u64Of(beforeFields[versionIndex].Num)
	if _, err := s.consumeBaitEvent(t.Context(), 1000001, rod.GUID, "escaped", []string{bait}, false); err != nil {
		t.Fatal(err)
	}
	sess, peer := newTunnelProbe(t, s)
	received := receiveTestPush(peer, 4, 22)
	st := s.stateFor(sess.Userid)
	st.mu.Lock()
	st.GearGuid, st.RigType, st.CastActive = rod.GUID, s.rigTypeForWrite(t.Context(), rod.GUID), true
	st.mu.Unlock()
	retrieve := &Ctx{Sess: sess, Log: logging.Discard()}
	if body, ok := hFishingEnd(retrieve); !ok || len(body) == 0 {
		t.Fatal("empty retrieve failed")
	}
	if len(retrieve.afterResponse) != 1 {
		t.Fatalf("retrieve queued %d replacement pushes, want 1 after response", len(retrieve.afterResponse))
	}
	select {
	case <-received:
		t.Fatal("replacement push was sent before the retrieve response")
	default:
	}
	retrieve.afterResponse[0]()
	select {
	case env := <-received:
		if env == nil {
			t.Fatal("replacement push was closed")
		}
		tree, _, err := proto.DecodeValue(env.Body, 0, sess.Table())
		if err != nil {
			t.Fatal(err)
		}
		groups := s.baitedHookSets(t.Context(), rod.GUID)
		if len(groups) != 1 {
			t.Fatal("replacement bait missing")
		}
		guids := strings.Join(deepGUIDs(tree), " ")
		if !strings.Contains(guids, rod.GUID) || !strings.Contains(guids, groups[0].baitGUIDs[0]) {
			t.Fatalf("replacement push omitted rod or bait: %s", guids)
		}
		var sourceTree *proto.Value
		for _, item := range tree.Items {
			if item.Kind == proto.KindObj && item.Class == before.WireClass {
				if guid := item.Field(fieldIdxByCS(before.WireClass, csGUID)); guid.Hex == source {
					sourceTree = item
					break
				}
			}
		}
		if sourceTree == nil {
			t.Fatal("replacement push omitted source stack")
		}
		quantityField := sourceTree.Field(fieldIdxByCS(before.WireClass, csQty))
		if qty := quantityField.U32(); qty != 1 {
			t.Fatalf("source stack push quantity = %d, want 1", qty)
		}
		versionField := sourceTree.Field(versionIndex)
		if version := versionField.U64(); version <= previousVersion {
			t.Fatalf("source stack update has stale version %d <= %d", version, previousVersion)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("retrieve did not push replacement bait before the next cast")
	}
}
