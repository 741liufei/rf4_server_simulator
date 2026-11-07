package gameserver

import (
	"context"
	"math"
	"testing"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

func TestCatchCardProbability(t *testing.T) {
	s, _ := newProgressSrv(t)
	for _, tc := range []struct {
		grade, rarity int64
		want          float64
	}{
		{1, 0, .001}, {2, 0, .001}, {3, 0, .5}, {4, 0, .8}, {3, 1, .125}, {4, 2, .2}, {1, 2, .00025},
	} {
		if got := s.catchCardProbability(tc.grade, tc.rarity); math.Abs(got-tc.want) > 1e-12 {
			t.Fatalf("grade=%d rarity=%d probability=%g want=%g", tc.grade, tc.rarity, got, tc.want)
		}
	}
	s.tunMu.Lock()
	s.tunCache = map[string]float64{"card_drop_blue": .6, "card_drop_rare_scale": .5}
	s.tunAt = time.Now()
	s.tunMu.Unlock()
	if got := s.catchCardProbability(4, 1); math.Abs(got-.3) > 1e-12 {
		t.Fatalf("parameter override = %g", got)
	}
}

func TestCatchCardAppearsInLandingResult(t *testing.T) {
	s, db := newProgressSrv(t)
	seedTradingCards(t, db)
	const uid = int64(7)
	if err := db.TunableSet(t.Context(), "card_drop_blue", "1", "tester"); err != nil {
		t.Fatal(err)
	}
	f := &RolledFish{FishGUID: "33333333333333333333333333333333", Species: "perch", Grade: 4, WeightG: 2100, LengthM: .48, LocLevel: "level_001_lake"}
	st := s.fishingState(uid)
	st.mu.Lock()
	st.Pending, st.Hooked, st.CastActive = f, true, true
	st.mu.Unlock()
	ctx := dynamicTestCtx(s, rigRequestTree(t, proto.Guid(zeroGUID), wiregen.MustMakeObj("ledehpkjcog", nil), proto.U32(1)))
	ctx.Main, ctx.Sub, ctx.Sess.Userid = 14, 4, uid
	read := func(want int) string {
		t.Helper()
		body, ok := hFishingEnd(ctx)
		if !ok || len(body) == 0 {
			t.Fatal("landing did not return a result")
		}
		v, _, err := proto.DecodeValue(body, 0, s.Table())
		if err != nil || v.TypeID != 801 {
			t.Fatal("invalid landing result", err)
		}
		grants := v.Fields[8].Val
		if grants == nil || grants.Kind != proto.KindArgs || grants.Code == nil || *grants.Code != "61" || len(grants.Items) != want {
			t.Fatalf("landing grants = %+v, want %d cards", grants, want)
		}
		if want == 0 {
			return ""
		}
		card := grants.Items[0]
		if card.TypeID != 11301 || card.Fields[0].Val.Fields[16].Hex != f.FishGUID {
			t.Fatal("landing result does not show the card from this fish")
		}
		return card.Fields[1].Hex
	}
	guid := read(1)
	if got := read(1); got != guid {
		t.Fatal("retry replaced the original card")
	}
	var count int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM item WHERE uid=?`, uid).Scan(&count); err != nil || count != 1 {
		t.Fatalf("retry created extra inventory items: %d %v", count, err)
	}
	// An original card already moved into a deck must stay there on retries.
	if _, err := db.SQL.Exec(`UPDATE item SET container='card_deck' WHERE guid=?`, guid); err != nil {
		t.Fatal(err)
	}
	read(0)
}

func TestCatchCardAtomicOnceAcrossRestart(t *testing.T) {
	s, db := newProgressSrv(t)
	seedTradingCards(t, db)
	ctx := context.Background()
	const uid = int64(7)
	// A certain drop isolates accounting from a random probability outcome.
	if err := db.TunableSet(ctx, "card_drop_blue", "1", "tester"); err != nil {
		t.Fatal(err)
	}
	fish := &RolledFish{FishGUID: "11111111111111111111111111111111", Species: "perch", Grade: 4, WeightG: 2100, LengthM: .48, LocLevel: "level_001_lake"}
	state := s.fishingState(uid)
	state.mu.Lock()
	state.Bait = "worm_01"
	state.mu.Unlock()
	first, err := s.settleCatchOnce(ctx, uid, fish, 1)
	if err != nil || first.droppedCardGUID == "" {
		t.Fatalf("first catch: %+v %v", first, err)
	}
	for _, srv := range []*Server{s, newFishingRecoveryServer(t, db)} {
		replay, err := srv.settleCatchOnce(ctx, uid, fish, 1)
		if err != nil || !replay.replayed || replay.droppedCardGUID != first.droppedCardGUID {
			t.Fatalf("replayed catch: %+v %v", replay, err)
		}
	}
	items, err := tradingInventory(ctx, db.SQL, uid)
	if err != nil || len(items) != 1 {
		t.Fatalf("same fish must only grant one card: %d %v", len(items), err)
	}
	fields, err := FieldsFromJSON(items[0].FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	original := fields[0].Val
	if original.Fields[16].Hex != fish.FishGUID || original.Fields[1].U32() != 2100 || original.Fields[3].U32() != 4 || original.Fields[10].StrOr() != fish.LocLevel || original.Fields[8].StrOr() != "worm_01" {
		t.Fatal("dropped card lost original catch data")
	}
	if _, err := db.SQL.Exec(`CREATE TRIGGER fail_card_receipt BEFORE INSERT ON kv WHEN NEW.k LIKE 'catch-settlement:%' BEGIN SELECT RAISE(ABORT,'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	next := *fish
	next.FishGUID = "22222222222222222222222222222222"
	if _, err := s.settleCatchOnce(ctx, uid, &next, 1); err == nil {
		t.Fatal("receipt failure was ignored")
	}
	items, _ = tradingInventory(ctx, db.SQL, uid)
	if len(items) != 1 {
		t.Fatal("failed settlement left a card")
	}
	if _, err := db.SQL.Exec(`DROP TRIGGER fail_card_receipt`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.settleCatchOnce(ctx, uid, &next, 1); err != nil {
		t.Fatal(err)
	}
	items, _ = tradingInventory(ctx, db.SQL, uid)
	if len(items) != 2 {
		t.Fatal("different fish GUID should have its own drop")
	}
}
