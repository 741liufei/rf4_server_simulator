package gameserver

// 竿组拆卸与 157 钓组对象完整性测试。
// 拆卸后物品必须回到背包，响应 f1 携带退回树，f2 只包含整条删除的拆分子实例。
// 完整装配后，14/2 的 157 对象应包含线、轮和全部钓组组件。

import (
	"context"
	"testing"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// newDetachTestServer 创建使用独立数据库的测试服务器。
func newDetachTestServer(t *testing.T) (*Server, *store.DB) {
	t.Helper()
	db := openItemTestDB(t)
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "detach"}}, logging.Discard(), nil)
	srv.SetDB(db)
	return srv, db
}

// detachRequestTree 编码并解码请求树，走与 dispatch 相同的解析路径。
func detachRequestTree(t *testing.T, items ...*proto.Value) *proto.Value {
	t.Helper()
	return rigRequestTree(t, items...)
}

// runRodSet 执行 17/2 装配并断言请求已处理。
func runRodSet(t *testing.T, srv *Server, sess *Session, rodGUID, itemGUID string, extra uint64) {
	t.Helper()
	tree := detachRequestTree(t, proto.U32(0), proto.Guid(rodGUID), proto.U32(0),
		proto.Guid(itemGUID), proto.U32(uint32(extra)))
	body, ok := hRodSetComp(&Ctx{Sess: sess, Main: 17, Sub: 2, Tree: tree,
		DecodeOK: true, Log: logging.Discard()})
	if !ok || len(body) == 0 {
		t.Fatalf("17/2 装配 %s 未处理", shortGUID(itemGUID))
	}
}

// fieldGuidHex 读取对象第 i 个字段的 GUID。
// GUID 和 OPTGUID 解码后存放在 Field.Hex 中，Val 为空。
func fieldGuidHex(v *proto.Value, i int) string {
	f := v.Field(i)
	if f.Hex != "" {
		return f.Hex
	}
	if f.Val != nil {
		return f.Val.GuidHex()
	}
	return ""
}

// runRodRemove 执行 17/3 拆卸并返回解码后的 800 响应。
func runRodRemove(t *testing.T, srv *Server, sess *Session, rodGUID string, slot uint64, itemGUID string, extra uint64) *proto.Value {
	t.Helper()
	tree := detachRequestTree(t, proto.U32(0), proto.Guid(rodGUID), proto.U32(uint32(slot)),
		proto.Guid(itemGUID), proto.U32(uint32(extra)))
	body, ok := hRodRemoveComp(&Ctx{Sess: sess, Main: 17, Sub: 3, Tree: tree,
		DecodeOK: true, Log: logging.Discard()})
	if !ok || len(body) == 0 {
		t.Fatalf("17/3 拆卸槽%d 未处理", slot)
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatalf("17/3 响应不可解码: %v", err)
	}
	if dec.Class != "dlbahppbdad" {
		t.Fatalf("17/3 应回 800 dlbahppbdad，实得 %s", dec.Class)
	}
	return dec
}

// snapshotClasses 返回快照 f1 段中元素的 wire 类名。
func snapshotClasses(t *testing.T, seg *proto.Value) []string {
	t.Helper()
	if seg == nil || seg.Kind != proto.KindArgs {
		t.Fatalf("快照段应为 args，实得 %v", seg)
	}
	out := make([]string, 0, seg.ArgCount())
	for i := 0; i < seg.ArgCount(); i++ {
		out = append(out, seg.ArgItem(i).Class)
	}
	return out
}

func assertBackpackRow(t *testing.T, db *store.DB, guid, rodGUID string) *store.Item {
	t.Helper()
	row, err := db.GetItemByGUID(context.Background(), guid)
	if err != nil || row == nil {
		t.Fatalf("物品行丢失 %s: %v", shortGUID(guid), err)
	}
	if row.Container != "backpack" {
		t.Fatalf("退回后 container=%q，应 backpack（拆下即回背包，rig.py:_detach 退回分支）", row.Container)
	}
	if row.RigGUID != "" {
		t.Fatalf("退回后 rig_guid=%q，应清空", row.RigGUID)
	}
	if got := rigStoredGUID(t, row, csParent); got != "" {
		t.Fatalf("退回后 wire 父引用应清空，实得 %q（不清则客户端仍视为挂载中）", got)
	}
	return row
}

func TestDetachReturnsToBackpack(t *testing.T) {
	t.Parallel()
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	sess := newProbeSess(srv)

	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1) // Feeder 竿（竿型 10）
	if err != nil {
		t.Fatalf("造竿失败: %v", err)
	}
	// 钉在路亚族 SimpleJig：本用例验证计件拆分子实例的拆卸回退。
	if !srv.rigSetType(ctx, uid, rod.GUID, 6) {
		t.Fatal("rigSetType 失败")
	}
	reel, err := srv.CreateItem(ctx, uid, "bclp_5004", "backpack", 1) // 渔轮（品类 6）
	if err != nil {
		t.Fatalf("造轮失败: %v", err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_15094", "backpack", 1) // 钓线（品类 11）
	if err != nil {
		t.Fatalf("造线失败: %v", err)
	}
	hook, err := srv.CreateItem(ctx, uid, "c_hook_20712", "backpack", 1) // 鱼钩（品类 12）
	if err != nil {
		t.Fatalf("造钩失败: %v", err)
	}

	// ---- 安装轮、线和钩 ----
	runRodSet(t, srv, sess, rod.GUID, reel.GUID, 0)
	runRodSet(t, srv, sess, rod.GUID, line.GUID, 0)
	runRodSet(t, srv, sess, rod.GUID, hook.GUID, 0)
	for _, it := range []*store.Item{reel, line, hook} {
		row, gerr := db.GetItemByGUID(ctx, it.GUID)
		if gerr != nil || row == nil || row.Container != "rig" || row.RigGUID != rod.GUID {
			t.Fatalf("前置：装配后应 container='rig' 且 rig_guid=竿，实得 %s/%s",
				row.Container, row.RigGUID)
		}
	}

	// ---- 拆线：退回背包，并进入 f1 而不是 f2 ----
	dec := runRodRemove(t, srv, sess, rod.GUID, 11, line.GUID, 0)
	if seg := rigSeg(dec, 0); seg == nil || seg.Kind != proto.KindNull {
		t.Fatalf("拆卸 f0 应为 null，实得 %v", seg)
	}
	if classes := snapshotClasses(t, rigSeg(dec, 1)); len(classes) != 2 ||
		classes[0] != "ohciikgjghe" || classes[1] != "cnolibojkao" {
		t.Fatalf("拆卸 f1 应为 [退回线, 竿]，实得 %v", classes)
	}
	if seg := rigSeg(dec, 2); seg == nil || seg.Kind != proto.KindNull {
		t.Fatalf("普通退回的 guid 绝不能进 f2（客户端 f2=删物品索引 → 物品消失），实得 %v", seg)
	}
	assertBackpackRow(t, db, line.GUID, rod.GUID)
	rodRow, _ := db.GetItemByGUID(ctx, rod.GUID)
	if got := rigStoredGUID(t, rodRow, csLine); got != "" {
		t.Fatalf("拆卸后竿 f3(bghbekmllgp) 应清空，实得 %q", got)
	}
	if g, gerr := db.GetItemGUIDByRigSlot(ctx, rod.GUID, 11); gerr == nil && g != "" {
		t.Fatalf("拆卸后 rig_component 槽 11 应删除，实得 %q", g)
	}

	// ---- 拆钩：验证相同的快照规则 ----
	dec = runRodRemove(t, srv, sess, rod.GUID, 12, hook.GUID, 250)
	if classes := snapshotClasses(t, rigSeg(dec, 1)); len(classes) != 2 ||
		classes[0] != "agedjipcnpe" || classes[1] != "cnolibojkao" {
		t.Fatalf("拆卸 f1 应为 [退回钩, 竿]，实得 %v", classes)
	}
	if seg := rigSeg(dec, 2); seg == nil || seg.Kind != proto.KindNull {
		t.Fatalf("普通退回 f2 应为 null，实得 %v", seg)
	}
	assertBackpackRow(t, db, hook.GUID, rod.GUID)

	// ---- 拆轮：渔轮使用 f4 专用字段 ----
	dec = runRodRemove(t, srv, sess, rod.GUID, 6, reel.GUID, 0)
	if classes := snapshotClasses(t, rigSeg(dec, 1)); len(classes) != 2 ||
		classes[0] != "dgjinoebdop" || classes[1] != "cnolibojkao" {
		t.Fatalf("拆卸 f1 应为 [退回轮, 竿]，实得 %v", classes)
	}
	assertBackpackRow(t, db, reel.GUID, rod.GUID)
	rodRow, _ = db.GetItemByGUID(ctx, rod.GUID)
	if got := rigStoredGUID(t, rodRow, csReel); got != "" {
		t.Fatalf("拆卸后竿 f4(neangpifoka) 应清空，实得 %q", got)
	}

	// ---- 拆下的线、轮和钩可以重新装配 ----
	runRodSet(t, srv, sess, rod.GUID, line.GUID, 0)
	runRodSet(t, srv, sess, rod.GUID, reel.GUID, 0)
	runRodSet(t, srv, sess, rod.GUID, hook.GUID, 0)
	for _, it := range []*store.Item{reel, line, hook} {
		row, gerr := db.GetItemByGUID(ctx, it.GUID)
		if gerr != nil || row == nil || row.Container != "rig" || row.RigGUID != rod.GUID {
			t.Fatalf("再次装配失败：container/rig_guid = %s/%q", row.Container, row.RigGUID)
		}
		if got := rigStoredGUID(t, row, csParent); got != rod.GUID {
			t.Fatalf("再次装配后 wire 父引用应= 竿，实得 %q", got)
		}
	}
	rodRow, _ = db.GetItemByGUID(ctx, rod.GUID)
	if got := rigStoredGUID(t, rodRow, csLine); got != line.GUID {
		t.Fatalf("重新装配后竿 f3 应= 线，实得 %q", got)
	}
	if got := rigStoredGUID(t, rodRow, csReel); got != reel.GUID {
		t.Fatalf("重新装配后竿 f4 应= 轮，实得 %q", got)
	}
	if comps := rigCompsOf(rigStoredF5(t, rodRow)); comps[12].GUID != hook.GUID {
		t.Fatalf("重新装配后 f5 组件表槽 12 应= 钩，实得 %v", comps)
	}

	// ---- 子实例拆卸：整条删除、来源堆数量加一、GUID 进入 f2 ----
	bait, err := srv.CreateItem(ctx, uid, "worm_01", "backpack", 1) // ljnnpokapig=30
	if err != nil {
		t.Fatalf("造饵失败: %v", err)
	}
	srcBefore := srv.stackCountOf(ctx, mustItem(t, db, bait.GUID))
	runRodSet(t, srv, sess, rod.GUID, bait.GUID, 0) // 拆出数量=1 的子实例
	rodRow, _ = db.GetItemByGUID(ctx, rod.GUID)
	child := rigCompsOf(rigStoredF5(t, rodRow))[27]
	if child.GUID == "" || child.GUID == bait.GUID {
		t.Fatalf("计件挂载应拆出子实例，实得 %q", child.GUID)
	}
	dec = runRodRemove(t, srv, sess, rod.GUID, 27, child.GUID, 350)
	if seg := rigSeg(dec, 2); seg == nil || seg.Kind != proto.KindGuidArr ||
		seg.ArgCount() != 1 || seg.ArgItem(0).Hex != child.GUID {
		t.Fatalf("拆分子实例的 guid 应进 f2（行已删除），实得 %v", seg)
	}
	if classes := snapshotClasses(t, rigSeg(dec, 1)); len(classes) != 2 ||
		classes[0] != "kbnbcppjkaf" || classes[1] != "cnolibojkao" {
		t.Fatalf("拆分子实例 f1 应为 [来源堆(+1), 竿]，实得 %v", classes)
	}
	if _, err := db.GetItemByGUID(ctx, child.GUID); err == nil {
		t.Fatal("拆分子实例行应被删除")
	}
	srcAfter := srv.stackCountOf(ctx, mustItem(t, db, bait.GUID))
	if srcAfter != srcBefore {
		t.Fatalf("来源堆应退回 1 个单位（%d → %d）", srcBefore, srcAfter)
	}
	// 鱼饵（bait）堆本体从未离开背包
	if row := mustItem(t, db, bait.GUID); row.Container != "backpack" {
		t.Fatalf("饵堆 container=%q，应 backpack", row.Container)
	}
}

func mustItem(t *testing.T, db *store.DB, guid string) *store.Item {
	t.Helper()
	row, err := db.GetItemByGUID(context.Background(), guid)
	if err != nil || row == nil {
		t.Fatalf("取物品 %s: %v", shortGUID(guid), err)
	}
	return row
}

// TestRig157Completeness 验证全装竿在 14/1 建档后，14/2 返回完整的 157 钓组对象。
func TestRig157Completeness(t *testing.T) {
	t.Parallel()
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	sess := newProbeSess(srv)

	rod, err := srv.CreateItem(ctx, uid, "feeder_10126_FD390", "backpack", 1) // FD390（Feeder，竿型 10）
	if err != nil {
		t.Fatalf("造 FD390 失败: %v", err)
	}
	// 同 TestDetachReturnsToBackpack：钉路亚族保持计件拆分子实例路径。
	if !srv.rigSetType(ctx, uid, rod.GUID, 6) {
		t.Fatal("rigSetType 失败")
	}
	reel, err := srv.CreateItem(ctx, uid, "spin_5184", "backpack", 1) // 渔轮
	if err != nil {
		t.Fatalf("造轮失败: %v", err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_15094", "backpack", 1) // 钓线
	if err != nil {
		t.Fatalf("造线失败: %v", err)
	}
	hook, err := srv.CreateItem(ctx, uid, "clas_20163", "backpack", 1) // 鱼钩
	if err != nil {
		t.Fatalf("造钩失败: %v", err)
	}
	bell, err := srv.CreateItem(ctx, uid, "feeder_bell", "backpack", 1) // 食笼铃（品类 47）
	if err != nil {
		t.Fatalf("造铃失败: %v", err)
	}
	bait, err := srv.CreateItem(ctx, uid, "bloodworm", "backpack", 1) // 饵（拆分子实例）
	if err != nil {
		t.Fatalf("造饵失败: %v", err)
	}

	runRodSet(t, srv, sess, rod.GUID, reel.GUID, 0)
	runRodSet(t, srv, sess, rod.GUID, line.GUID, 0)
	runRodSet(t, srv, sess, rod.GUID, bell.GUID, 100)
	runRodSet(t, srv, sess, rod.GUID, hook.GUID, 250)
	runRodSet(t, srv, sess, rod.GUID, bait.GUID, 0) // 计件 → 子实例

	// 先直接执行建档和尖梢收敛，避免测试会话触发 4/22 推送。
	srv.ensureRig(ctx, uid, rod.GUID)

	// ---- 14/1 返回 158 手持上下文，f3 是钩 GUID ----
	tree := detachRequestTree(t, proto.Guid(rod.GUID))
	body, ok := hCastPrepare(&Ctx{Sess: sess, Main: 14, Sub: 1, Tree: tree,
		DecodeOK: true, Log: logging.Discard()})
	if !ok || len(body) == 0 {
		t.Fatal("14/1 未处理")
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil || dec.Class != "lfnjkiffphn" {
		t.Fatalf("14/1 应回 158 lfnjkiffphn，实得 %v err=%v", dec, err)
	}
	// GUID 解码后存放在 Field.Hex，Val 为空。
	if got := fieldGuidHex(dec, 3); got != hook.GUID {
		t.Fatalf("14/1 f3 应= 钩 guid（hhfadijinob），实得 %q", got)
	}

	// ---- 14/2 返回 800，f0 是包含 157 钓组对象的 args(61) ----
	tree = detachRequestTree(t, proto.Guid(rod.GUID))
	body, ok = hCast(&Ctx{Sess: sess, Main: 14, Sub: 2, Tree: tree,
		DecodeOK: true, Log: logging.Discard()})
	if !ok || len(body) == 0 {
		t.Fatal("14/2 未处理")
	}
	dec, _, err = proto.DecodeValue(body, 0, srv.Table())
	if err != nil || dec.Class != "dlbahppbdad" {
		t.Fatalf("14/2 应回 800，实得 %v err=%v", dec, err)
	}
	f0 := rigSeg(dec, 0)
	if f0 == nil || f0.Kind != proto.KindArgs || f0.ArgCount() != 1 {
		t.Fatalf("14/2 f0 应为 args(61)[157]，实得 %v", f0)
	}
	rig157 := f0.ArgItem(0)
	if rig157.Class != "cnolibojkao" || rig157.FieldCount() < 16 {
		t.Fatalf("f0[0] 应为完整 157 cnolibojkao，实得 %s（%d 字段）",
			rig157.Class, rig157.FieldCount())
	}
	// f3 是线 GUID，f4 是轮 GUID。
	if got := fieldGuidHex(rig157, 3); got != line.GUID {
		t.Fatalf("157.f3 应= 线 guid，实得 %q", got)
	}
	if got := fieldGuidHex(rig157, 4); got != reel.GUID {
		t.Fatalf("157.f4 应= 轮 guid，实得 %q", got)
	}
	// 出厂部件必须是内嵌对象：155 竿坯、156 手柄、159 竿座。
	for idx, want := range map[int]string{0: "kjggkpflhjp", 1: "hgabifpnhmk", 2: "knocoeggfdk"} {
		sub := rig157.Field(idx).Val
		if sub == nil || sub.Kind != proto.KindObj || sub.Class != want {
			t.Fatalf("157.f%d 应为出厂部件 %s，实得 %v", idx, want, sub)
		}
	}
	// f13 是物品数字 ID（FD390 为 5559）。
	if got := u64Of(rig157.Field(13).Num); got != 5559 {
		t.Fatalf("157.f13 应为数字 id 5559，实得 %d", got)
	}
	// f14/f15 工艺/耐久 = 1.0
	if got := proto.F32FromBits(uint32(u64Of(rig157.Field(14).Num))); got != 1.0 {
		t.Fatalf("157.f14 工艺应 1.0，实得 %v", got)
	}
	if got := proto.F32FromBits(uint32(u64Of(rig157.Field(15).Num))); got != 1.0 {
		t.Fatalf("157.f15 耐久应 1.0，实得 %v", got)
	}
	// f5 是 153 钓组对象，类型为 12，组件包含 47/12/27。
	f5 := rig157.Field(5).Val
	if f5 == nil || f5.Kind != proto.KindObj || f5.Class != "gbbhdkefnkn" {
		t.Fatalf("157.f5 应为 153 gbbhdkefnkn，实得 %v", f5)
	}
	f5f0 := f5.Field(0)
	if name := f5f0.StrOr(); name != "rig_BottomSimple" {
		t.Fatalf("153.f0 应 rig_BottomSimple（Feeder 派生），实得 %q", name)
	}
	if got := int64(u64Of(f5.Field(1).Num)); got != 12 {
		t.Fatalf("153.f1 应 12（BottomSimple），实得 %d", got)
	}
	comps := rigCompsOf(f5)
	if c, ok := comps[47]; !ok || c.GUID != bell.GUID || c.Extra != 100 {
		t.Fatalf("152 组件表缺 食笼铃(47, extra100)：实得 %v", comps)
	}
	if c, ok := comps[12]; !ok || c.GUID != hook.GUID || c.Extra != 250 {
		t.Fatalf("152 组件表缺 钩(12, extra250)：实得 %v", comps)
	}
	baitRow := mustItem(t, db, bait.GUID)
	if c, ok := comps[27]; !ok || c.GUID == "" || c.GUID == bait.GUID {
		t.Fatalf("152 组件表缺 饵(27) 挂载子实例：实得 %v", comps)
	} else if cnt := srv.stackCountOf(ctx, mustItem(t, db, c.GUID)); cnt != 1 {
		t.Fatalf("饵挂载子实例数量应 1，实得 %d", cnt)
	} else if cnt != 1 || baitRow.Container != "backpack" {
		t.Fatalf("饵来源堆应在背包且不被动，实得 container=%s", baitRow.Container)
	}
	// 组件顺序必须按规范索引升序：铃 100、钩 250、饵 350。
	if comps[47].Extra >= comps[12].Extra || comps[12].Extra >= comps[27].Extra {
		t.Fatalf("152 组件规范索引应升序 100<250<350，实得 %v", comps)
	}
	// 数据库中的钓组类型也应同步为 12。
	if rt, found, gerr := db.GetRigType(ctx, rod.GUID); gerr != nil || !found || rt != 12 {
		t.Fatalf("rig 行 rig_type 应 12，实得 %d (found=%v err=%v)", rt, found, gerr)
	}
}
