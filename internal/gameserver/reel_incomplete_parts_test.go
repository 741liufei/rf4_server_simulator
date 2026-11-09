package gameserver

import (
	"context"
	"testing"
)

func TestEveryIncompleteReelDefinitionHasFallbackParts(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()

	defs, err := db.ListItemDefsByCategory(ctx, gearCatReel)
	if err != nil {
		t.Fatal(err)
	}
	incomplete := 0
	for _, def := range defs {
		if reelCorePartsComplete(defAttrs(def)) {
			continue
		}
		incomplete++
		attrs := srv.reelMaterializationAttrs(ctx, def)
		if !reelCorePartsComplete(attrs) {
			t.Errorf("轮型 %s 仍无法取得完整六件模板", def.ItemID)
			continue
		}
		for _, key := range reelCorePartKeys {
			cfg, _ := attrs[key].(map[string]any)
			subID, _ := cfg["nhjofmmmngl"].(string)
			subDef, err := db.GetItemDefByItemID(ctx, subID)
			if err != nil || subDef == nil {
				t.Errorf("轮型 %s 的零件 %s 无法解析: %v", def.ItemID, subID, err)
			}
		}
	}
	if incomplete == 0 {
		t.Fatal("测试数据中没有占位轮型，无法覆盖本修复")
	}
}

func TestIncompleteReelMaterializesAndWearsFrictionParts(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	reel, err := srv.CreateItem(ctx, uid, "spin_5231", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	fields := snapshotFields(t, reel.FieldsJSON)
	for _, category := range []int64{
		reelPartCatSpool, reelPartCatWasher, reelPartCatBearing,
		reelPartCatHandle, reelPartCatOil, reelPartCatGear,
	} {
		if _, def, _, ok := srv.selectedReelPart(ctx, fields, category); !ok || def == nil {
			t.Fatalf("占位轮型缺少品类 %d 的零件", category)
		}
	}
	if rated, ok := srv.reelWasherRatedFromRow(ctx, reel); !ok || rated <= 0 {
		t.Fatalf("占位轮型摩擦片额定制动力无效: rated=%v ok=%v", rated, ok)
	}
	before := srv.reelBaseDragFromRow(ctx, reel)
	if before <= 0 {
		t.Fatalf("占位轮型基础制动力无效: %v", before)
	}
	wornOut, found, err := srv.degradeFrictionWasherBy(ctx, reel, 0.1)
	if err != nil || !found || wornOut {
		t.Fatalf("摩擦片磨损失败: wornOut=%v found=%v err=%v", wornOut, found, err)
	}
	fresh := mustItem(t, db, reel.GUID)
	after := srv.reelBaseDragFromRow(ctx, fresh)
	if after >= before {
		t.Fatalf("摩擦片磨损没有降低制动力: before=%v after=%v", before, after)
	}
	tree, err := srv.ItemToTree(ctx, fresh)
	if err != nil {
		t.Fatal(err)
	}
	if strength := snapshotFloat(tree.Fields, tree.Class, csStrength); strength <= 0 {
		t.Fatalf("占位轮型下发树没有有效制动力: %v", strength)
	}
	if perf, err := srv.syncReelPerformance(ctx, fresh); err != nil || perf == nil || perf.BaseDragKg <= 0 || perf.LockedDragKg <= 0 {
		t.Fatalf("占位轮型性能表未建立: perf=%+v err=%v", perf, err)
	}
}

func TestRepairReelComponentsBackfillsLegacyIncompleteReel(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	reel, err := srv.CreateItem(ctx, uid, "spin_5231", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	fields := snapshotFields(t, reel.FieldsJSON)
	for _, key := range reelPartKeys {
		idx := fieldIdxByCS(reel.WireClass, key)
		if idx >= 0 && idx < len(fields) {
			fields[idx].Val = nil
		}
	}
	raw, err := FieldsToJSON(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateItemFields(ctx, reel.GUID, raw, reel.Qty, reel.Durability, reel.Workmanship, reel.RigGUID); err != nil {
		t.Fatal(err)
	}
	if n, err := srv.RepairReelComponents(ctx); err != nil || n < 1 {
		t.Fatalf("历史占位轮型修复失败: n=%d err=%v", n, err)
	}
	fresh := mustItem(t, db, reel.GUID)
	fields = snapshotFields(t, fresh.FieldsJSON)
	for _, category := range []int64{
		reelPartCatSpool, reelPartCatWasher, reelPartCatBearing,
		reelPartCatHandle, reelPartCatOil, reelPartCatGear,
	} {
		if _, def, _, ok := srv.selectedReelPart(ctx, fields, category); !ok || def == nil {
			t.Fatalf("修复后仍缺少品类 %d 的零件", category)
		}
	}
	if rated, ok := srv.reelWasherRatedFromRow(ctx, fresh); !ok || rated <= 0 {
		t.Fatalf("修复后摩擦片制动力无效: rated=%v ok=%v", rated, ok)
	}
}
