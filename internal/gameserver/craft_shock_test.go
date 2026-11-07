package gameserver

import (
	"context"
	"math"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
)

func TestCraftShockLeaderUsesMainLineSlot(t *testing.T) {
	db := openRecipeTestDB(t)
	srv := newSkillsCraftServer(t, db)
	ctx := context.Background()
	const uid = int64(2000043)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_15065", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	srv.alignItemRowQty(ctx, line)
	sess := &Session{ID: 1, Userid: uid, Srv: srv, log: logging.Discard()}
	hctx := &Ctx{Sess: sess, Main: 9, Sub: 1, CallID: 1, DecodeOK: true,
		Tree: proto.Args524(craftObj8("craft_leader_shock_200", []craftSlot{{ItemID: "craft_mat_line", GUID: line.GUID}}, 1)), Log: logging.Discard()}
	body, ok := hCraft(hctx)
	if !ok || u64Of(decodeTreeValue(t, srv, body).Fields[0].Num) != 1 {
		t.Fatal("Super Line 7lb 制作减震引线应成功")
	}
	rows, err := db.ListItemsByContainers(ctx, uid, []string{"backpack"})
	if err != nil {
		t.Fatal(err)
	}
	var crafted *store.Item
	for _, row := range rows {
		if row.ItemID == "leader_crafted_shock_200" {
			crafted = row
		}
	}
	if crafted == nil {
		t.Fatal("缺少减震引线成品")
	}
	check := func() {
		t.Helper()
		row, err := db.GetItemByGUID(ctx, crafted.GUID)
		if err != nil || row == nil || row.Container != "backpack" {
			t.Fatalf("成品应保留在背包: %+v, %v", row, err)
		}
		tree, err := srv.ItemToTree(ctx, row)
		if err != nil {
			t.Fatal(err)
		}
		inner := tree.Fields[fieldIdxByCS(tree.Class, craftedMainLineKey)].Val
		if inner == nil || inner.Kind != proto.KindObj || inner.Class != categoryWireClass[gearCatLine].Class {
			t.Fatalf("f3 应使用普通主线实例: %+v", inner)
		}
		if got := u64Of(inner.Fields[fieldIdxByCS(inner.Class, csNumericID)].Num); got != 9201 {
			t.Fatalf("主线模型=%d，期望 9201", got)
		}
		if got := u64Of(inner.Fields[fieldIdxByCS(inner.Class, gearAttrLineLen)].Num); got != 200 {
			t.Fatalf("主线长度=%d，期望 200cm", got)
		}
		if got := wireFloatForTest(t, tree, csStrength); math.Abs(got-3.1) > 0.001 {
			t.Fatalf("成品强度=%f，期望 3.1kg", got)
		}
		for _, key := range []string{gearAttrLeaderIn, "lbadjgllglo"} {
			v := tree.Fields[fieldIdxByCS(tree.Class, key)].Val
			if v != nil && v.Kind != proto.KindNull {
				t.Fatalf("减震引线 %s 应为空: %+v", key, v)
			}
		}
	}
	check()
	if err := srv.RepairItemPhysics(ctx); err != nil {
		t.Fatal(err)
	}
	check()
	after, err := db.GetItemByGUID(ctx, line.GUID)
	if err != nil || after == nil || after.Qty != 118 {
		t.Fatalf("应只消耗 2m 主线: %+v, %v", after, err)
	}
}

func TestRepairLegacyShockLeaderMigratesF1ToMainLineSlot(t *testing.T) {
	db := openRecipeTestDB(t)
	srv := newSkillsCraftServer(t, db)
	ctx := context.Background()
	const uid = int64(2000053)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name        string
		lineItemID  string
		sourceKey   string
		workmanship float64
		durability  float64
	}{
		{name: "旧前导线对象", lineItemID: "wrl_sim_monorig_7.5LB", sourceKey: gearAttrLeaderIn, workmanship: 0.9, durability: 0.95},
		{name: "旧普通主线对象", lineItemID: "mono_15247", sourceKey: craftedMainLineKey, workmanship: 0.8, durability: 0.9},
	}
	fixtures := make([]string, len(cases))
	for i, tc := range cases {
		fixtures[i] = makeLegacyShockLeaderWithF1Line(t, srv, db, uid, tc.lineItemID, tc.sourceKey, tc.workmanship, tc.durability)
	}

	if err := srv.RepairItemPhysics(ctx); err != nil {
		t.Fatal(err)
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row, err := db.GetItemByGUID(ctx, fixtures[i])
			if err != nil || row == nil {
				t.Fatalf("读取旧减震引线失败: %v", err)
			}
			fields, err := FieldsFromJSON(row.FieldsJSON)
			if err != nil {
				t.Fatal(err)
			}
			mainLine := fields[fieldIdxByCS(row.WireClass, craftedMainLineKey)].Val
			if mainLine == nil || mainLine.Kind != proto.KindObj || mainLine.Class != categoryWireClass[gearCatLine].Class {
				t.Fatalf("旧减震引线未迁移到 f3 普通主线对象: %+v", mainLine)
			}
			lineDef, err := db.GetItemDefByItemID(ctx, tc.lineItemID)
			if err != nil || lineDef == nil {
				t.Fatalf("读取材料定义失败: %v", err)
			}
			if got := u64Of(mainLine.Fields[fieldIdxByCS(mainLine.Class, csNumericID)].Num); got != uint64(lineDef.NumericID) {
				t.Fatalf("迁移后 numeric_id=%d，期望保留 %d", got, lineDef.NumericID)
			}
			if got := u64Of(mainLine.Fields[fieldIdxByCS(mainLine.Class, gearAttrLineLen)].Num); got != 200 {
				t.Fatalf("迁移后长度=%d，期望 200cm", got)
			}
			if got := wireFloatForTest(t, mainLine, csWorkmanship); math.Abs(got-tc.workmanship) > 0.001 {
				t.Fatalf("迁移后工艺=%f，期望 %f", got, tc.workmanship)
			}
			if got := wireFloatForTest(t, mainLine, csDurability); math.Abs(got-tc.durability) > 0.001 {
				t.Fatalf("迁移后耐久=%f，期望 %f", got, tc.durability)
			}
			wantStrength := itemDefF32(lineDef, gearAttrMaxLoad) * tc.workmanship * tc.durability
			if got := wireFloatForTest(t, mainLine, csStrength); math.Abs(got-wantStrength) > 0.001 {
				t.Fatalf("迁移后内线强度=%f，期望 %f", got, wantStrength)
			}
			wantGUID := uuidLike(row.GUID, "legacy-line")
			if got := mainLine.Fields[fieldIdxByCS(mainLine.Class, csGUID)].Hex; got != wantGUID {
				t.Fatalf("迁移后内线 GUID=%s，期望保留 %s", got, wantGUID)
			}
			if old := fields[fieldIdxByCS(row.WireClass, gearAttrLeaderIn)].Val; old != nil && old.Kind != proto.KindNull {
				t.Fatalf("迁移后 f1 应清空: %+v", old)
			}
		})
	}
}

func makeLegacyShockLeaderWithF1Line(
	t *testing.T,
	srv *Server,
	db *store.DB,
	uid int64,
	lineItemID string,
	sourceKey string,
	workmanship float64,
	durability float64,
) string {
	t.Helper()
	ctx := context.Background()
	row, err := srv.prepareNewItem(ctx, uid, "leader_crafted_shock_200", "backpack", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	lineDef, err := db.GetItemDefByItemID(ctx, lineItemID)
	if err != nil {
		t.Fatal(err)
	}
	material := craftedMaterial{
		row: &store.Item{GUID: uuidLike(row.GUID, "legacy-line"), Workmanship: workmanship, Durability: durability},
		def: lineDef,
	}
	line, err := srv.craftedMaterialObject(row, material, sourceKey, 200)
	if err != nil {
		t.Fatal(err)
	}
	if idx := fieldIdxByCS(line.Class, csGUID); idx >= 0 {
		// craftedMaterialObject 会按槽位派生嵌套 GUID；这里固定成旧对象自己的
		// GUID，确保迁移测试验证的是保留原对象身份，而不是重新生成一个。
		line.Fields[idx].Hex = material.row.GUID
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	fields[fieldIdxByCS(row.WireClass, gearAttrLeaderIn)].Val = line
	setWireFloat(fields, row.WireClass, fieldIdxByCS(row.WireClass, csStrength),
		itemDefF32(lineDef, gearAttrMaxLoad)*workmanship*durability)
	row.FieldsJSON, err = FieldsToJSON(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertItem(ctx, row); err != nil {
		t.Fatal(err)
	}
	return row.GUID
}
