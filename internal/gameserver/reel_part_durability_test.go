package gameserver

// 渔轮零件耐久对承力与手感的作用（spec §4）：轴承效率含轴承耐久、齿轮效率含齿轮耐久，
// 泄力、锁轮承力与抛投摩擦三处必须同时跟着变；搏鱼裁决用的承力必须和性能表同源，
// 否则磨了摩擦片或换了旧轴承只改一处，"满红不爆"只修好一半。

import (
	"context"
	"math"
	"testing"

	"rf4server/internal/store"
)

// setReelPartDurabilityForTest 把轮子内嵌零件的耐久写成 want，模拟"这件用旧了"。
func setReelPartDurabilityForTest(t *testing.T, srv *Server, db *store.DB, ctx context.Context,
	reelGUID string, category int64, want float64) {
	t.Helper()
	row, err := db.GetItemByGUID(ctx, reelGUID)
	if err != nil || row == nil {
		t.Fatalf("读取轮子失败: %v", err)
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatalf("解析轮子零件失败: %v", err)
	}
	child, _, _, found := srv.selectedReelPart(ctx, fields, category)
	if !found || child == nil {
		t.Fatalf("出厂轮子应内嵌品类 %d 零件，测试才能模拟磨损", category)
	}
	idx := fieldIdxByCS(child.Class, csDurability)
	if idx < 0 {
		t.Fatalf("品类 %d 的零件没有耐久字段", category)
	}
	setWireFloat(child.Fields, child.Class, idx, want)
	fj, err := FieldsToJSON(fields)
	if err != nil {
		t.Fatalf("序列化零件失败: %v", err)
	}
	if err := db.UpdateItemFields(ctx, row.GUID, fj, row.Qty, row.Durability, row.Workmanship, row.RigGUID); err != nil {
		t.Fatalf("写回零件耐久失败: %v", err)
	}
}

// reelPerfForTest 重新同步并返回性能表（每次都用库里的最新行）。
func reelPerfForTest(t *testing.T, srv *Server, db *store.DB, ctx context.Context, reelGUID string) *store.ReelPerformance {
	t.Helper()
	row, err := db.GetItemByGUID(ctx, reelGUID)
	if err != nil || row == nil {
		t.Fatalf("读取轮子失败: %v", err)
	}
	perf, err := srv.syncReelPerformance(ctx, row)
	if err != nil || perf == nil {
		t.Fatalf("重建轮子物理参数失败: %v", err)
	}
	return perf
}

func TestReelPartDurabilityLowersDragAndLockStrength(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	// Spark 2 2000S 出厂内嵌摩擦片/轴承/齿轮/线杯/摇柄/轮油六件，是能模拟零件磨损的整轮。
	reel, err := srv.CreateItem(ctx, uid, "spin_5820", "backpack", 1)
	if err != nil {
		t.Fatalf("造轮失败: %v", err)
	}

	_, bearingQuality, bearingDurability := srv.reelPartStateFromRow(ctx, reel, reelPartCatBearing)
	if bearingDurability != 1 {
		t.Fatalf("出厂轴承耐久应为 1，实得 %v", bearingDurability)
	}
	fresh := reelPerfForTest(t, srv, db, ctx, reel.GUID)
	if fresh.BaseDragKg <= 0 || fresh.LockedDragKg <= 0 {
		t.Fatalf("出厂轮子应有泄力与锁轮承力: %+v", fresh)
	}
	// 搏鱼裁决与性能表必须同源：同一行、同一算法，两个数字必须一模一样。
	row, err := db.GetItemByGUID(ctx, reel.GUID)
	if err != nil || row == nil {
		t.Fatalf("读取轮子失败: %v", err)
	}
	if got := srv.reelLockedDragFromRow(ctx, row); math.Abs(got-fresh.LockedDragKg) > 1e-9 {
		t.Fatalf("搏鱼裁决承力与性能表不同源：裁决 %.6f 性能表 %.6f", got, fresh.LockedDragKg)
	}

	// 轴承用旧到剩 0.2：摩擦片没动，但泄力、锁轮承力都要降，抛投也更涩。
	setReelPartDurabilityForTest(t, srv, db, ctx, reel.GUID, reelPartCatBearing, 0.2)
	wornRow, err := db.GetItemByGUID(ctx, reel.GUID)
	if err != nil || wornRow == nil {
		t.Fatalf("读取轮子失败: %v", err)
	}
	// 耐久字段走 U32 承载 F32 位型，写 0.2 读回带 float32 噪声，后续期望一律用读回值。
	_, _, wornBearingDur := srv.reelPartStateFromRow(ctx, wornRow, reelPartCatBearing)
	if math.Abs(wornBearingDur-0.2) > 1e-6 {
		t.Fatalf("轴承耐久没写进去，实得 %v", wornBearingDur)
	}
	worn := reelPerfForTest(t, srv, db, ctx, reel.GUID)
	if math.Abs(worn.BaseDragKg-fresh.BaseDragKg) > 1e-9 {
		t.Fatalf("摩擦片没动，基础制动力不该变：%.6f -> %.6f", fresh.BaseDragKg, worn.BaseDragKg)
	}
	// 泄力与锁轮承力都必须按轴承效率同比降下来。
	lost := reelBearingFactor(bearingQuality, wornBearingDur) / reelBearingFactor(bearingQuality, 1)
	wantDrag := reelDragAtSetting(fresh, defaultReelDragSetting) * lost
	if got := reelDragAtSetting(worn, defaultReelDragSetting); math.Abs(got-wantDrag) > 1e-9 {
		t.Fatalf("旧轴承应把泄力压到 %.6f，实得 %.6f", wantDrag, got)
	}
	wantLocked := fresh.LockedDragKg * lost
	if math.Abs(worn.LockedDragKg-wantLocked) > 1e-9 {
		t.Fatalf("旧轴承应把锁轮承力压到 %.6f，实得 %.6f", wantLocked, worn.LockedDragKg)
	}
	if worn.CastFriction <= fresh.CastFriction {
		t.Fatalf("旧轴承应让抛投更涩：新轮 %f 旧轮 %f", fresh.CastFriction, worn.CastFriction)
	}
	// 换了新轴承能整段找回来：耐久写回 1，三个数字全部复位。
	setReelPartDurabilityForTest(t, srv, db, ctx, reel.GUID, reelPartCatBearing, 1)
	restored := reelPerfForTest(t, srv, db, ctx, reel.GUID)
	if math.Abs(restored.LockedDragKg-fresh.LockedDragKg) > 1e-9 || restored.CastFriction != fresh.CastFriction {
		t.Fatalf("换新轴承应恢复承力与手感：locked %.6f->%.6f friction %f->%f",
			fresh.LockedDragKg, restored.LockedDragKg, fresh.CastFriction, restored.CastFriction)
	}

	// 齿轮只进传动链：磨完齿轮压锁轮承力，但不该动泄力档位上的制动力。
	_, gearQuality, _ := srv.reelPartStateFromRow(ctx, wornRow, reelPartCatGear)
	setReelPartDurabilityForTest(t, srv, db, ctx, reel.GUID, reelPartCatGear, 0)
	gwear := reelPerfForTest(t, srv, db, ctx, reel.GUID)
	if math.Abs(gwear.BaseDragKg-fresh.BaseDragKg) > 1e-9 {
		t.Fatalf("齿轮不该改基础制动力：%.6f -> %.6f", fresh.BaseDragKg, gwear.BaseDragKg)
	}
	if got := reelDragAtSetting(gwear, defaultReelDragSetting); math.Abs(got-reelDragAtSetting(fresh, defaultReelDragSetting)) > 1e-9 {
		t.Fatalf("泄力档位不含齿轮效率，不该随齿轮磨损变化：%.6f -> %.6f",
			reelDragAtSetting(fresh, defaultReelDragSetting), got)
	}
	lostGear := reelGearFactor(gearQuality, 0) / reelGearFactor(gearQuality, 1)
	wantGearLocked := fresh.LockedDragKg * lostGear
	if math.Abs(gwear.LockedDragKg-wantGearLocked) > 1e-9 {
		t.Fatalf("磨完齿轮应把锁轮承力压到 %.6f，实得 %.6f", wantGearLocked, gwear.LockedDragKg)
	}
}
