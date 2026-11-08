package gameserver

import (
	"context"
	"testing"

	"rf4server/internal/store"
)

func TestRestoredRecordUsesFishingRulerAndTrophyCards(t *testing.T) {
	t.Parallel()
	s, db := newDetachTestServer(t)
	ctx := context.Background()
	const uid int64 = 1000001
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`INSERT OR REPLACE INTO loc_alias(alias,loc) VALUES('level_001_lake','湖')`, `INSERT OR REPLACE INTO map_fish_roster(loc,fish_key) VALUES('level_001_lake','perch')`} {
		if _, err := db.SQL.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	r, err := PrepareRestoredRecord(ctx, db, "perch", 4, 0)
	if err != nil {
		t.Fatal(err)
	}
	if s.rollGradeDB(ctx, "perch", r.WeightG) != 4 || s.rollGradeDB(ctx, "perch", r.WeightG-1) == 4 {
		t.Fatalf("wrong lower bound: %+v", r)
	}
	if _, err := PrepareRestoredRecord(ctx, db, "perch", 3, r.WeightG); err == nil {
		t.Fatal("mismatched weight accepted")
	}
	if err := db.RestoreFishRecords(ctx, "test", uid, "level_001_lake", []store.CatchRecord{r}, "fixture"); err != nil {
		t.Fatal(err)
	}
	cards, err := s.buildFishTrophies323(ctx, uid)
	if err != nil || cards.ArgCount() != 1 {
		t.Fatalf("trophy groups: %v %v", cards, err)
	}
	group := cards.Items[0]
	if *group.Field(0).Str != "level_001_lake" {
		t.Fatal("wrong map")
	}
	card := group.Field(1).Val.Items[0]
	if *card.Field(0).Str != "perch" || *card.Field(1).Num != 4 || *card.Field(2).Num != uint64(r.WeightG) {
		t.Fatalf("wrong trophy card: %+v", card)
	}
	other, err := s.buildFishTrophies323(ctx, uid+1)
	if err != nil || other.ArgCount() != 0 {
		t.Fatal("other player's trophy changed")
	}
}
