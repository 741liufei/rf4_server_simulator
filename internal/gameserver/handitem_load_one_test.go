package gameserver

import (
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

func loadOneHanditemCompCtx(t *testing.T, sess *Session, parentGUID, itemGUID string) *Ctx {
	t.Helper()
	return &Ctx{
		Sess: sess, Main: 13, Sub: 1,
		Tree:           rigRequestTree(t, proto.Guid(parentGUID), proto.Guid(itemGUID)),
		DecodeOK:       true,
		ExpectResponse: true,
		Log:            logging.Discard(),
	}
}

func loadOnePushHasGUID(list []string, guid string) bool {
	for _, item := range list {
		if item == guid {
			return true
		}
	}
	return false
}

// throwOnce 发起一次 13/5 抛投并解码 805，返回 f3 更新段与 f5 移除段里的全部 GUID。
func throwOnce(t *testing.T, srv *Server, handGUID string) (upserts, removed []string) {
	t.Helper()
	req := rigRequestTree(t, proto.Guid(handGUID),
		wiregen.MustMakeObj("olieodeoaof", map[int]*proto.Value{0: proto.F32(12)}))
	body, ok := hDynamicHanditemThrow(dynamicTestCtx(srv, req))
	if !ok || len(body) == 0 {
		t.Fatal("13/5 未处理")
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	if reason := srv.dispatch.checkRegisteredResponse(13, 5, body, srv.Table()); reason != "" {
		t.Fatalf("13/5 合同守卫误拒: %s", reason)
	}
	return deepGUIDs(dec.Field(3).Val), deepGUIDs(dec.Field(5).Val)
}

// TestHanditemLoadOneAndAutoReload 锁定弹弓「一大堆」修复：客户端按装填数量逐颗
// 渲染并抛出（反编译 0xdf2db0/0xdf5230），整堆装填会一次抛出一大堆。缺省只装 1 颗、
// 剩余留背包，每抛一颗自动从背包补 1 颗，画面=1 颗飞出、背包逐颗减、可连抛。
func TestHanditemLoadOneAndAutoReload(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := t.Context()
	sess, _ := newTunnelProbe(t, srv)

	hand, err := srv.CreateItem(ctx, 1000001, "wh_pc_gb", "backpack", 1)
	if err != nil {
		t.Fatalf("创建手持物失败: %v", err)
	}
	stack, err := srv.CreateItem(ctx, 1000001, "feedcompleted", "backpack", 1)
	if err != nil {
		t.Fatalf("创建窝料堆失败: %v", err)
	}
	srv.setStackCount(ctx, stack, 5)

	// 装填：只装 1 颗进弹弓，剩余 4 颗留背包。
	body, ok := hHanditemSetComp(loadOneHanditemCompCtx(t, sess, hand.GUID, stack.GUID))
	if !ok {
		t.Fatal("13/1 装填未处理")
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatalf("13/1 响应解码失败: %v", err)
	}
	loaded := srv.handitemLoadedGuid(ctx, hand.GUID)
	if loaded == "" || loaded == stack.GUID {
		t.Fatalf("装填应生成新子实例，实得 %q", shortGUID(loaded))
	}
	loadGuids := deepGUIDs(dec)
	if !loadOnePushHasGUID(loadGuids, loaded) || !loadOnePushHasGUID(loadGuids, stack.GUID) {
		t.Fatalf("13/1 应同时下发新子实例和背包来源堆更新，实得 %v", loadGuids)
	}
	if child, _ := db.GetItemByGUID(ctx, loaded); child == nil || child.Container != "rig" || srv.stackCountOf(ctx, child) != 1 {
		t.Fatalf("弹弓弹药格应只装 1 颗，实得 %+v", child)
	}
	if src, _ := db.GetItemByGUID(ctx, stack.GUID); src == nil || src.Container != "backpack" || srv.stackCountOf(ctx, src) != 4 {
		t.Fatalf("剩余 4 颗应留背包，实得 %+v", src)
	}

	// 连抛 4 颗：每抛一颗自动从背包补 1 颗，背包 4→3→2→1→0（最后一颗整堆搬走）。
	for i := 0; i < 4; i++ {
		prevLoaded := srv.handitemLoadedGuid(ctx, hand.GUID)
		upserts, removed := throwOnce(t, srv, hand.GUID)
		if !loadOnePushHasGUID(removed, prevLoaded) {
			t.Fatalf("第 %d 抛：用尽的子实例应进 f5 移除，实得 %v", i+1, removed)
		}
		if row, _ := db.GetItemByGUID(ctx, prevLoaded); row != nil {
			t.Fatalf("第 %d 抛：用尽的子实例应已删除", i+1)
		}
		newLoaded := srv.handitemLoadedGuid(ctx, hand.GUID)
		if newLoaded == "" || newLoaded == prevLoaded {
			t.Fatalf("第 %d 抛后应自动补一颗新子实例，实得 %q", i+1, shortGUID(newLoaded))
		}
		if !loadOnePushHasGUID(upserts, newLoaded) {
			t.Fatalf("第 %d 抛：新补的子实例应进 f3 让客户端重建 ready 球，实得 %v", i+1, upserts)
		}
		if i < 3 && !loadOnePushHasGUID(upserts, stack.GUID) {
			t.Fatalf("第 %d 抛：背包来源堆数量更新应进 f3，实得 %v", i+1, upserts)
		}
		if i == 3 && !loadOnePushHasGUID(removed, stack.GUID) {
			t.Fatalf("第 4 抛：最后一个来源份数用尽时应进 f5 移除来源堆，实得 %v", removed)
		}
		if child, _ := db.GetItemByGUID(ctx, newLoaded); child == nil || srv.stackCountOf(ctx, child) != 1 {
			t.Fatalf("第 %d 抛后弹药格应仍是 1 颗，实得 %+v", i+1, child)
		}
		wantSrc := int64(3 - i) // 3,2,1,0
		src, _ := db.GetItemByGUID(ctx, stack.GUID)
		if wantSrc == 0 {
			if src != nil {
				t.Fatalf("第 %d 抛后背包来源堆应已用尽删除，实得 %+v", i+1, src)
			}
		} else if src == nil || srv.stackCountOf(ctx, src) != wantSrc {
			t.Fatalf("第 %d 抛后背包应剩 %d 颗，实得 %+v", i+1, wantSrc, src)
		}
	}

	// 第 5 抛：背包已空，用尽后无料可补，弹药格清空。
	prevLoaded := srv.handitemLoadedGuid(ctx, hand.GUID)
	throwOnce(t, srv, hand.GUID)
	if row, _ := db.GetItemByGUID(ctx, prevLoaded); row != nil {
		t.Fatal("最后一颗用尽后应删除")
	}
	if got := srv.handitemLoadedGuid(ctx, hand.GUID); got != "" {
		t.Fatalf("背包无料可补时弹药格应清空，实得 %q", shortGUID(got))
	}
}

// TestHanditemLoadOneDisabledKeepsWholeStack 锁定回退开关：handitem_load_one=0
// 时仍是整堆装填（来源堆删除），抛一颗扣 1 不自动补填。客户端行为若变差可原样退回。
func TestHanditemLoadOneDisabledKeepsWholeStack(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := t.Context()
	setWeatherTestTunable(t, srv, "handitem_load_one", "0")
	sess, _ := newTunnelProbe(t, srv)

	hand, err := srv.CreateItem(ctx, 1000001, "wh_pc_gb", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	stack, err := srv.CreateItem(ctx, 1000001, "feedcompleted", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	srv.setStackCount(ctx, stack, 5)

	if _, ok := hHanditemSetComp(loadOneHanditemCompCtx(t, sess, hand.GUID, stack.GUID)); !ok {
		t.Fatal("13/1 装填未处理")
	}
	loaded := srv.handitemLoadedGuid(ctx, hand.GUID)
	if child, _ := db.GetItemByGUID(ctx, loaded); child == nil || srv.stackCountOf(ctx, child) != 5 {
		t.Fatalf("整堆模式应装 5 颗，实得 %+v", child)
	}
	if src, _ := db.GetItemByGUID(ctx, stack.GUID); src != nil {
		t.Fatalf("整堆模式来源堆应删除，实得 %+v", src)
	}

	// 抛一颗：扣 1 剩 4，不自动补填（仍是同一子实例）。
	throwOnce(t, srv, hand.GUID)
	if got := srv.handitemLoadedGuid(ctx, hand.GUID); got != loaded {
		t.Fatalf("整堆模式抛后不应换子实例，实得 %q — 原 %q", shortGUID(got), shortGUID(loaded))
	}
	if child, _ := db.GetItemByGUID(ctx, loaded); child == nil || srv.stackCountOf(ctx, child) != 4 {
		t.Fatalf("整堆模式抛一颗应剩 4，实得 %+v", child)
	}
}
