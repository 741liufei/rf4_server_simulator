package gameserver

// 提取物（dip，类别 58）与竿架（类别 79）实例类回归（2026-11-05）：
// 客户端背包格子把这两族物品的 DTO 强转成 pphmpibfgea（Player.log
// InvalidCastException 456 次实证），只有 abmokghdbhf(11500) 的 DTO 是它。
// 早期没有映射时落成钓线类 ohciikgjghe，会让按 I 打开背包永远转圈。

import (
	"context"
	"testing"

	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

// legacyFallbackItem 造一条早期误落成钓线类的存量物品。
func legacyFallbackItem(t *testing.T, guid string, def *store.ItemDef, qty int64) *store.Item {
	t.Helper()
	obj, err := wiregen.MakeObj(fallbackWireClass, nil)
	if err != nil {
		t.Fatal(err)
	}
	fields := obj.Fields
	fields[fieldIdxByCS(fallbackWireClass, csGUID)].Hex = guid
	fields[fieldIdxByCS(fallbackWireClass, csNumericID)].Num = ptrU64(uint64(def.NumericID))
	fields[fieldIdxByCS(fallbackWireClass, csQty)].Num = ptrU64(uint64(qty))
	fj, err := FieldsToJSON(fields)
	if err != nil {
		t.Fatal(err)
	}
	return &store.Item{
		GUID: guid, UID: 1000004, ItemID: def.ItemID, NumericID: def.NumericID,
		WireClass: fallbackWireClass, TypeID: fallbackTypeID,
		Container: "backpack", Qty: qty, FieldsJSON: fj, Durability: 1, Workmanship: 1,
	}
}

// TestReclassifyDipFromFallback 存量提取物启动修复：换到 abmokghdbhf/11500，
// 数量写进该族的两个数量槽（f13 dnmfciolgep + f0 miecmmbpolk），状态位保留。
func TestReclassifyDipFromFallback(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()

	def, err := db.GetItemDef(ctx, 5136) // dip_hvz_se 大草莓提取物
	if err != nil || def == nil {
		t.Skip("静态库缺 dip_hvz_se 定义: ", err)
	}
	if def.Category != 58 {
		t.Fatalf("dip_hvz_se 品类 = %d，期望 58", def.Category)
	}

	guid := "00112233445566778899aabbccddee01"
	legacy := legacyFallbackItem(t, guid, def, 100)
	if err := db.InsertItem(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetItemByGUID(ctx, guid)
	if err != nil || row == nil {
		t.Fatal("读取存量物品失败: ", err)
	}

	changed, err := srv.reclassifyStoredItem(ctx, row, def)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("提取物未被改写")
	}
	fresh, err := db.GetItemByGUID(ctx, guid)
	if err != nil || fresh == nil {
		t.Fatal("改写后读取失败: ", err)
	}
	if fresh.WireClass != "abmokghdbhf" || fresh.TypeID != 11500 {
		t.Fatalf("改写后 = %s/%d，期望 abmokghdbhf/11500", fresh.WireClass, fresh.TypeID)
	}
	if fresh.Qty != 100 {
		t.Fatalf("行数量 = %d，期望 100", fresh.Qty)
	}
	fields, err := FieldsFromJSON(fresh.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != len(wiregen.ClassOps["abmokghdbhf"]) {
		t.Fatalf("字段数 = %d，期望 %d", len(fields), len(wiregen.ClassOps["abmokghdbhf"]))
	}
	if got := u64Of(fields[fieldIdxByCS("abmokghdbhf", "dnmfciolgep")].Num); got != 100 {
		t.Errorf("f13 dnmfciolgep = %d，期望 100", got)
	}
	if got := u64Of(fields[fieldIdxByCS("abmokghdbhf", "miecmmbpolk")].Num); got != 100 {
		t.Errorf("f0 miecmmbpolk（UI 计数）= %d，期望 100", got)
	}
	if got := u64Of(fields[fieldIdxByCS("abmokghdbhf", csNumericID)].Num); got != 5136 {
		t.Errorf("numeric id = %d，期望 5136", got)
	}
	if fields[fieldIdxByCS("abmokghdbhf", csGUID)].Hex != guid {
		t.Errorf("guid 未保留: %s", fields[fieldIdxByCS("abmokghdbhf", csGUID)].Hex)
	}
	if bits, ok := defAttrU64(def, csStateEnum); ok {
		if got := u64Of(fields[fieldIdxByCS("abmokghdbhf", csStateEnum)].Num); got != bits {
			t.Errorf("状态位 = %d，期望定义值 %d", got, bits)
		}
	}
}

// TestReclassifyRodPodFromFallback 存量竿架（79 有映射但迁移集此前没包含它）。
func TestReclassifyRodPodFromFallback(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()

	def, err := db.GetItemDefByItemID(ctx, "rod_pod_03r")
	if err != nil || def == nil {
		t.Skip("静态库缺 rod_pod_03r 定义: ", err)
	}
	if def.Category != 79 {
		t.Fatalf("rod_pod_03r 品类 = %d，期望 79", def.Category)
	}

	guid := "00112233445566778899aabbccddee02"
	legacy := legacyFallbackItem(t, guid, def, 1)
	if err := db.InsertItem(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetItemByGUID(ctx, guid)
	if err != nil || row == nil {
		t.Fatal("读取存量物品失败: ", err)
	}
	changed, err := srv.reclassifyStoredItem(ctx, row, def)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("竿架未被改写")
	}
	fresh, err := db.GetItemByGUID(ctx, guid)
	if err != nil || fresh == nil {
		t.Fatal("改写后读取失败: ", err)
	}
	if fresh.WireClass != "abmokghdbhf" || fresh.TypeID != 11500 {
		t.Fatalf("改写后 = %s/%d，期望 abmokghdbhf/11500", fresh.WireClass, fresh.TypeID)
	}
	// 竿架不可堆叠：数量槽保持零值，不得误写。
	fields, err := FieldsFromJSON(fresh.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if got := u64Of(fields[fieldIdxByCS("abmokghdbhf", "dnmfciolgep")].Num); got != 0 {
		t.Errorf("不可堆叠竿架 f13 应为 0，实得 %d", got)
	}
}

// TestPrepareNewDipUsesFamilyClass 新购提取物直接落在正确的实例类上，
// 数量 = 定义份数（100）× 购买单位。
func TestPrepareNewDipUsesFamilyClass(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()

	if def, err := db.GetItemDefByItemID(ctx, "dip_hvz_se"); err != nil || def == nil {
		t.Skip("静态库缺 dip_hvz_se 定义: ", err)
	}
	it, err := srv.prepareNewItem(ctx, 1000004, "dip_hvz_se", "backpack", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if it.WireClass != "abmokghdbhf" || it.TypeID != 11500 {
		t.Fatalf("新购实例 = %s/%d，期望 abmokghdbhf/11500", it.WireClass, it.TypeID)
	}
	if it.Qty != 100 {
		t.Fatalf("新购数量 = %d，期望定义份数 100", it.Qty)
	}
	fields, err := FieldsFromJSON(it.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if got := u64Of(fields[fieldIdxByCS("abmokghdbhf", "dnmfciolgep")].Num); got != 100 {
		t.Errorf("f13 dnmfciolgep = %d，期望 100", got)
	}
	if got := u64Of(fields[fieldIdxByCS("abmokghdbhf", "miecmmbpolk")].Num); got != 100 {
		t.Errorf("f0 miecmmbpolk = %d，期望 100", got)
	}
}

func assertDipQtyFields(t *testing.T, it *store.Item, want int64) {
	t.Helper()
	if it == nil {
		t.Fatal("物品为空")
	}
	fields, err := FieldsFromJSON(it.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	f13 := u64Of(fields[fieldIdxByCS(it.WireClass, "dnmfciolgep")].Num)
	f0 := u64Of(fields[fieldIdxByCS(it.WireClass, "miecmmbpolk")].Num)
	if it.Qty != want || f13 != uint64(want) || f0 != uint64(want) {
		t.Fatalf("qty=%d f13=%d f0=%d, want %d", it.Qty, f13, f0, want)
	}
}

func TestDipFactoryAndStackKeepQuantityFields(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	it, err := srv.CreateItem(ctx, 1000004, "dip_hvz_se", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	assertDipQtyFields(t, it, 100)

	stacked, merged, err := srv.AddToStack(ctx, 1000004, "dip_hvz_se", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !merged || stacked.GUID != it.GUID {
		t.Fatalf("应并入原堆，merged=%v got=%s want=%s", merged, stacked.GUID, it.GUID)
	}
	fresh, err := db.GetItemByGUID(ctx, it.GUID)
	if err != nil {
		t.Fatal(err)
	}
	assertDipQtyFields(t, fresh, 200)
}

func TestDipExactCountKeepsQuantityFields(t *testing.T) {
	srv, _ := newRigTestServer(t)
	it, err := srv.prepareNewItemWithCount(context.Background(), 1000004, "dip_hvz_se", "backpack", 7, "")
	if err != nil {
		t.Fatal(err)
	}
	assertDipQtyFields(t, it, 7)
}
