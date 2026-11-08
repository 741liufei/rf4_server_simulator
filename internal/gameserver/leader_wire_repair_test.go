package gameserver

import (
	"context"
	"testing"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

func TestLeaderWireDefForLineMapping(t *testing.T) {
	t.Parallel()
	srv, db := newLeaderTestServer(t)
	ctx := context.Background()

	cases := map[string]string{
		"mono_15245": "wrl_sim_monorig_4LB",
		"mono_15247": "wrl_sim_monorig_7.5LB",
		"mono_15249": "wrl_sim_monorig_15LB",
	}
	for source, want := range cases {
		lineDef, err := db.GetItemDefByItemID(ctx, source)
		if err != nil {
			t.Fatal(err)
		}
		got, err := srv.leaderWireDefForLine(ctx, lineDef)
		if err != nil {
			t.Fatalf("%s 映射失败: %v", source, err)
		}
		if got.ItemID != want {
			t.Fatalf("%s 映射=%s，期望 %s", source, got.ItemID, want)
		}
	}

	// mono_15065 在目录里没有四项参数完全一致的孪生前导线，
	// 但玩家拿它做引线也必须成功：取拉力与线径最接近的前导线定义。
	nearest, err := db.GetItemDefByItemID(ctx, "mono_15065")
	if err != nil {
		t.Fatal(err)
	}
	got, err := srv.leaderWireDefForLine(ctx, nearest)
	if err != nil {
		t.Fatalf("mono_15065 必须映射到最接近的前导线: %v", err)
	}
	if got.ItemID != "wrl_sim_fluoro7lb" {
		t.Fatalf("mono_15065 映射=%s，期望最接近的 wrl_sim_fluoro7lb", got.ItemID)
	}
	if got.Category != gearCatLeaderWire {
		t.Fatalf("映射结果 %s 品类=%d，期望前导线 %d", got.ItemID, got.Category, gearCatLeaderWire)
	}
	if want, have := itemDefF32(nearest, gearAttrMaxLoad), itemDefF32(got, gearAttrMaxLoad); have != want {
		t.Fatalf("最近候选 %s 拉力=%f，应贴近真实线材 %f", got.ItemID, have, want)
	}

	// 非线材不能被当成内线，鱼竿仍然要拒绝。
	rod, err := db.GetItemDefByItemID(ctx, "feeder_10125_FD360")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.leaderWireDefForLine(ctx, rod); err == nil {
		t.Fatal("非线材必须拒绝映射")
	}
}

// TestRepairCraftedLeaderWireMapsNearestAndRestoresQuarantine 验证自制引线的
// 三条修复路径：有精确孪生按精确孪生；没有孪生按最接近的前导线；只有在隔离区
// 里且根本没有内嵌线材的成品才继续留在隔离区。
func TestRepairCraftedLeaderWireMapsNearestAndRestoresQuarantine(t *testing.T) {
	t.Parallel()
	srv, db := newLeaderTestServer(t)
	ctx := context.Background()
	const uid = int64(2000042)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}

	repairable := makeLegacyCraftedLeader(t, srv, db, uid, "mono_15247")
	nearestCase := makeLegacyCraftedLeader(t, srv, db, uid, "mono_15065")
	restorable := makeLegacyCraftedLeader(t, srv, db, uid, "mono_15065")
	if err := db.UpdateItemContainer(ctx, restorable, craftedLeaderQuarantineContainer); err != nil {
		t.Fatal(err)
	}
	emptyLeader := makeCraftedLeaderWithoutWire(t, srv, db, uid)

	if err := srv.RepairItemPhysics(ctx); err != nil {
		t.Fatal(err)
	}

	assertRepairedInnerWire(t, db, repairable, 16064, 3.4)
	// 旧版本会因为"找不到参数一致的前导线"把这件引线关进隔离区；
	// 现在按最接近的 wrl_sim_fluoro7lb 修复，强度仍按玩家那根 3.1kg 主线。
	assertRepairedInnerWire(t, db, nearestCase, 16057, 3.1)

	restored, err := db.GetItemByGUID(ctx, restorable)
	if err != nil || restored == nil {
		t.Fatal(err)
	}
	if restored.Container != "backpack" {
		t.Fatalf("隔离区里的引线解除隔离后容器=%s，期望 backpack", restored.Container)
	}
	others, err := db.ListItemsByContainers(ctx, uid, []string{"backpack"})
	if err != nil {
		t.Fatal(err)
	}
	// 解除隔离后要占用背包里一个空槽，不能和已有物品叠在同一格。
	taken := map[int64]bool{}
	for _, row := range others {
		if row.GUID != restored.GUID {
			taken[row.Slot] = true
		}
	}
	if taken[restored.Slot] {
		t.Fatalf("解除隔离的引线背包槽位 %d 与已有物品重叠", restored.Slot)
	}

	hidden, err := db.GetItemByGUID(ctx, emptyLeader)
	if err != nil || hidden == nil {
		t.Fatal(err)
	}
	if hidden.Container != craftedLeaderQuarantineContainer {
		t.Fatalf("没有内嵌线材的引线容器=%s，期望 %s", hidden.Container, craftedLeaderQuarantineContainer)
	}
}

// assertRepairedInnerWire 校验修复后的自制引线使用了指定前导线模型，
// 并把强度写回真实线材的公斤数。
func assertRepairedInnerWire(t *testing.T, db *store.DB, guid string, wantNumericID uint64, wantStrength float64) {
	t.Helper()
	ctx := context.Background()
	fixed, err := db.GetItemByGUID(ctx, guid)
	if err != nil || fixed == nil {
		t.Fatal(err)
	}
	if fixed.Container != "backpack" {
		t.Fatalf("可修复引线容器=%s，期望 backpack", fixed.Container)
	}
	fields, err := FieldsFromJSON(fixed.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	line := fields[fieldIdxByCS(fixed.WireClass, gearAttrLeaderIn)].Val
	if line == nil || line.Kind != proto.KindObj {
		t.Fatalf("修复后缺少内嵌线材: %+v", line)
	}
	if got := u64Of(line.Fields[fieldIdxByCS(line.Class, csNumericID)].Num); got != wantNumericID {
		t.Fatalf("修复后内线 numeric_id=%d，期望 %d", got, wantNumericID)
	}
	if got := wireFloatForTest(t, line, csStrength); got < wantStrength-0.01 || got > wantStrength+0.01 {
		t.Fatalf("修复后内线强度=%f，期望 %fkg", got, wantStrength)
	}
	lineDef, err := db.GetItemDef(ctx, int64(wantNumericID))
	if err != nil || lineDef == nil {
		t.Fatalf("修复后的内线定义不存在: %d", wantNumericID)
	}
	if lineDef.Category != gearCatLeaderWire {
		t.Fatalf("修复后的内线 %s 品类=%d，期望前导线 %d", lineDef.ItemID, lineDef.Category, gearCatLeaderWire)
	}
}

// makeCraftedLeaderWithoutWire 造一件没有任何内嵌线材的自制引线成品，
// 对应旧版本扣料成功但物化失败留下的空壳。
func makeCraftedLeaderWithoutWire(t *testing.T, srv *Server, db *store.DB, uid int64) string {
	t.Helper()
	ctx := context.Background()
	row, err := srv.prepareNewItem(ctx, uid, "leader_crafted_carp_50", "backpack", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertItem(ctx, row); err != nil {
		t.Fatal(err)
	}
	return row.GUID
}

func newLeaderTestServer(t *testing.T) (*Server, *store.DB) {
	t.Helper()
	db := openItemTestDB(t)
	return newSkillsCraftServer(t, db), db
}

func makeLegacyCraftedLeader(t *testing.T, srv *Server, db *store.DB, uid int64, lineItemID string) string {
	t.Helper()
	ctx := context.Background()
	row, err := srv.prepareNewItem(ctx, uid, "leader_crafted_carp_50", "backpack", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	lineDef, err := db.GetItemDefByItemID(ctx, lineItemID)
	if err != nil {
		t.Fatal(err)
	}
	material := craftedMaterial{
		row: &store.Item{GUID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Workmanship: 1, Durability: 1},
		def: lineDef,
	}
	line, err := srv.craftedMaterialObject(row, material, gearAttrLeaderIn, 50)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	fields[fieldIdxByCS(row.WireClass, gearAttrLeaderIn)].Val = line
	setWireFloat(fields, row.WireClass, fieldIdxByCS(row.WireClass, csStrength),
		itemDefF32(lineDef, gearAttrMaxLoad))
	row.FieldsJSON, err = FieldsToJSON(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertItem(ctx, row); err != nil {
		t.Fatal(err)
	}
	return row.GUID
}
