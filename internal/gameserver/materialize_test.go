package gameserver

import (
	"context"
	"math"
	"testing"

	"rf4server/internal/proto"
)

// TestReelMaterializesKnobs 验证渔轮 f5 摇柄子实例存在，并且其字段可被客户端使用。
func TestReelMaterializesKnobs(t *testing.T) {
	srv, db := newDetachTestServer(t)
	ctx := context.Background()

	reel, err := srv.CreateItem(ctx, 1000001, "spin_5184", "backpack", 1)
	if err != nil {
		t.Fatalf("创建 spin_5184 失败: %v", err)
	}
	stored, err := FieldsFromJSON(reel.FieldsJSON)
	if err != nil {
		t.Fatalf("解析新建渔轮字段失败: %v", err)
	}
	storedDrag := stored[fieldIdxByCS(reel.WireClass, "mlfjkaoicdn")].Val
	if storedDrag == nil || storedDrag.Class != "loedbndnill" {
		t.Fatal("新建渔轮落库时 f3 刹车子实例缺失")
	}
	storedForceIdx := fieldIdxByCS(storedDrag.Class, csDragForce)
	if got := float64(proto.F32FromBits(uint32(u64Of(storedDrag.Field(storedForceIdx).Num)))); got <= 0 {
		t.Fatalf("新建渔轮落库刹车力=%f，期望为正值", got)
	}

	tree, err := srv.ItemToTree(ctx, reel)
	if err != nil {
		t.Fatalf("重建 spin_5184 失败: %v", err)
	}
	if tree.Class != "dgjinoebdop" || tree.TypeID != 149 {
		t.Fatalf("渔轮 wire=%s/%d，期望 dgjinoebdop/149", tree.Class, tree.TypeID)
	}

	// spin_5184 的 f2/f3/f4/f5/f6/f10 应为真实子实例。
	for _, idx := range []int{2, 3, 4, 5, 6, 10} {
		f := tree.Field(idx)
		if f.Val == nil || f.Val.Kind != proto.KindObj {
			t.Fatalf("渔轮 f%d 子部件缺失: %#v", idx, f)
		}
	}

	knob := tree.Field(5).Val
	if knob.Class != "bndpebpblfk" || knob.TypeID != 148 {
		t.Fatalf("摇柄 wire=%s/%d，期望 bndpebpblfk/148", knob.Class, knob.TypeID)
	}
	nidIdx := fieldIdxByCS(knob.Class, csNumericID)
	stateIdx := fieldIdxByCS(knob.Class, csStateEnum)
	if nidIdx < 0 || stateIdx < 0 {
		t.Fatalf("摇柄字段映射缺失: nid=%d state=%d", nidIdx, stateIdx)
	}
	if got := u64Of(knob.Field(nidIdx).Num); got != 8558 {
		t.Fatalf("摇柄 numeric_id=%d，期望 8558", got)
	}
	if got := u64Of(knob.Field(stateIdx).Num); got != 17920 {
		t.Fatalf("摇柄状态=0x%X，期望 0x4600(定义 0x4200|挂载 0x400)", got)
	}

	// 刹车片子 f3/loedbndnill 的 f0 是客户端曳力物理值，必须为正值。
	drag := tree.Field(3).Val
	if drag == nil || drag.Class != "loedbndnill" {
		t.Fatalf("渔轮 f3 应为刹车片子 loedbndnill，实得 %#v", drag)
	}
	sidx := fieldIdxByCS(drag.Class, csDragForce)
	if sidx < 0 {
		t.Fatalf("刹车片缺少强度字段 %s", csDragForce)
	}
	gotDrag := float64(proto.F32FromBits(uint32(u64Of(drag.Field(sidx).Num))))
	def, derr := db.GetItemDef(ctx, reel.NumericID)
	if derr != nil || def == nil {
		t.Fatalf("渔轮定义缺失: %v", derr)
	}
	cfg, _ := defAttrs(def)["mlfjkaoicdn"].(map[string]any)
	bits, _ := cfg["omghfmdfjpc"].(float64)
	wantDrag := float64(proto.F32FromBits(uint32(bits))) * reel.Workmanship
	if gotDrag <= 0 || math.Abs(gotDrag-wantDrag) > 0.0001 {
		t.Fatalf("刹车片子强度=%f，期望 %f（0 会导致线杯无制动）", gotDrag, wantDrag)
	}
}

// TestRodFactoryPartsCarryCondition 验证竿坯、手柄和导环继承母竿的工艺与耐久，
// 且容器枚举为 rig(4)。
func TestRodFactoryPartsCarryCondition(t *testing.T) {
	srv, _ := newDetachTestServer(t)
	ctx := context.Background()
	rod, err := srv.CreateItem(ctx, 1000001, "feeder_10126_FD390", "backpack", 1)
	if err != nil {
		t.Fatalf("创建 FD390 失败: %v", err)
	}
	tree, err := srv.ItemToTree(ctx, rod)
	if err != nil {
		t.Fatalf("重建 FD390 失败: %v", err)
	}
	for idx := 0; idx < 3; idx++ {
		part := tree.Field(idx).Val
		if part == nil || part.Kind != proto.KindObj {
			t.Fatalf("FD390 f%d 出厂部件缺失", idx)
		}
		widx := fieldIdxByCS(part.Class, csWorkmanship)
		didx := fieldIdxByCS(part.Class, csDurability)
		sidx := fieldIdxByCS(part.Class, csSlotEnum)
		if widx < 0 || didx < 0 || sidx < 0 {
			t.Fatalf("部件 %s 基类字段缺失: work=%d dur=%d slot=%d", part.Class, widx, didx, sidx)
		}
		if got := float64(proto.F32FromBits(uint32(u64Of(part.Field(widx).Num)))); got < 0.9999 || got > 1.0001 {
			t.Fatalf("f%d 工艺=%f，期望 1.0", idx, got)
		}
		if got := float64(proto.F32FromBits(uint32(u64Of(part.Field(didx).Num)))); got < 0.9999 || got > 1.0001 {
			t.Fatalf("f%d 耐久=%f，期望 1.0（0 会显示损耗 100%%）", idx, got)
		}
		if got := u64Of(part.Field(sidx).Num); got != 4 {
			t.Fatalf("f%d 容器枚举=%d，期望 rig(4)", idx, got)
		}
	}
}

// TestForcePartsStrengthRendered 验证线、前导和钩的 U32 字段按 F32 位型承载强度。
func TestForcePartsStrengthRendered(t *testing.T) {
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	for _, itemID := range []string{"mono_15097", "sh_leader_40815", "clas_20163"} {
		it, err := srv.CreateItem(ctx, 1000001, itemID, "backpack", 1)
		if err != nil {
			t.Fatalf("创建 %s 失败: %v", itemID, err)
		}
		stored, err := FieldsFromJSON(it.FieldsJSON)
		if err != nil {
			t.Fatalf("解析新建 %s 字段失败: %v", itemID, err)
		}
		rawIdx := fieldIdxByCS(it.WireClass, csStrength)
		if rawIdx < 0 || fieldOpAt(it.WireClass, rawIdx) != "U32" {
			t.Fatalf("%s 新建落库强度位置错误: idx=%d op=%s", itemID, rawIdx, fieldOpAt(it.WireClass, rawIdx))
		}
		if raw := float64(proto.F32FromBits(uint32(u64Of(stored[rawIdx].Num)))); raw <= 0 {
			t.Fatalf("%s 新建落库强度=%f，创建路径没有直接写正确值", itemID, raw)
		}
		tree, err := srv.ItemToTree(ctx, it)
		if err != nil {
			t.Fatalf("重建 %s 失败: %v", itemID, err)
		}
		idx := fieldIdxByCS(tree.Class, csStrength)
		if idx < 0 || fieldOpAt(tree.Class, idx) != "U32" {
			t.Fatalf("%s 强度位置应为 U32，实际 idx=%d op=%s", itemID, idx, fieldOpAt(tree.Class, idx))
		}
		got := float64(proto.F32FromBits(uint32(u64Of(tree.Field(idx).Num))))
		def, derr := db.GetItemDef(ctx, it.NumericID)
		if derr != nil || def == nil {
			t.Fatalf("%s 定义缺失: %v", itemID, derr)
		}
		want, ok := strengthBitsFor(def, it.Durability)
		if !ok || want <= 0 {
			t.Fatalf("%s 定义强度无效: %f ok=%v", itemID, want, ok)
		}
		if got <= 0 || math.Abs(got-want) > 0.0001 {
			t.Fatalf("%s 渲染强度=%f，期望 %f（0 会导致线组物理异常）", itemID, got, want)
		}
	}
}

// TestMountedRootItemContainerEnum 验证根物品的 wire 容器枚举仍为 0；
// 只有母物品内部嵌套子实例才使用 4。
func TestMountedRootItemContainerEnum(t *testing.T) {
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	sess := newProbeSess(srv)
	rod, err := srv.CreateItem(ctx, uid, "feeder_10126_FD390", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_15097", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	runRodSet(t, srv, sess, rod.GUID, line.GUID, 0)

	check := func(want uint64) {
		t.Helper()
		row, err := db.GetItemByGUID(ctx, line.GUID)
		if err != nil || row == nil {
			t.Fatalf("重新读线失败: %v", err)
		}
		tree, err := srv.ItemToTree(ctx, row)
		if err != nil {
			t.Fatal(err)
		}
		idx := fieldIdxByCS(tree.Class, csSlotEnum)
		if idx < 0 {
			t.Fatalf("线类 %s 缺少容器枚举字段", tree.Class)
		}
		if got := u64Of(tree.Field(idx).Num); got != want {
			t.Fatalf("线容器枚举=%d，期望 %d", got, want)
		}
	}
	check(0) // 根物品挂在 rig 上，但 wire 容器仍为 0
	runRodRemove(t, srv, sess, rod.GUID, 11, line.GUID, 0)
	check(0) // backpack
}

func TestLureFactoryHooksHaveStrength(t *testing.T) {
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	rod, err := srv.CreateItem(ctx, 1000001, "spin_10032_S60L", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	lure, err := srv.CreateItem(ctx, 1000001, "crankbaits_2171", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := srv.ItemToTree(ctx, lure)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Fields) == 0 || tree.Fields[0].Val == nil || len(tree.Fields[0].Val.Items) == 0 {
		t.Fatal("拟饵缺少出厂钩")
	}
	for i, hook := range tree.Fields[0].Val.Items {
		if hook == nil || hook.Class != "agedjipcnpe" {
			t.Fatalf("出厂钩[%d] 类型错误: %+v", i, hook)
		}
		ni := fieldIdxByCS(hook.Class, csNumericID)
		if ni < 0 || hook.Fields[ni].Num == nil {
			t.Fatalf("出厂钩[%d] 缺 numeric_id", i)
		}
		def, err := db.GetItemDef(ctx, int64(u64Of(hook.Fields[ni].Num)))
		if err != nil || def == nil {
			t.Fatalf("出厂钩定义缺失: %v", err)
		}
		want, ok := strengthBitsFor(def, lure.Durability)
		si := fieldIdxByCS(hook.Class, csStrength)
		got := float64(proto.F32FromBits(uint32(u64Of(hook.Fields[si].Num))))
		if !ok || got <= 0 || math.Abs(got-want) > 0.0001 {
			t.Fatalf("出厂钩[%d] 强度=%f，期望 %f（0 会导致搏鱼力度条为空）", i, got, want)
		}
	}
	mountLure(t, db, srv, rod.GUID, lure.GUID)
	st := srv.stateFor(1000001)
	st.mu.Lock()
	st.GearGuid = rod.GUID
	st.mu.Unlock()
	items, err := srv.buildFightItems(ctx, 1000001, false)
	if err != nil || len(items) != 1 {
		t.Fatalf("14/11 4/22 应推 1 个钩对象: n=%d err=%v", len(items), err)
	}
	if items[0].Class != "agedjipcnpe" || items[0].TypeID != 131 {
		t.Fatalf("14/11 4/22 类型=%s/%d，期望 agedjipcnpe/131", items[0].Class, items[0].TypeID)
	}
	hookStrength := float64(proto.F32FromBits(uint32(u64Of(items[0].Field(0).Num))))
	if hookStrength <= 0 {
		t.Fatalf("14/11 推送钩强=%f，期望 >0", hookStrength)
	}
}
