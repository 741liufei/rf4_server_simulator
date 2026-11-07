package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestFishingStateRoundTrip(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "fishing-state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}

	castAt := time.Unix(1_790_000_000, 123_456_789)
	fallAt := castAt.Add(1200 * time.Millisecond)
	biteAt := castAt.Add(13 * time.Second)
	want := &FishingState{
		UID:            1000001,
		RodGUID:        "11111111111111111111111111111111",
		GearGUID:       "11111111111111111111111111111111",
		RigType:        12,
		Loc:            "level_019_american_pond",
		Spot:           9876,
		DepthCM:        375,
		CastActive:     true,
		CastTS:         castAt,
		FallTS:         fallAt,
		FallSeen:       true,
		RigSettled:     true,
		TickSeen:       true,
		LastAnim:       0x800,
		PushSent:       true,
		Hooked:         true,
		Resolved:       false,
		Bait:           "worm_01",
		LastSkill:      3,
		BiteSpeed:      0.75,
		TrophyLuck:     0.12,
		FightTolerance: 0.18,
		BiteAt:         biteAt,
		Pending: &FishingFish{
			FishGUID: "22222222222222222222222222222222",
			Species:  "c.roach",
			Grade:    2,
			WeightG:  318,
			LengthM:  0.245,
			XP:       21,
			Silver:   17,
			LocLevel: "level_019_american_pond",
			NamedID:  "named-1",
			Name:     "Silver",
		},
		Armed: &FishingFish{
			FishGUID: "33333333333333333333333333333333",
			Species:  "perch",
			Grade:    3,
			WeightG:  650,
			LengthM:  0.41,
			LocLevel: "level_001_lake",
		},
	}
	ctx := context.Background()
	if err := db.SaveFishingState(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := db.LoadFishingState(ctx, want.UID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch:\n got=%+v\nwant=%+v", got, want)
	}

	// UPSERT 必须清空已消费的 pending/armed，不能把上一杆残值留在库中。
	want.Pending = nil
	want.Armed = nil
	want.CastActive = false
	want.Resolved = true
	if err := db.SaveFishingState(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err = db.LoadFishingState(ctx, want.UID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("upsert mismatch:\n got=%+v\nwant=%+v", got, want)
	}
}

func TestLoadFishingStateMissing(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "fishing-state-missing.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	got, err := db.LoadFishingState(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("无记录应返回 nil，实得 %+v", got)
	}
}
