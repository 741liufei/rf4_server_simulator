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
