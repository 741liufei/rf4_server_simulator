package gameserver

import (
	"context"
	"math"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// TestCraftedLeaderEntersStrengthChain 锁住自制引线的承力口径。
//
// 自制引线（leader_crafted_*）的定义里 mbnambppbom（内嵌线材）和 lbadjgllglo
// （连接件）都是 null：材料与连接件是玩家制作时才选的，只有物品实例上才有。
// 修复前服务端读定义得到 0，整根引线不进最弱环：客户端按引线 3.4kg 判该断，
// 服务端却把断件判到主线/鱼竿/渔轮上，玩家看到的"该断的没断、不该爆的爆了"
// 就是这么来的；下发给客户端的强度也停在制作时的旧值，磨损完全不体现。
func TestCraftedLeaderEntersStrengthChain(t *testing.T) {
	db := openRecipeTestDB(t)
	ctx := context.Background()
	srv := newSkillsCraftServer(t, db)
	uid := int64(2000066)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}

	// 真实制作一根自制引线：线材 3.4kg、连接件 6.75kg，成品取最弱的 3.4kg。
	lineMat, err := srv.CreateItem(ctx, uid, "mono_15247", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	srv.alignItemRowQty(ctx, lineMat)
	connMat, err := srv.CreateItem(ctx, uid, "swlock_40009", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	srv.alignItemRowQty(ctx, connMat)
	// 成品承力应由“材料额定值 × 材料耐久 × 材料工艺”决定，不能只看定义额定值。
	// 3.4kg × 0.5 × 0.8 = 1.36kg；连接件 6.75kg × 0.6 × 0.9 不是短板。
	if err := db.SetItemDurability(ctx, lineMat.GUID, 0.5); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.ExecContext(ctx, `UPDATE item SET workmanship=? WHERE guid=?`, 0.8, lineMat.GUID); err != nil {
		t.Fatal(err)
	}
	if err := db.SetItemDurability(ctx, connMat.GUID, 0.6); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.ExecContext(ctx, `UPDATE item SET workmanship=? WHERE guid=?`, 0.9, connMat.GUID); err != nil {
		t.Fatal(err)
	}
	sess := &Session{ID: 1, Userid: uid, Srv: srv, log: logging.Discard()}
	hctx := &Ctx{
		Sess: sess, Main: 9, Sub: 1, CallID: 1, DecodeOK: true,
		Tree: proto.Args524(craftObj8("craft_leader_mar_40", []craftSlot{
			{ItemID: "craft_mat_line", GUID: "00112233445566778899aabbccddeeff"},
			{ItemID: "craft_mat_swivels", GUID: "ffeeddccbbaa99887766554433221100"},
		}, 1)),
		Log: logging.Discard(),
	}
	if _, ok := hCraft(hctx); !ok {
		t.Fatal("9/1 制作引线未处理")
	}
	rows, err := db.ListItemsByContainers(ctx, uid, []string{"backpack", "rig"})
	if err != nil {
		t.Fatal(err)
	}
	var crafted *store.Item
	for _, row := range rows {
		if row.ItemID == "leader_crafted_mar_40" {
			crafted = row
			break
		}
	}
	if crafted == nil {
		t.Fatal("未找到成品引线")
	}
	const leaderKg = 1.36
	craftedFields, err := FieldsFromJSON(crafted.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	craftedObj := &proto.Value{Kind: proto.KindObj, Class: crafted.WireClass, Fields: craftedFields}
	if got, ok := wireFloatAt(craftedObj, csStrength); !ok || math.Abs(got-leaderKg) > 1e-3 {
		t.Fatalf("制作快照强度=%f，期望 %f", got, leaderKg)
	}
	mainLine := craftedFields[fieldIdxByCS(crafted.WireClass, craftedMainLineKey)].Val
	if mainLine == nil || mainLine.Kind != proto.KindObj {
		t.Fatalf("普通主线材料应写入 f3: %+v", mainLine)
	}
	if got, ok := wireFloatAt(mainLine, csStrength); !ok || math.Abs(got-leaderKg) > 1e-3 {
		t.Fatalf("内嵌主线强度=%f，期望 %f", got, leaderKg)
	}
	if got := srv.craftedLeaderBaseStrengthKg(ctx, crafted.GUID); math.Abs(got-leaderKg) > 1e-3 {
		t.Fatalf("引线实例基准强度=%f，期望 %f", got, leaderKg)
	}

	// 装上竿/轮/主线，让这根引线进入钓组承力链。
	insertDef := func(numeric int64, itemID string, cat int64, attrs string) {
		t.Helper()
		if _, err := db.SQL.Exec(`INSERT OR REPLACE INTO item_def(numeric_id,item_id,category,kind,brand,attrs_json)
			VALUES(?,?,?,'test','',?)`, numeric, itemID, cat, attrs); err != nil {
			t.Fatal(err)
		}
	}
	insertDef(9101, "rod_leader_load", gearCatRod, attrsJSON(t, map[string]any{
		gearAttrMaxLoad: f32Attr(14.0102), gearAttrLength: f32Attr(2.1),
	}))
	insertDef(9102, "reel_leader_load", gearCatReel, attrsJSON(t, map[string]any{
		gearAttrDrag: map[string]any{gearAttrDragMax: f32Attr(6.4)},
	}))
	insertDef(9103, "line_leader_load", gearCatLine, attrsJSON(t, map[string]any{
		gearAttrMaxLoad: f32Attr(10.5), gearAttrStretch: f32Attr(0.85),
	}))
	const rodGUID = "51111111111111111111111111111111"
	const reelGUID = "52222222222222222222222222222222"
	const mainLineGUID = "53333333333333333333333333333333"
	for _, spec := range []struct {
		guid    string
		numeric int64
		itemID  string
	}{
		{rodGUID, 9101, "rod_leader_load"},
		{reelGUID, 9102, "reel_leader_load"},
		{mainLineGUID, 9103, "line_leader_load"},
	} {
		row := &store.Item{GUID: spec.guid, UID: uid, ItemID: spec.itemID, NumericID: spec.numeric,
			WireClass: "cnolibojkao", TypeID: 157, Container: "rig", FieldsJSON: "[]",
			Durability: 1, Workmanship: 1}
		if err := db.InsertItem(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	for slot, guid := range map[int64]string{
		gearCatReel: reelGUID, gearCatLine: mainLineGUID, gearCatLeader: crafted.GUID,
	} {
		if err := db.UpsertRigComponent(ctx, rodGUID, slot, guid, 0); err != nil {
			t.Fatal(err)
		}
	}

	ids := srv.rigGearIDs(ctx, uid, rodGUID)
	if ids.Leader != crafted.ItemID || ids.LeaderGUID != crafted.GUID {
		t.Fatalf("引线未被装配识别: %+v", ids)
	}
	phys := srv.loadGearSpecForRig(ctx, ids, rodGUID)
	if math.Abs(phys.LeaderStrengthKg-leaderKg) > 1e-3 {
		t.Fatalf("引线承力=%f，期望按实例材料取 %fg", phys.LeaderStrengthKg, leaderKg)
	}
	if got := gearWeakestPart(phys); got != "leader" {
		t.Fatalf("最弱环=%q，期望 leader（修复前引线强度为 0，会被跳过）", got)
	}
	if got := gearBreakLimitKg(phys); math.Abs(got-leaderKg) > 1e-3 {
		t.Fatalf("承力链上限=%f，期望引线 %fg", got, leaderKg)
	}

	// 客户端没点名断件（拟饵出厂钩这类内嵌件）时走最弱环回退：必须断引线，
	// 不能再改成断主线。
	dec, err := srv.resolveGearBreak(ctx, uid, rodGUID, "")
	if err != nil {
		t.Fatal(err)
	}
	if dec.Part != "leader" || dec.GUID != crafted.GUID {
		t.Fatalf("断件裁决=%+v，期望断这根自制引线 %s", dec, crafted.GUID)
	}

	// 磨损必须同时体现在服务端最弱环和下发给客户端的强度字段上。
	if err := db.SetItemDurability(ctx, crafted.GUID, 0.5); err != nil {
		t.Fatal(err)
	}
	half, err := db.GetItemByGUID(ctx, crafted.GUID)
	if err != nil || half == nil {
		t.Fatal(err)
	}
	phys = srv.loadGearSpecForRig(ctx, srv.rigGearIDs(ctx, uid, rodGUID), rodGUID)
	if math.Abs(phys.LeaderStrengthKg-leaderKg*0.5) > 1e-3 {
		t.Fatalf("半耐久引线承力=%f，期望 %f", phys.LeaderStrengthKg, leaderKg*0.5)
	}
	pushed, err := srv.ItemToTree(ctx, half)
	if err != nil {
		t.Fatal(err)
	}
	if got := wireFloatForTest(t, pushed, csStrength); math.Abs(got-leaderKg*0.5) > 1e-3 {
		t.Fatalf("下发的引线强度=%f，期望随耐久衰减到 %f", got, leaderKg*0.5)
	}
	// 材料对象自身的强度也按母件耐久结算，两端不能各算一套。
	if got := wireFloatForTest(t, pushed, csDurability); math.Abs(got-0.5) > 1e-6 {
		t.Fatalf("下发的引线耐久=%f，期望 0.5", got)
	}
}

// TestCraftedLeaderBaseStrengthUsesNestedPhysicalStrength 覆盖旧版普通主线
// 映射到前导线模型的存档：numeric_id 指向模型定义，真实材料仍由嵌套对象
// 的 csStrength 保存。承力必须读该字段，不能回查模型定义的额定值。
func TestCraftedLeaderBaseStrengthUsesNestedPhysicalStrength(t *testing.T) {
	db := openRecipeTestDB(t)
	ctx := context.Background()
	srv := newSkillsCraftServer(t, db)
	uid := int64(2000067)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	row, err := srv.prepareNewItem(ctx, uid, "leader_crafted_mar_40", "backpack", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	lineDef, err := db.GetItemDefByItemID(ctx, "mono_15247")
	if err != nil || lineDef == nil {
		t.Fatal(err)
	}
	mappedDef, err := srv.leaderWireDefForLine(ctx, lineDef)
	if err != nil {
		t.Fatal(err)
	}
	material := craftedMaterial{
		row:      &store.Item{GUID: uuidLike(row.GUID, "legacy-line"), Workmanship: 0.8, Durability: 0.5},
		def:      mappedDef,
		physical: lineDef,
	}
	line, err := srv.craftedMaterialObject(row, material, gearAttrLeaderIn, 100)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	fields[fieldIdxByCS(row.WireClass, gearAttrLeaderIn)].Val = line
	row.FieldsJSON, err = FieldsToJSON(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertItem(ctx, row); err != nil {
		t.Fatal(err)
	}

	const wantKg = 1.36 // 3.4kg × 0.5 耐久 × 0.8 工艺
	if got := itemDefF32(mappedDef, gearAttrMaxLoad); math.Abs(got-wantKg) <= 1e-3 {
		t.Fatalf("测试前提失败：映射模型额定值 %f 无法区分嵌套真实强度", got)
	}
	if got := srv.craftedLeaderBaseStrengthKg(ctx, row.GUID); math.Abs(got-wantKg) > 1e-3 {
		t.Fatalf("映射模型的引线承力=%f，期望读取嵌套真实强度 %f", got, wantKg)
	}
}
