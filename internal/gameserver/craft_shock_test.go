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
