package gameserver

// 沉子单实例回归：鲤鱼钓组（24-29、34）模板里有两个 {29,200} 沉子位，
// 规范索引完全一致。旧逻辑会把第二次安装写到 ordinal=1，而客户端按索引
// 渲染同一槽位，结果是一枚沉子叠两份：
//   - 重量按两份计，甩线时客户端误判超重、播炸线音效但服务端不真的断线；
//   - 收起一次只卸掉一条 rig_component，要再收一次才真正干净。
// 修复后同索引多槽位去重，第二次安装按替换处理。

import (
	"context"
	"testing"
)

func TestSinkerKeepsSingleInstance(t *testing.T) {
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	sess := newProbeSess(srv)

	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatalf("造竿失败: %v", err)
	}
	if !srv.rigSetType(ctx, uid, rod.GUID, 26) { // BottomPopup：模板含两个 {29,200}
		t.Fatal("rigSetType 失败")
	}
	first, err := srv.CreateItem(ctx, uid, "flpsinker_30149", "backpack", 1) // Flat Pear Clay SW 50
	if err != nil {
		t.Fatalf("造第一枚沉子失败: %v", err)
	}
	second, err := srv.CreateItem(ctx, uid, "flpsinker_30149", "backpack", 1)
	if err != nil {
		t.Fatalf("造第二枚沉子失败: %v", err)
	}

	runRodSet(t, srv, sess, rod.GUID, first.GUID, 200)
	runRodSet(t, srv, sess, rod.GUID, second.GUID, 200)

	rows, err := db.ListRigComponents(ctx, rod.GUID)
	if err != nil {
		t.Fatalf("读组件失败: %v", err)
	}
	count := 0
	var ordinal int64
	var guid string
	for _, rc := range rows {
		if rc.Slot != 29 {
			continue
		}
		count++
		ordinal, guid = rc.Ordinal, rc.GUID
	}
	if count != 1 {
		t.Fatalf("沉子应只有 1 条 rig_component，实得 %d 条", count)
	}
	if ordinal != 0 {
		t.Fatalf("沉子 ordinal=%d，应固定为 0（同索引槽位不再分配第二实例）", ordinal)
	}
	if guid != second.GUID {
		t.Fatalf("槽位应换成后装的 %s，实得 %s", shortGUID(second.GUID), shortGUID(guid))
	}

	prev, err := db.GetItemByGUID(ctx, first.GUID)
	if err != nil || prev == nil {
		t.Fatalf("第一枚沉子行丢失: %v", err)
	}
	if prev.Container != "backpack" || prev.RigGUID != "" {
		t.Fatalf("第一枚应退回背包，实得 container=%q rig=%q", prev.Container, prev.RigGUID)
	}
}

func TestLegacyDuplicateSinkersAreReturnedByProjection(t *testing.T) {
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !srv.rigSetType(ctx, uid, rod.GUID, 26) {
		t.Fatal("rigSetType 失败")
	}
	kept, err := srv.CreateItem(ctx, uid, "flpsinker_30149", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	extra, err := srv.CreateItem(ctx, uid, "flpsinker_30149", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	srv.rigSetComponent(ctx, uid, rod.GUID, 0, kept.GUID, 200,
		rigSetOpts{ParentType: familyRod, EnforceCategory: true})

	// 构造旧版本留下的第二个同索引沉子。
	row, err := db.GetItemByGUID(ctx, rod.GUID)
	if err != nil || row == nil {
		t.Fatal(err)
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	idx := fieldIdxByCS(row.WireClass, csRigObject)
	name, rigType, ok := rigIdentity(fields[idx].Val)
	if !ok {
		t.Fatal("缺少钓组身份")
	}
	comps := rigCompsOf(fields[idx].Val)
	comps[rigCompKey(29, 1)] = rigComp{Category: 29, Ordinal: 1, GUID: extra.GUID, Extra: 200}
	fields[idx].Val = buildRig153Like(fields[idx].Val, name, rigType, comps, 100, 0)
	srv.saveItemFields(ctx, row, fields)
	if err := db.SetItemRigLink(ctx, extra.GUID, rod.GUID, 29); err != nil {
		t.Fatal(err)
	}

	if err := srv.reconcileRigProjection(ctx, rod.GUID); err != nil {
		t.Fatal(err)
	}
	fresh, err := db.GetItemByGUID(ctx, rod.GUID)
	if err != nil {
		t.Fatal(err)
	}
	got := rigTestCategoryComps(t, rigStoredF5(t, fresh), 29)
	if len(got) != 1 || got[0].GUID != kept.GUID || got[0].Ordinal != 0 {
		t.Fatalf("历史重复沉子未清理: %+v", got)
	}
	returned, err := db.GetItemByGUID(ctx, extra.GUID)
	if err != nil || returned == nil {
		t.Fatal(err)
	}
	if returned.Container != "backpack" || returned.RigGUID != "" {
		t.Fatalf("多余沉子未退回背包: %+v", returned)
	}
	rows, err := db.ListRigComponents(ctx, rod.GUID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, rc := range rows {
		if rc.Slot == 29 {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("派生表仍保留 %d 条沉子", count)
	}
}
