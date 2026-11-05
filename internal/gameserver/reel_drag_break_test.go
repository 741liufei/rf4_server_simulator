package gameserver

import (
	"context"
	"math"
	"testing"
)

// TestReelDragSettingGatesRodBreak 复刻海图实测：轻纺车竿竿身 3.91kg、
// 渔轮最大摩擦 3.2kg、主线 6.8kg。摩擦 1~29 档超过制动力只会打滑放线，
// 竿身承受不到更高的张力；只有第 30 档锁轮才可能把竿压成最弱环。
func TestReelDragSettingGatesRodBreak(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "spin_10001_S60L", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	reel, err := srv.CreateItem(ctx, uid, "spin_5820", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_15249", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, reel.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)

	// 摩擦 29 档：制动力低于竿身承力，超载只会爆轮。
	if err := db.UpsertRodSetting(ctx, rod.GUID, reelDragSettingKey, "29"); err != nil {
		t.Fatal(err)
	}
	spec := srv.loadGearSpecForRig(ctx, srv.rigGearIDs(ctx, uid, rod.GUID), rod.GUID)
	if !(spec.ReelCurrentDragKg > 0 && spec.ReelCurrentDragKg < spec.RodPowerKg) {
		t.Fatalf("29 档制动力应低于竿身承力: drag=%f rod=%f", spec.ReelCurrentDragKg, spec.RodPowerKg)
	}
	if !(spec.RodPowerKg < spec.ReelDragKg && spec.LineStrengthKg > spec.RodPowerKg) {
		t.Fatalf("测试钓组应满足 线>锁轮>竿: line=%f lock=%f rod=%f",
			spec.LineStrengthKg, spec.ReelDragKg, spec.RodPowerKg)
	}
	if decision, derr := srv.resolveGearBreak(ctx, uid, rod.GUID, reel.GUID); derr != nil || decision.Part != "reel" {
		t.Fatalf("29 档超载应判渔轮: %+v err=%v", decision, derr)
	}
	outcome, err := srv.breakLineOrRod(ctx, uid, rod.GUID, reel.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.ReelDamaged || outcome.BreakPart != "reel" || len(outcome.RemovedGUIDs) != 0 {
		t.Fatalf("29 档爆轮只应掉耐久: %+v", outcome)
	}
	for _, guid := range []string{rod.GUID, reel.GUID, line.GUID} {
		row, gerr := db.GetItemByGUID(ctx, guid)
		if gerr != nil || row == nil {
			t.Fatalf("爆轮后 %s 不应消失: %v", guid, gerr)
		}
	}
	if fresh, _ := db.GetItemByGUID(ctx, rod.GUID); fresh.Durability != 1 {
		t.Fatalf("爆轮不应伤到竿体: %f", fresh.Durability)
	}

	// 第 30 档锁轮：制动力压过竿身，这时才轮到断竿。
	if err := db.SetItemDurability(ctx, reel.GUID, 1); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertRodSetting(ctx, rod.GUID, reelDragSettingKey, "30"); err != nil {
		t.Fatal(err)
	}
	locked := srv.loadGearSpecForRig(ctx, srv.rigGearIDs(ctx, uid, rod.GUID), rod.GUID)
	if !(locked.ReelCurrentDragKg > locked.RodPowerKg) {
		t.Fatalf("锁轮制动力应高于竿身承力: drag=%f rod=%f", locked.ReelCurrentDragKg, locked.RodPowerKg)
	}
	if decision, derr := srv.resolveGearBreak(ctx, uid, rod.GUID, reel.GUID); derr != nil || decision.Part != "rod" {
		t.Fatalf("锁轮应判断竿: %+v err=%v", decision, derr)
	}
}

func TestReelDragWorkmanshipAppliedOnce(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	reel, err := srv.CreateItem(ctx, 1000001, "spin_5820", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	reel.Workmanship = 0.8
	if err := db.UpdateItemFields(ctx, reel.GUID, reel.FieldsJSON, reel.Qty, reel.Durability, reel.Workmanship, reel.RigGUID); err != nil {
		t.Fatal(err)
	}
	reel, err = db.GetItemByGUID(ctx, reel.GUID)
	if err != nil || reel == nil {
		t.Fatal(err)
	}
	fields, err := FieldsFromJSON(reel.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	srv.canonicalizeItemPhysicalFields(ctx, reel, fields)
	fj, err := FieldsToJSON(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateItemFields(ctx, reel.GUID, fj, reel.Qty, reel.Durability, reel.Workmanship, reel.RigGUID); err != nil {
		t.Fatal(err)
	}
	reel, err = db.GetItemByGUID(ctx, reel.GUID)
	if err != nil || reel == nil {
		t.Fatal(err)
	}
	def, err := srv.itemDefForRow(ctx, reel)
	if err != nil || def == nil {
		t.Fatal(err)
	}
	want := reelDragMaxKg(def) * reel.Workmanship
	got := srv.reelBaseDragFromRow(ctx, reel)
	if math.Abs(got-want) > 0.01 {
		t.Fatalf("制动力=%f，期望只乘一次母轮成色后的 %f", got, want)
	}
}
