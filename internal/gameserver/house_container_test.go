package gameserver

import (
	"context"
	"testing"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

// 房屋仓库（容器枚举 10/11）的库存枚举链路：4/1 计数与 4/4 列表必须能
// 看到 item.container='house' 的物品，且容量非零。缺数据源时客户端房屋
// 仓库 UI 永远为空，存放交互看上去"没反应"（物品实际已入库）。
func TestHouseContainerInventory(t *testing.T) {
	t.Parallel()
	db := openStaticTestDB(t)
	ctx := context.Background()
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "house"}}, logging.Discard(), nil)
	srv.SetDB(db)
	if err := srv.EnsurePlayer(ctx, 1000001); err != nil {
		t.Fatal(err)
	}
	sess := &Session{ID: 1, Userid: 1000001, Srv: srv, log: logging.Discard()}

	// 枚举映射：10/11 都指向 house 存储。
	for _, kind := range []uint64{10, 11} {
		got := nativeInventoryContainers(kind)
		if len(got) != 1 || got[0] != "house" {
			t.Fatalf("nativeInventoryContainers(%d) = %v，期望 [house]", kind, got)
		}
	}
	if nativeInventoryContainers(12) != nil {
		t.Fatal("未知容器枚举应回 nil")
	}

	// 播种：背包 1 件 + 房屋 2 件。
	if _, err := srv.CreateItem(ctx, 1000001, "worm_01", "backpack", 1); err != nil {
		t.Fatalf("创建 worm_01 失败: %v", err)
	}
	houseGUIDs := make([]string, 0, 2)
	for i := 0; i < 2; i++ {
		it, err := srv.CreateItem(ctx, 1000001, "worm_01", "house", 1)
		if err != nil {
			t.Fatalf("创建 worm_01 失败: %v", err)
		}
		houseGUIDs = append(houseGUIDs, it.GUID)
	}

	// 4/1 容器 10：used=2、容量非零。
	b, ok := hItemCount(&Ctx{Sess: sess, Main: 4, Sub: 1, Tree: proto.Args524(proto.U32(10)), DecodeOK: true})
	if !ok {
		t.Fatal("4/1 未处理")
	}
	dec, _, err := proto.DecodeValue(b, 0, srv.Table())
	if err != nil {
		t.Fatalf("4/1 响应解码失败: %v", err)
	}
	if dec.Kind != proto.KindObj || len(dec.Fields) < 3 {
		t.Fatalf("4/1 响应形状异常: kind=%v fields=%d", dec.Kind, len(dec.Fields))
	}
	used, cap, capMember := u64Of(dec.Fields[0].Num), u64Of(dec.Fields[1].Num), u64Of(dec.Fields[2].Num)
	if used != 2 {
		t.Errorf("4/1 容器10 used=%d，期望 2", used)
	}
	if cap != houseContainerCapacity {
		t.Errorf("4/1 容器10 容量=%d，期望 %d", cap, houseContainerCapacity)
	}
	if capMember != 0 {
		t.Errorf("4/1 容器10 会员容量=%d，期望 0", capMember)
	}

	// 4/1 容器 11 同一存储。
	b11, _ := hItemCount(&Ctx{Sess: sess, Main: 4, Sub: 1, Tree: proto.Args524(proto.U32(11)), DecodeOK: true})
	dec11, _, err := proto.DecodeValue(b11, 0, srv.Table())
	if err != nil {
		t.Fatalf("4/1 容器11 解码失败: %v", err)
	}
	if u64Of(dec11.Fields[0].Num) != 2 {
		t.Errorf("4/1 容器11 used=%d，期望 2", u64Of(dec11.Fields[0].Num))
	}
	if got := u64Of(dec11.Fields[1].Num); got != houseContainerCapacity {
		t.Errorf("4/1 容器11 容量=%d，期望 %d", got, houseContainerCapacity)
	}

	// 4/4 容器 10：回 house 内 2 个 GUID，且与 DB 一致。
	b44, ok := hItemGUIDs(&Ctx{Sess: sess, Main: 4, Sub: 4, Tree: proto.Args524(proto.U32(10)), DecodeOK: true})
	if !ok {
		t.Fatal("4/4 未处理")
	}
	dec44, _, err := proto.DecodeValue(b44, 0, srv.Table())
	if err != nil {
		t.Fatalf("4/4 响应解码失败: %v", err)
	}
	if dec44.Kind != proto.KindGuidArr || len(dec44.Items) != 2 {
		t.Fatalf("4/4 容器10 应回 2 个 GUID，实得 kind=%v n=%d", dec44.Kind, len(dec44.Items))
	}
	want, err := srv.DB.ItemGUIDsByContainers(ctx, 1000001, []string{"house"})
	if err != nil {
		t.Fatal(err)
	}
	if len(dec44.Items) != len(want) {
		t.Fatalf("4/4 与 DB 不一致: %d vs %d", len(dec44.Items), len(want))
	}
	for i, it := range dec44.Items {
		gotGUID := ""
		if it.Hex != "" {
			gotGUID = it.Hex
		}
		if gotGUID != want[i] {
			t.Errorf("4/4[%d] = %s，期望 %s", i, gotGUID, want[i])
		}
	}

	// 4/5 容器 10：返回同一批房屋物品树，且树内枚举为 10。
	b45, ok := hItemDetails(&Ctx{Sess: sess, Main: 4, Sub: 5,
		Tree: proto.Args524(proto.U32(10), proto.GuidArr(houseGUIDs)), DecodeOK: true})
	if !ok {
		t.Fatal("4/5 未处理")
	}
	dec45, _, err := proto.DecodeValue(b45, 0, srv.Table())
	if err != nil {
		t.Fatalf("4/5 解码失败: %v", err)
	}
	if dec45.ArgCount() != 2 {
		t.Fatalf("4/5 容器10 应回 2 件物品，实得 %d", dec45.ArgCount())
	}
	for _, item := range dec45.Items {
		idx := fieldIdxByCS(item.Class, csSlotEnum)
		if idx < 0 {
			t.Errorf("4/5 房屋物品 %s 缺容器枚举", item.Class)
			continue
		}
		if got := u64Of(item.Fields[idx].Num); got != 10 {
			t.Errorf("4/5 房屋物品 %s 枚举=%d，期望 10", item.Class, got)
		}
	}
}

// 房屋物品的树内容器枚举必须投影为 10：客户端物品管理器按该枚举把
// 物品挂进对应容器字典，恒 0 时房屋仓库永远为空。
func TestHouseItemTreeStorageKind(t *testing.T) {
	t.Parallel()
	db := openStaticTestDB(t)
	ctx := context.Background()
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "house"}}, logging.Discard(), nil)
	srv.SetDB(db)
	if err := srv.EnsurePlayer(ctx, 1000001); err != nil {
		t.Fatal(err)
	}
	it, err := srv.CreateItem(ctx, 1000001, "worm_01", "house", 1)
	if err != nil {
		t.Fatalf("创建 worm_01 失败: %v", err)
	}
	tree, err := srv.ItemToTree(ctx, it)
	if err != nil {
		t.Fatal(err)
	}
	idx := fieldIdxByCS(tree.Class, csSlotEnum)
	if idx < 0 {
		t.Fatalf("类 %s 缺容器枚举字段", tree.Class)
	}
	if got := u64Of(tree.Fields[idx].Num); got != 10 {
		t.Errorf("房屋物品树容器枚举=%d，期望 10", got)
	}

	// 背包物品保持 0。
	bp, err := srv.CreateItem(ctx, 1000001, "worm_01", "backpack", 1)
	if err != nil {
		t.Fatalf("创建 worm_01 失败: %v", err)
	}
	bpTree, err := srv.ItemToTree(ctx, bp)
	if err != nil {
		t.Fatal(err)
	}
	if got := u64Of(bpTree.Fields[idx].Num); got != 0 {
		t.Errorf("背包物品树容器枚举=%d，期望 0", got)
	}
}

// 4/6 端到端：背包→房屋、房屋→背包时，DB 归属和 4/21/4/22
// 增量树都必须同步更新。测试必须消费真实推送，不能只验证 ItemToTree。
func TestHouseItemMoveEndToEnd(t *testing.T) {
	t.Parallel()
	srv, _ := newStaticRigTestServer(t)
	ctx := t.Context()
	sess, peer := newTunnelProbe(t, srv)
	pushes := handitemPushReader(t, peer)
	it, err := srv.CreateItem(ctx, sess.Userid, "worm_01", "backpack", 1)
	if err != nil {
		t.Fatalf("创建 worm_01 失败: %v", err)
	}

	runMove := func(dst uint64, wantContainer string, wantKind uint64) {
		t.Helper()
		tree := proto.Args524(proto.U32(0), proto.U32(uint32(dst)), proto.GuidArr([]string{it.GUID}))
		body, ok := hItemMove(&Ctx{Sess: sess, Main: 4, Sub: 6, Tree: tree, DecodeOK: true,
			ExpectResponse: true, Log: logging.Discard()})
		if !ok || len(body) == 0 {
			t.Fatalf("4/6 移动到 %d 未处理", dst)
		}
		first := nextPush(t, pushes)
		if first.Main != 4 || first.Sub != 21 || !pushHasGUID(pushGuids(t, srv, first), it.GUID) {
			t.Fatalf("移动到 %d 的首条推送 = %d/%d，期望 4/21 删除 %s", dst, first.Main, first.Sub, shortGUID(it.GUID))
		}
		second := nextPush(t, pushes)
		if second.Main != 4 || second.Sub != 22 || !pushHasGUID(pushGuids(t, srv, second), it.GUID) {
			t.Fatalf("移动到 %d 的次条推送 = %d/%d，期望 4/22 加入 %s", dst, second.Main, second.Sub, shortGUID(it.GUID))
		}
		dec, _, err := proto.DecodeValue(second.Body, 0, srv.Table())
		if err != nil {
			t.Fatalf("4/22 解码失败: %v", err)
		}
		if dec.ArgCount() < 1 {
			t.Fatalf("4/22 没有物品树: %+v", dec)
		}
		itemTree := dec.ArgItem(dec.ArgCount() - 1)
		idx := fieldIdxByCS(itemTree.Class, csSlotEnum)
		if idx < 0 {
			t.Fatalf("4/22 物品树类 %s 缺容器枚举", itemTree.Class)
		}
		if got := u64Of(itemTree.Fields[idx].Num); got != wantKind {
			t.Fatalf("4/22 容器枚举=%d，期望 %d", got, wantKind)
		}
		row, err := srv.DB.GetItemByGUID(ctx, it.GUID)
		if err != nil || row == nil {
			t.Fatalf("读取移动后物品失败: %+v %v", row, err)
		}
		if row.Container != wantContainer {
			t.Fatalf("移动后 container=%s，期望 %s", row.Container, wantContainer)
		}
	}

	runMove(10, "house", 10)
	runMove(0, "backpack", 0)
}

// 房屋容量在事务内校验：已有 100 件时一次移动 2 件必须整体失败，
// 不能出现第 101/102 件已落库或只移动了一部分。
func TestHouseItemMoveCapacityRollsBack(t *testing.T) {
	t.Parallel()
	srv, db := newStaticRigTestServer(t)
	ctx := t.Context()
	sess := newProbeSess(srv)
	for i := 0; i < houseContainerCapacity; i++ {
		if _, err := srv.CreateItem(ctx, sess.Userid, "worm_01", "house", 1); err != nil {
			t.Fatalf("创建房屋物品失败: %v", err)
		}
	}
	guids := make([]string, 0, 2)
	for i := 0; i < 2; i++ {
		it, err := srv.CreateItem(ctx, sess.Userid, "worm_01", "backpack", 1)
		if err != nil {
			t.Fatalf("创建背包物品失败: %v", err)
		}
		guids = append(guids, it.GUID)
	}
	before, err := db.CountItemsByContainers(ctx, sess.Userid, []string{"house"})
	if err != nil {
		t.Fatal(err)
	}
	tree := proto.Args524(proto.U32(0), proto.U32(10), proto.GuidArr(guids))
	capture := &ctxReplyCapture{}
	body, ok := hItemMove(&Ctx{Sess: sess, Main: 4, Sub: 6, Tree: tree, DecodeOK: true,
		ExpectResponse: true, Log: logging.Discard(), replyCapture: capture})
	if !ok || body != nil {
		t.Fatalf("容量已满时应返回失败而非 800 快照: ok=%v body=%d", ok, len(body))
	}
	if _, present, _, failure := capture.snapshot(); !present || !failure {
		t.Fatalf("容量已满时必须记录失败响应: present=%v failure=%v", present, failure)
	}
	after, err := db.CountItemsByContainers(ctx, sess.Userid, []string{"house"})
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("容量失败后房屋物品数 %d，期望保持 %d", after, before)
	}
	for _, g := range guids {
		row, err := db.GetItemByGUID(ctx, g)
		if err != nil || row == nil {
			t.Fatalf("读取被拒绝物品失败: %+v %v", row, err)
		}
		if row.Container != "backpack" {
			t.Fatalf("容量失败后物品 %s 的 container=%s，期望 backpack", shortGUID(g), row.Container)
		}
	}
}

// 同一 GUID 在请求中重复出现时只计一件；99 件已有物品加 1 件新物品
// 应正好落在容量上限 100，而不是被重复计数后误判为超限。
func TestHouseItemMoveDeduplicatesGUIDs(t *testing.T) {
	t.Parallel()
	srv, db := newStaticRigTestServer(t)
	ctx := t.Context()
	sess := newProbeSess(srv)
	for i := 0; i < houseContainerCapacity-1; i++ {
		if _, err := srv.CreateItem(ctx, sess.Userid, "worm_01", "house", 1); err != nil {
			t.Fatalf("创建房屋物品失败: %v", err)
		}
	}
	it, err := srv.CreateItem(ctx, sess.Userid, "worm_01", "backpack", 1)
	if err != nil {
		t.Fatalf("创建背包物品失败: %v", err)
	}
	tree := proto.Args524(proto.U32(0), proto.U32(10), proto.GuidArr([]string{it.GUID, it.GUID}))
	body, ok := hItemMove(&Ctx{Sess: sess, Main: 4, Sub: 6, Tree: tree, DecodeOK: true,
		ExpectResponse: false, Log: logging.Discard()})
	if !ok || body == nil {
		t.Fatalf("容量边界移动应成功: ok=%v body=%d", ok, len(body))
	}
	row, err := db.GetItemByGUID(ctx, it.GUID)
	if err != nil || row == nil || row.Container != "house" {
		t.Fatalf("重复 GUID 应只移动一次并进入房屋: %+v %v", row, err)
	}
	used, err := db.CountItemsByContainers(ctx, sess.Userid, []string{"house"})
	if err != nil || used != houseContainerCapacity {
		t.Fatalf("房屋物品数=%d err=%v，期望 %d", used, err, houseContainerCapacity)
	}
}

// 容量校验必须与 4/1 展示的导入容量一致，不能固定写死 100。
func TestHouseItemMoveHonorsImportedCapacity(t *testing.T) {
	t.Parallel()
	srv, db := newStaticRigTestServer(t)
	ctx := t.Context()
	sess := newProbeSess(srv)
	if err := db.KVSet(ctx, "native-import:1000001:inventory", `{"countsByKind":{"10":{"a":2,"b":0}}}`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := srv.CreateItem(ctx, sess.Userid, "worm_01", "house", 1); err != nil {
			t.Fatalf("创建房屋物品失败: %v", err)
		}
	}
	it, err := srv.CreateItem(ctx, sess.Userid, "worm_01", "backpack", 1)
	if err != nil {
		t.Fatalf("创建背包物品失败: %v", err)
	}
	countBody, ok := hItemCount(&Ctx{Sess: sess, Main: 4, Sub: 1, Tree: proto.Args524(proto.U32(10)), DecodeOK: true})
	if !ok {
		t.Fatal("4/1 未处理")
	}
	dec, _, err := proto.DecodeValue(countBody, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	if cap := u64Of(dec.Fields[1].Num); cap != 2 {
		t.Fatalf("4/1 导入容量=%d，期望 2", cap)
	}
	tree := proto.Args524(proto.U32(0), proto.U32(10), proto.GuidArr([]string{it.GUID}))
	body, ok := hItemMove(&Ctx{Sess: sess, Main: 4, Sub: 6, Tree: tree, DecodeOK: true,
		ExpectResponse: false, Log: logging.Discard()})
	if !ok || body != nil {
		t.Fatalf("导入容量已满时应拒绝移动: ok=%v body=%d", ok, len(body))
	}
	row, err := db.GetItemByGUID(ctx, it.GUID)
	if err != nil || row == nil || row.Container != "backpack" {
		t.Fatalf("导入容量拒绝后物品应留在背包: %+v %v", row, err)
	}
}
