package store

import (
	"context"
	"testing"
)

func TestHookBiteProfileSyncPreservesTuning(t *testing.T) {
	db := openIntegrityTestDB(t)
	ctx := context.Background()
	want := HookBiteProfile{
		ItemID:      "triple_test",
		NumericID:   9001,
		Size:        25,
		LengthMM:    18,
		StrengthKg:  150,
		WireWeightG: 5.5,
		Quality:     100,
		Family:      "treble",
		Brand:       "test-brand",
	}
	if err := db.UpsertHookBiteProfile(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetHookBiteProfile(ctx, want.ItemID)
	if err != nil || got == nil {
		t.Fatalf("读取档案: profile=%+v err=%v", got, err)
	}
	if got.SolidScale != 1 || got.HoldScale != 1 ||
		got.SmallFishFloor != 0 || got.OverCapacityHoldFloor != 0 {
		t.Fatalf("默认调参值错误: %+v", got)
	}

	if err := db.SetHookBiteProfileTuning(ctx, want.ItemID, 1.25, 0.8, 0.35, 0.6); err != nil {
		t.Fatal(err)
	}
	want.StrengthKg = 160
	want.Brand = "updated-brand"
	want.UpdatedTS = 99
	if err := db.UpsertHookBiteProfile(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err = db.GetHookBiteProfile(ctx, want.ItemID)
	if err != nil || got == nil {
		t.Fatalf("重读档案: profile=%+v err=%v", got, err)
	}
	if got.StrengthKg != 160 || got.Brand != "updated-brand" || got.UpdatedTS != 99 {
		t.Fatalf("真实物理字段未刷新: %+v", got)
	}
	if got.SolidScale != 1.25 || got.HoldScale != 0.8 ||
		got.SmallFishFloor != 0.35 || got.OverCapacityHoldFloor != 0.6 {
		t.Fatalf("同步覆盖了人工调参: %+v", got)
	}

	if err := db.Migrate(); err != nil {
		t.Fatalf("迁移重复执行失败: %v", err)
	}
	rows, err := db.ListHookBiteProfiles(ctx)
	if err != nil || len(rows) != 1 || rows[0].ItemID != want.ItemID {
		t.Fatalf("列表或重复迁移异常: rows=%+v err=%v", rows, err)
	}
}
