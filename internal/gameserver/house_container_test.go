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
	db := openItemTestDB(t)
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
		t.Skipf("worm_01 创建跳过: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := srv.CreateItem(ctx, 1000001, "worm_01", "house", 1); err != nil {
			t.Skipf("worm_01 创建跳过: %v", err)
		}
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
	if cap == 0 {
		t.Errorf("4/1 容器10 容量=0，客户端会认为仓库不可用")
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
}

// 房屋物品的树内容器枚举必须投影为 10：客户端物品管理器按该枚举把
// 物品挂进对应容器字典，恒 0 时房屋仓库永远为空。
func TestHouseItemTreeStorageKind(t *testing.T) {
	db := openItemTestDB(t)
	ctx := context.Background()
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "house"}}, logging.Discard(), nil)
	srv.SetDB(db)
	if err := srv.EnsurePlayer(ctx, 1000001); err != nil {
		t.Fatal(err)
	}
	it, err := srv.CreateItem(ctx, 1000001, "worm_01", "house", 1)
	if err != nil {
		t.Skipf("worm_01 创建跳过: %v", err)
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
		t.Skipf("worm_01 创建跳过: %v", err)
	}
	bpTree, err := srv.ItemToTree(ctx, bp)
	if err != nil {
		t.Fatal(err)
	}
	if got := u64Of(bpTree.Fields[idx].Num); got != 0 {
		t.Errorf("背包物品树容器枚举=%d，期望 0", got)
	}
}

// 4/6 端到端：背包→房屋移动后 DB 归属与树枚举同步更新。
func TestHouseItemMoveEndToEnd(t *testing.T) {
	db := openItemTestDB(t)
	ctx := context.Background()
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "house"}}, logging.Discard(), nil)
	srv.SetDB(db)
	if err := srv.EnsurePlayer(ctx, 1000001); err != nil {
		t.Fatal(err)
	}
	sess := &Session{ID: 1, Userid: 1000001, Srv: srv, log: logging.Discard()}
	sess.state = map[string]any{}
	it, err := srv.CreateItem(ctx, 1000001, "worm_01", "backpack", 1)
	if err != nil {
		t.Skipf("worm_01 创建跳过: %v", err)
	}
	tree := proto.Args524(proto.U32(0), proto.U32(10), proto.GuidArr([]string{it.GUID}))
	if _, ok := hItemMove(&Ctx{Sess: sess, Main: 4, Sub: 6, Tree: tree, DecodeOK: true, Log: logging.Discard()}); !ok {
		t.Fatal("4/6 未处理")
	}
	row, err := srv.DB.GetItemByGUID(ctx, it.GUID)
	if err != nil || row == nil {
		t.Fatal(err)
	}
	if row.Container != "house" {
		t.Errorf("移动后 container=%s，期望 house", row.Container)
	}
	moveTree, err := srv.ItemToTree(ctx, row)
	if err != nil {
		t.Fatal(err)
	}
	idx := fieldIdxByCS(moveTree.Class, csSlotEnum)
	if got := u64Of(moveTree.Fields[idx].Num); got != 10 {
		t.Errorf("移动后树容器枚举=%d，期望 10", got)
	}
}
