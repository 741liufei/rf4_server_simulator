package gameserver

import (
	"context"
	"math"
	"testing"
)

// TestReportedReelBreakDamagesReel 复刻海图实测：轻纺车竿（Arrow S60UL）
// 竿身 3.08kg、摩擦片额定 3.2kg 折算后锁轮承力 3.89kg、主线 6.8kg，竿身是最弱环。
// 客户端 UI 显示 1~30，17/14 上报 0~29（UI = 线上值 + 1）。17/9 上报渔轮断件时，
// 服务端按上报件损坏渔轮本体，不再改判成“无损坏的泄力打滑”，也不改断最弱的竿身。
func TestReportedReelBreakDamagesReel(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "spin_10003_S60UL", "backpack", 1)
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

	// 线上 28（UI 29）：制动力低于竿身承力，但客户端上报断件后仍要断。
	if err := db.UpsertRodSetting(ctx, rod.GUID, reelDragSettingKey, "28"); err != nil {
		t.Fatal(err)
	}
	spec := srv.loadGearSpecForRig(ctx, srv.rigGearIDs(ctx, uid, rod.GUID), rod.GUID)
	if !(spec.ReelCurrentDragKg > 0 && spec.ReelCurrentDragKg < spec.RodPowerKg) {
		t.Fatalf("UI 29 档制动力应低于竿身承力: drag=%f rod=%f", spec.ReelCurrentDragKg, spec.RodPowerKg)
	}
	if !(spec.RodPowerKg < spec.ReelDragKg && spec.LineStrengthKg > spec.RodPowerKg) {
		t.Fatalf("测试钓组应满足 线>锁轮>竿: line=%f lock=%f rod=%f",
			spec.LineStrengthKg, spec.ReelDragKg, spec.RodPowerKg)
	}
	outcome, err := srv.breakLineOrRod(ctx, uid, rod.GUID, reel.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.ReelDamaged || !outcome.ReelBroken || outcome.FrictionWorn || outcome.LineBroken {
		t.Fatalf("上报渔轮断件应直接把渔轮判坏: %+v", outcome)
	}
	if fresh, _ := db.GetItemByGUID(ctx, reel.GUID); fresh == nil || fresh.Durability != 0 {
		t.Fatalf("上报渔轮断件后耐久应直接归 0: %+v", fresh)
	}
	if fresh, _ := db.GetItemByGUID(ctx, rod.GUID); fresh == nil || fresh.Durability != 1 {
		t.Fatalf("上报渔轮断件不应伤到竿体: %+v", fresh)
	}
	// 官服断轮口径：钓组原样保留、鱼仍挂钩，只是无法收线。
	if row, _ := db.GetItemByGUID(ctx, line.GUID); row == nil || row.Container == "backpack" {
		t.Fatalf("渔轮损坏后主线应仍装在竿上: %+v", row)
	}
	if got := rigSlotGUIDs(t, srv, rod.GUID); got[catLine] != line.GUID || got[catReel] != reel.GUID {
		t.Fatalf("渔轮损坏后钓组应原样保留: %+v", got)
	}
	if len(outcome.RemovedGUIDs) != 0 {
		t.Fatalf("渔轮损坏不应摘除任何组件: %+v", outcome.RemovedGUIDs)
	}
	if !outcome.KeepFight {
		t.Fatalf("渔轮损坏应保持搏鱼（鱼仍挂钩）: %+v", outcome)
	}
}

// TestReelDragWireZeroIsValidSetting 锁定 0 基映射：客户端 UI 1 上报线上 0，
// 这是有效的最小泄力档，不能当成“没有设置”而回退到锁轮。
func TestReelDragWireZeroIsValidSetting(t *testing.T) {
	t.Parallel()
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

	if err := db.UpsertRodSetting(ctx, rod.GUID, reelDragSettingKey, "0"); err != nil {
		t.Fatal(err)
	}
	setting, ok := srv.rodReelDragSetting(ctx, rod.GUID)
	if !ok || setting != 0 {
		t.Fatalf("线上 0 应是最小泄力档而不是缺省: setting=%d ok=%v", setting, ok)
	}
	spec := srv.loadGearSpecForRig(ctx, srv.rigGearIDs(ctx, uid, rod.GUID), rod.GUID)
	if !(spec.ReelCurrentDragKg > 0 && spec.ReelCurrentDragKg < spec.RodPowerKg) {
		t.Fatalf("UI 1 档制动力应低于竿身承力: drag=%f rod=%f", spec.ReelCurrentDragKg, spec.RodPowerKg)
	}
}

// TestReelSettingsDefaultWhenMissing 锁定：没有 17/14/17/15 记录时按线上 15
// （UI 16）兜底，不能当成锁轮；默认档位不把渔轮加进最弱环候选。
func TestReelSettingsDefaultWhenMissing(t *testing.T) {
	t.Parallel()
	srv, _ := newRigTestServer(t)
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

	if got, ok := srv.rodReelDragSetting(ctx, rod.GUID); !ok || got != defaultReelDragSetting {
		t.Fatalf("缺省摩擦档位=%d ok=%v，期望 %d", got, ok, defaultReelDragSetting)
	}
	spec := srv.loadGearSpecForRig(ctx, srv.rigGearIDs(ctx, uid, rod.GUID), rod.GUID)
	if !(spec.ReelCurrentDragKg > 0 && spec.ReelCurrentDragKg < spec.RodPowerKg) {
		t.Fatalf("缺省档位应按泄力算: drag=%f rod=%f", spec.ReelCurrentDragKg, spec.RodPowerKg)
	}
	if decision, derr := srv.resolveGearBreak(ctx, uid, rod.GUID, reel.GUID); derr != nil ||
		decision.Part == "reel" || decision.ReelSlip {
		t.Fatalf("缺省档位不能当锁轮: %+v err=%v", decision, derr)
	}
}

// TestLineBreakReportHonoredBelowLockDrag 锁定：客户端明确上报断线时，
// 摩擦 23/24/28 档不能被否决成“泄力打滑”；服务端只按最弱环决定断哪一件。
func TestLineBreakReportHonoredBelowLockDrag(t *testing.T) {
	t.Parallel()
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
	line, err := srv.CreateItem(ctx, uid, "mono_15065", "backpack", 1) // 3.1kg，比竿弱
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, reel.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)

	if err := db.UpsertRodSetting(ctx, rod.GUID, reelDragSettingKey, "24"); err != nil {
		t.Fatal(err)
	}
	decision, err := srv.resolveGearBreak(ctx, uid, rod.GUID, line.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Part != "line" || decision.ReelSlip {
		t.Fatalf("客户端上报断线时不能被泄力档位否决: %+v", decision)
	}
	outcome, err := srv.breakLineOrRod(ctx, uid, rod.GUID, line.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.LineBroken || outcome.BreakPart != "line" {
		t.Fatalf("断线报告应落成 line break: %+v", outcome)
	}
	if fresh, _ := db.GetItemByGUID(ctx, rod.GUID); fresh == nil || fresh.Durability <= 0 {
		t.Fatal("断线不应把竿判成损坏")
	}
}

func TestReelDragWorkmanshipAppliedOnce(t *testing.T) {
	t.Parallel()
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
	// csDragForce 已是持久化磨损状态，字段存在时不再按母轮成色重算。
	// 本例专门验证"仅字段缺失时初始化"这条路径只应用一次成色：先清掉
	// 子件字段，再让 canonicalize 按 0.8 成色补齐。
	drag, _, _, ok := srv.selectedReelPart(ctx, fields, 9)
	if !ok || drag == nil {
		t.Fatal("轮子缺少摩擦片(cat9)子实例")
	}
	dragIdx := fieldIdxByCS(drag.Class, csDragForce)
	if dragIdx < 0 || dragIdx >= len(drag.Fields) {
		t.Fatal("摩擦片缺少制动力字段")
	}
	drag.Fields[dragIdx].Num = nil
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
