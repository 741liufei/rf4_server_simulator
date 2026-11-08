package gameserver

import (
	"context"
	"math"
	"testing"
	"time"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

func dynamicTestCtx(srv *Server, tree *proto.Value) *Ctx {
	return &Ctx{
		Sess: newProbeSess(srv), Main: 0, Sub: 0, Tree: tree,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard(),
	}
}

func TestSonarRollContextUsesBiteContextState(t *testing.T) {
	srv, _ := newRigTestServer(t)
	sess, _ := newTunnelProbe(t, srv)
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	st := srv.stateFor(sess.Userid)
	st.mu.Lock()
	st.HasPos, st.PosX, st.PosZ = true, 10, 20
	st.HasCastPos, st.CastPosX, st.CastPosZ = true, 110, 220
	st.Spot, st.RigType, st.DepthCM = 9, 50, 450
	st.LureLayer, st.LureAction, st.LureMotionMask, st.ReelSetting = .9, "marine_jig", 0x400, 8
	st.Bonus = fishingSkillBonus{BiteSpeed: 1.1, EcologyInterest: .22, TrophyLuck: .18}
	st.mu.Unlock()

	c := sonarRollContext(srv, sess, "loc12_215", now)
	if !c.hasPos || c.x != 110 || c.z != 220 {
		t.Fatalf("声呐应优先使用真实抛竿落点: %+v", c)
	}
	if c.spot != 9 || c.rigType != 50 || c.depthCM != 450 ||
		c.lureLayer != .9 || c.lureAction != "marine_jig" || c.lureMask != 0x400 || c.lureSpeed != 8 {
		t.Fatalf("声呐竿位上下文与真实咬口不一致: %+v", c)
	}
	if math.Abs(c.skill.TrophyLuck-.18) > 1e-9 || math.Abs(c.skill.EcologyInterest-.22) > 1e-9 {
		t.Fatalf("声呐未继承当前技能加成: %+v", c.skill)
	}
}

func TestDynamicProtocolHandlersShapeAndEcho(t *testing.T) {
	t.Parallel()
	srv, _ := newRigTestServer(t)
	registerDynamicProtocolHandlers(srv.dispatch)
	for _, k := range []key2{{13, 5}, {13, 6}, {17, 9}, {17, 10}, {17, 11}, {3, 40}, {3, 41}, {1, 10}} {
		if srv.dispatch.lookup(k.main, k.sub) == nil {
			t.Fatalf("%d/%d 未注册动态 handler", k.main, k.sub)
		}
	}

	t.Run("1/10 sonar echoes water", func(t *testing.T) {
		tree := rigRequestTree(t, proto.StrVal("loc12_01"))
		body, ok := hDynamicSonar(dynamicTestCtx(srv, tree))
		if !ok {
			t.Fatal("1/10 未处理")
		}
		dec, _, err := proto.DecodeValue(body, 0, srv.Table())
		if err != nil {
			t.Fatal(err)
		}
		if dec.Class != "efnipphkbce" || dec.TypeID != 15003 {
			t.Fatalf("1/10 响应类型=%s/%d", dec.Class, dec.TypeID)
		}
		f0 := dec.Field(0)
		if got := f0.StrOr(); got != "loc12_01" {
			t.Fatalf("1/10 f0=%q, 期望 loc12_01", got)
		}
		grid := dec.Field(1).Val
		if grid == nil || grid.Kind != proto.KindArgs || grid.ArgCodeStr() != sonarCellArgCode || grid.ArgCount() != 0 {
			t.Fatalf("1/10 f1 应为空 args(%s)，实得 %#v", sonarCellArgCode, grid)
		}
		if reason := srv.dispatch.checkRegisteredResponse(1, 10, body, srv.Table()); reason != "" {
			t.Fatalf("1/10 合同守卫误拒: %s", reason)
		}
	})

	t.Run("3/40 echoes reward guid", func(t *testing.T) {
		guid := "0123456789abcdef0123456789abcdef"
		tree := rigRequestTree(t, proto.Guid(guid), proto.Bool(true))
		body, ok := hDynamicRewardEntry(dynamicTestCtx(srv, tree))
		if !ok {
			t.Fatal("3/40 未处理")
		}
		dec, _, err := proto.DecodeValue(body, 0, srv.Table())
		if err != nil {
			t.Fatal(err)
		}
		if dec.Class != "enikeammfen" || dec.TypeID != 2015 {
			t.Fatalf("3/40 响应类型=%s/%d", dec.Class, dec.TypeID)
		}
		f2 := dec.Field(2)
		if got := f2.GuidHex(); got != guid {
			t.Fatalf("3/40 f2=%s, 期望回显 %s", got, guid)
		}
		if reason := srv.dispatch.checkRegisteredResponse(3, 40, body, srv.Table()); reason != "" {
			t.Fatalf("3/40 合同守卫误拒: %s", reason)
		}
	})

	t.Run("17/9 echoes broken component guid without DB", func(t *testing.T) {
		srv := NewServer(config.Game{}, logging.Discard(), nil)
		rodGUID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		brokenGUID := "fedcba9876543210fedcba9876543210"
		tree := rigRequestTree(t, proto.Guid(rodGUID), proto.Guid(brokenGUID), proto.U32(1), proto.Null())
		body, ok := hDynamicRodBreak(dynamicTestCtx(srv, tree))
		if !ok {
			t.Fatal("17/9 未处理")
		}
		dec, _, err := proto.DecodeValue(body, 0, srv.Table())
		if err != nil {
			t.Fatal(err)
		}
		if dec.Class != "ikmdmfoojfg" || dec.TypeID != 11028 {
			t.Fatalf("17/9 响应类型=%s/%d", dec.Class, dec.TypeID)
		}
		if u64Of(dec.Fields[0].Num) != 0 {
			t.Fatal("无数据库时兼容响应 f0 应为 false")
		}
		f1 := dec.Field(1)
		if got := f1.GuidHex(); got != brokenGUID {
			t.Fatalf("17/9 f1=%s, 期望回显 %s", got, brokenGUID)
		}
		if reason := srv.dispatch.checkRegisteredResponse(17, 9, body, srv.Table()); reason != "" {
			t.Fatalf("17/9 合同守卫误拒: %s", reason)
		}
	})

	t.Run("snapshot handlers return dynamic 800", func(t *testing.T) {
		for _, k := range []key2{{13, 6}, {17, 10}, {17, 11}} {
			// 17/10 剪线已改成自己落库的 handler，回包仍是同一种 800 容器快照。
			fn := hDynamicRigSnapshot
			if k == (key2{17, 10}) {
				fn = hRodCutLine
			}
			body, ok := fn(dynamicTestCtx(srv, rigRequestTree(t)))
			if !ok {
				t.Fatalf("%d/%d 未处理", k.main, k.sub)
			}
			dec, _, err := proto.DecodeValue(body, 0, srv.Table())
			if err != nil {
				t.Fatal(err)
			}
			if dec.Class != "dlbahppbdad" || dec.TypeID != 800 {
				t.Fatalf("%d/%d 响应=%s/%d", k.main, k.sub, dec.Class, dec.TypeID)
			}
			if reason := srv.dispatch.checkRegisteredResponse(k.main, k.sub, body, srv.Table()); reason != "" {
				t.Fatalf("%d/%d 合同守卫误拒: %s", k.main, k.sub, reason)
			}
		}
	})
}

func TestDynamicHanditemThrowConsumesLoadedStack(t *testing.T) {
	t.Parallel()
	srv, _ := newRigTestServer(t)
	registerDynamicProtocolHandlers(srv.dispatch)
	ctx := context.Background()
	const uid = int64(1000001)
	hand, err := srv.CreateItem(ctx, uid, "wh_pc_gb", "backpack", 1)
	if err != nil {
		t.Fatalf("创建手持物失败: %v", err)
	}
	bait, err := srv.CreateItem(ctx, uid, "worm_01", "rig", 1)
	if err != nil {
		t.Fatalf("创建装填物失败: %v", err)
	}
	srv.setStackCount(ctx, bait, 2)
	// 这段锁的是「f4 抛出物注入」这条链路本身，所以把开关显式打开。
	// 出厂缺省已改成 0：实机看客户端自己就会画这一发抛投（设计稿 §17.2），
	// 缺省不下发；缺省行为由本测试末尾那段守。
	setWeatherTestTunable(t, srv, "handitem_throw_projectile", "1")
	srv.setHanditemLoadedRef(ctx, hand.GUID, bait.GUID)

	target := wiregen.MustMakeObj("olieodeoaof", map[int]*proto.Value{0: proto.F32(17.5)})
	req := rigRequestTree(t, proto.Guid(hand.GUID), target)
	body, ok := hDynamicHanditemThrow(dynamicTestCtx(srv, req))
	if !ok {
		t.Fatal("13/5 第一次未处理")
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	if dec.Class != "lmbomkliaad" || dec.TypeID != 805 {
		t.Fatalf("13/5 响应=%s/%d", dec.Class, dec.TypeID)
	}
	f0 := dec.Field(0)
	gotDist := float64(proto.F32FromBits(f0.U32()))
	if math.Abs(gotDist-17.5) > 0.001 {
		t.Fatalf("13/5 f0=%f, 期望 17.5", gotDist)
	}
	scatterField := dec.Field(1)
	if gotScatter := float64(proto.F32FromBits(scatterField.U32())); math.Abs(gotScatter-5.0) > 0.001 {
		t.Fatalf("13/5 f1=%f, 期望缺省散布 5%%", gotScatter)
	}
	upserts := dec.Field(3).Val
	if upserts == nil || upserts.ArgCount() != 1 {
		t.Fatalf("13/5 f3 应更新装填物，实得 %#v", upserts)
	}
	fresh, err := srv.DB.GetItemByGUID(ctx, bait.GUID)
	if err != nil || fresh == nil {
		t.Fatalf("第一次抛投后装填物应剩 1 份: %v", err)
	}
	if left := srv.stackCountOf(ctx, fresh); left != 1 {
		t.Fatalf("第一次抛投后剩 %d 份，期望 1", left)
	}

	// f2 姿态块与 f4 抛出物：客户端凭 f2 写全局时间，凭 f4 的新实例生成飞行中的饵。
	if pose := dec.Field(2).Val; pose == nil || pose.Class != "pnbphpdmppe" {
		t.Fatalf("13/5 f2 应为姿态块 pnbphpdmppe，实得 %#v", pose)
	}
	proj := dec.Field(4).Val
	if proj == nil || proj.ArgCount() != 1 {
		t.Fatalf("13/5 f4 应下发 1 个抛出物，实得 %#v", proj)
	}
	pj := proj.ArgItem(0)
	gi := fieldIdxByCS(pj.Class, csGUID)
	if gi < 0 {
		t.Fatalf("抛出物类 %s 没有 GUID 槽", pj.Class)
	}
	pjID := pj.Field(gi)
	g := pjID.GuidHex()
	if g == "" || g == bait.GUID {
		t.Fatalf("抛出物必须是新实例 GUID，实得 %q（装填物 %s）", g, bait.GUID)
	}
	if qi := fieldIdxByCS(pj.Class, csQty); qi >= 0 {
		pjQty := pj.Field(qi)
		if got := pjQty.U32(); got != 1 {
			t.Fatalf("抛出物数量=%d，期望 1", got)
		}
	}
	if row, _ := srv.DB.GetItemByGUID(ctx, g); row != nil {
		t.Fatal("抛出物是一次性记录，不应落库")
	}

	body, ok = hDynamicHanditemThrow(dynamicTestCtx(srv, req))
	if !ok {
		t.Fatal("13/5 第二次未处理")
	}
	dec, _, err = proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	if removed := dec.Field(5).Val; removed == nil || removed.Kind != proto.KindGuidArr ||
		removed.ArgCount() != 1 || removed.ArgItem(0).GuidHex() != bait.GUID {
		t.Fatalf("第二次抛投 f5 应移除装填物 %s，实得 %#v", bait.GUID, removed)
	}
	if left, _ := srv.DB.GetItemByGUID(ctx, bait.GUID); left != nil {
		t.Fatal("装填物用尽后仍存在（被错误退回/未删除）")
	}
	if loaded := srv.handitemLoadedGuid(ctx, hand.GUID); loaded != "" {
		t.Fatalf("装填物用尽后引用未清空: %s", loaded)
	}
	if reason := srv.dispatch.checkRegisteredResponse(13, 5, body, srv.Table()); reason != "" {
		t.Fatalf("13/5 合同守卫误拒: %s", reason)
	}

	// 缺省（tunable=0）：不再下发抛出物实例，但扣料、姿态块、落点照旧。
	setWeatherTestTunable(t, srv, "handitem_throw_projectile", "0")
	bait2, err := srv.CreateItem(ctx, uid, "worm_01", "rig", 1)
	if err != nil {
		t.Fatalf("创建第二发装填物失败: %v", err)
	}
	srv.setStackCount(ctx, bait2, 2)
	srv.setHanditemLoadedRef(ctx, hand.GUID, bait2.GUID)
	body, ok = hDynamicHanditemThrow(dynamicTestCtx(srv, req))
	if !ok {
		t.Fatal("13/5 缺省关闭抛出物时未处理")
	}
	dec, _, err = proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	if proj := dec.Field(4).Val; proj == nil || proj.ArgCount() != 0 {
		t.Fatalf("缺省应不下发抛出物实例，实得 %#v", proj)
	}
	if pose := dec.Field(2).Val; pose == nil || pose.Class != "pnbphpdmppe" {
		t.Fatalf("关掉抛出物不该影响 f2 姿态块，实得 %#v", pose)
	}
	setWeatherTestTunable(t, srv, "handitem_throw_scatter", "0")
	body, ok = hDynamicHanditemThrow(dynamicTestCtx(srv, req))
	if !ok {
		t.Fatal("13/5 散布归零时未处理")
	}
	dec, _, err = proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	scatterField = dec.Field(1)
	if gotScatter := float64(proto.F32FromBits(scatterField.U32())); gotScatter != 0 {
		t.Fatalf("散布开关调 0 后 f1=%f，期望 0", gotScatter)
	}
	if fresh2, _ := srv.DB.GetItemByGUID(ctx, bait2.GUID); fresh2 != nil {
		t.Fatalf("第二发用尽后 bait2 应删除，实得 %#v", fresh2)
	}
	if loaded := srv.handitemLoadedGuid(ctx, hand.GUID); loaded != "" {
		t.Fatalf("两发用尽后装填引用应清空，实得 %s", loaded)
	}
}

// TestDynamicHanditemThrowDistanceForceOverridesClientDistance 锁 handitem_throw_dist_force。
// 实机证据：客户端 13/5 的 f0 恒为 10.0，力度和朝向都没进包（olieodeoaof.f10 位置块全零），
// 所以这一发的飞行距离只能由服务端下发的 805.f0 决定。
// 缺省 0=沿用客户端值；给正值时 f0 覆盖为服务端距离，扣料仍是每发 1 份。
func TestDynamicHanditemThrowDistanceForceOverridesClientDistance(t *testing.T) {
	t.Parallel()
	srv, _ := newRigTestServer(t)
	registerDynamicProtocolHandlers(srv.dispatch)
	ctx := context.Background()
	const uid = int64(1000001)
	hand, err := srv.CreateItem(ctx, uid, "wh_pc_gb", "backpack", 1)
	if err != nil {
		t.Fatalf("创建手持物失败: %v", err)
	}
	bait, err := srv.CreateItem(ctx, uid, "worm_01", "rig", 1)
	if err != nil {
		t.Fatalf("创建装填物失败: %v", err)
	}
	srv.setStackCount(ctx, bait, 2)
	srv.setHanditemLoadedRef(ctx, hand.GUID, bait.GUID)
	target := wiregen.MustMakeObj("olieodeoaof", map[int]*proto.Value{0: proto.F32(10)})
	req := rigRequestTree(t, proto.Guid(hand.GUID), target)

	body, ok := hDynamicHanditemThrow(dynamicTestCtx(srv, req))
	if !ok {
		t.Fatal("13/5 缺省距离未处理")
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	f0d := dec.Field(0)
	if got := float64(proto.F32FromBits(f0d.U32())); math.Abs(got-10) > 0.001 {
		t.Fatalf("缺省 f0=%f，期望沿用客户端上报的 10", got)
	}

	setWeatherTestTunable(t, srv, "handitem_throw_dist_force", "60")
	body, ok = hDynamicHanditemThrow(dynamicTestCtx(srv, req))
	if !ok {
		t.Fatal("13/5 服务端强制距离未处理")
	}
	dec, _, err = proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	f0f := dec.Field(0)
	if got := float64(proto.F32FromBits(f0f.U32())); math.Abs(got-60) > 0.001 {
		t.Fatalf("强制距离后 f0=%f，期望服务端下发的 60", got)
	}
	if row, _ := srv.DB.GetItemByGUID(ctx, bait.GUID); row != nil {
		t.Fatal("两发用尽后装填物应删除（覆盖距离不该影响扣料）")
	}
}

// TestDynamicHanditemThrowLedgerSurvivesCallIDReuse 锁弹弓窝账的幂等键。
// CallID 是客户端每次连接重新编号的：上一次登录打过 call=35，这一次登录第一次
// 投掷很可能又是 call=35。撞键会被幂等静默吞掉——饵照扣、窝一格没记
// （实机 13:10 三发 call=35/36/37 就是这样一笔都没进账）。
func TestDynamicHanditemThrowLedgerSurvivesCallIDReuse(t *testing.T) {
	t.Parallel()
	srv, _ := newRigTestServer(t)
	registerDynamicProtocolHandlers(srv.dispatch)
	setWeatherTestTunable(t, srv, "groundbait_enabled", "1")
	ctx := context.Background()
	const uid = int64(1000001)
	st := srv.stateFor(uid)
	st.mu.Lock()
	st.Loc = "gb-test-loc"
	st.mu.Unlock()
	hand, err := srv.CreateItem(ctx, uid, "wh_pc_gb", "backpack", 1)
	if err != nil {
		t.Fatalf("创建手持物失败: %v", err)
	}
	bait, err := srv.CreateItem(ctx, uid, "worm_01", "rig", 1)
	if err != nil {
		t.Fatalf("创建装填物失败: %v", err)
	}
	srv.setStackCount(ctx, bait, 2)
	srv.setHanditemLoadedRef(ctx, hand.GUID, bait.GUID)
	target := wiregen.MustMakeObj("olieodeoaof", map[int]*proto.Value{0: proto.F32(10)})
	req := rigRequestTree(t, proto.Guid(hand.GUID), target)

	// 同一台弹弓、同一个 CallID，但分属两次登录：两发都必须进窝账。
	for _, sessID := range []uint64{11, 12} {
		sess := newProbeSess(srv)
		sess.ID = sessID
		c := &Ctx{Sess: sess, Tree: req, DecodeOK: true, ExpectResponse: true,
			CallID: 35, Log: logging.Discard()}
		if _, ok := hDynamicHanditemThrow(c); !ok {
			t.Fatalf("会话 %d 的 13/5 未处理", sessID)
		}
	}
	if n := gbCount(t, srv, uid); n != 2 {
		t.Fatalf("两次登录同 CallID 应有 2 笔窝账，实得 %d（幂等键撞车）", n)
	}
	var keys []string
	rows, err := srv.DB.SQL.Query(
		`SELECT txn_guid FROM groundbait_deposit WHERE uid=? AND channel='catapult' ORDER BY txn_guid`, uid)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, v)
	}
	_ = rows.Close()
	if len(keys) == 2 && keys[0] == keys[1] {
		t.Fatalf("两发共用同一个幂等键: %v", keys)
	}
}

func TestDynamicEquippedItemsUsesRigRows(t *testing.T) {
	t.Parallel()
	srv, _ := newRigTestServer(t)
	registerDynamicProtocolHandlers(srv.dispatch)
	ctx := context.Background()
	item, err := srv.CreateItem(ctx, 1000001, "mono_15097", "rig", 1)
	if err != nil {
		t.Fatalf("创建钓线失败: %v", err)
	}
	body, ok := hDynamicEquippedItems(dynamicTestCtx(srv, rigRequestTree(t)))
	if !ok {
		t.Fatal("3/41 未处理")
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	if dec.Kind != proto.KindArgs || dec.ArgCodeStr() != "61" || dec.ArgCount() != 1 {
		t.Fatalf("3/41 应回 args(61)[1 件]，实得 %#v", dec)
	}
	got := dec.ArgItem(0)
	if got == nil || got.Kind != proto.KindObj || got.TypeID != uint32(item.TypeID) {
		t.Fatalf("3/41 物品树不符: %#v", got)
	}
	if reason := srv.dispatch.checkRegisteredResponse(3, 41, body, srv.Table()); reason != "" {
		t.Fatalf("3/41 合同守卫误拒: %s", reason)
	}
}

// TestDynamicHanditemThrowLandingOverridesPoseBlock 锁「落点写进 805.f2 前五个 float」。
// 客户端 13/5 不带朝向、olieodeoaof.f10 位置块整块全零，飞行方向只能由服务端下发；
// 同一个形状的类型 325 deggnlnohoh 的五个 float 实测就是 x/y/z/偏航/俯仰。
// 这里钉三件事：开开关后 f2 前三位是「玩家沿抛竿方向前进 dist 米」的坐标；
// 方向取玩家→上次抛竿落点；关掉开关原样退回体力姿态块。
func TestDynamicHanditemThrowLandingOverridesPoseBlock(t *testing.T) {
	t.Parallel()
	srv, _ := newRigTestServer(t)
	registerDynamicProtocolHandlers(srv.dispatch)
	ctx := context.Background()
	const uid = int64(1000001)
	hand, err := srv.CreateItem(ctx, uid, "wh_pc_gb", "backpack", 1)
	if err != nil {
		t.Fatalf("创建弹弓失败: %v", err)
	}
	bait, err := srv.CreateItem(ctx, uid, "worm_01", "rig", 1)
	if err != nil {
		t.Fatalf("创建窝料失败: %v", err)
	}
	srv.setStackCount(ctx, bait, 3)
	srv.setHanditemLoadedRef(ctx, hand.GUID, bait.GUID)

	st := srv.stateFor(uid)
	st.mu.Lock()
	st.PosX, st.PosZ, st.HasPos = 100, 100, true
	// 浮标在正 +Z 方向 60m 处：这一发应沿这个方向飞 10m，落点 (100, 110)。
	st.CastPosX, st.CastPosZ, st.HasCastPos = 100, 160, true
	st.mu.Unlock()

	req := rigRequestTree(t, proto.Guid(hand.GUID),
		wiregen.MustMakeObj("olieodeoaof", map[int]*proto.Value{0: proto.F32(10), 1: proto.F32(10)}))

	throwOnce := func() *proto.Value {
		t.Helper()
		body, ok := hDynamicHanditemThrow(dynamicTestCtx(srv, req))
		if !ok {
			t.Fatal("13/5 未处理")
		}
		dec, _, err := proto.DecodeValue(body, 0, srv.Table())
		if err != nil {
			t.Fatal(err)
		}
		if dec.Class != "lmbomkliaad" || dec.TypeID != 805 {
			t.Fatalf("13/5 响应=%s/%d", dec.Class, dec.TypeID)
		}
		pose := dec.Field(2).Val
		if pose == nil || pose.Class != "pnbphpdmppe" {
			t.Fatalf("13/5 f2 应为 float×5+long+int 块，实得 %#v", pose)
		}
		return pose
	}
	f32 := func(v *proto.Value, idx int) float64 {
		t.Helper()
		f := v.Field(idx)
		return float64(proto.F32FromBits(f.U32()))
	}

	setWeatherTestTunable(t, srv, "handitem_throw_pose_pos", "1")
	pose := throwOnce()
	if got := f32(pose, 0); math.Abs(got-100) > 0.01 {
		t.Fatalf("落点 x=%f，期望 100", got)
	}
	if got := f32(pose, 1); math.Abs(got) > 0.01 {
		t.Fatalf("落点 y=%f，期望水面 0", got)
	}
	if got := f32(pose, 2); math.Abs(got-110) > 0.01 {
		t.Fatalf("落点 z=%f，期望 100+10=110（朝 +Z 的浮标方向前进 10m）", got)
	}
	if got := f32(pose, 3); math.Abs(got) > 0.01 {
		t.Fatalf("偏航=%f 度，期望 +Z 方向 0 度", got)
	}
	// 后四位不动：体力常量与世界钟 FILETIME 保持姿态块原值。
	if got := f32(pose, 5); math.Abs(got-11.375) > 0.001 {
		t.Fatalf("f2 第 6 位=%f，期望沿用姿态块 11.375", got)
	}
	f7 := pose.Field(7)
	if f7.U64() == 0 {
		t.Fatal("f2 世界钟 FILETIME 不应为 0")
	}

	// 没抛过竿时用兜底偏航：fallback_yaw=90 → 落点往 +X 走。
	st.mu.Lock()
	st.CastPosX, st.CastPosZ, st.HasCastPos = 0, 0, false
	st.mu.Unlock()
	setWeatherTestTunable(t, srv, "handitem_throw_fallback_yaw", "90")
	pose = throwOnce()
	if got := f32(pose, 0); math.Abs(got-110) > 0.01 {
		t.Fatalf("兜底偏航 90 度时落点 x=%f，期望 110", got)
	}
	if got := f32(pose, 2); math.Abs(got-100) > 0.01 {
		t.Fatalf("兜底偏航 90 度时落点 z=%f，期望 100", got)
	}
	setWeatherTestTunable(t, srv, "handitem_throw_fallback_yaw", "0")

	// 关掉开关：f2 退回原样姿态块（前三位是体力，不是坐标）。
	setWeatherTestTunable(t, srv, "handitem_throw_pose_pos", "0")
	pose = throwOnce()
	if got := f32(pose, 0); math.Abs(got-100) < 0.01 {
		t.Fatalf("关掉 pose_pos 后 f2 首位仍是坐标 %f，期望体力值", got)
	}
}

// TestDynamicHanditemThrowLandingCell 锁 handitem_throw_land_cell：
// 开开关后窝账格子跟着真实落点走，不再是上次抛竿的落点格。
func TestDynamicHanditemThrowLandingCell(t *testing.T) {
	t.Parallel()
	srv, _ := newRigTestServer(t)
	registerDynamicProtocolHandlers(srv.dispatch)
	setWeatherTestTunable(t, srv, "groundbait_enabled", "1")
	setWeatherTestTunable(t, srv, "handitem_throw_land_cell", "1")
	ctx := context.Background()
	const uid = int64(1000001)
	hand, err := srv.CreateItem(ctx, uid, "wh_pc_gb", "backpack", 1)
	if err != nil {
		t.Fatalf("创建弹弓失败: %v", err)
	}
	bait, err := srv.CreateItem(ctx, uid, "feedcompleted", "rig", 1)
	if err != nil {
		t.Fatalf("创建窝料失败: %v", err)
	}
	srv.setStackCount(ctx, bait, 2)
	srv.setHanditemLoadedRef(ctx, hand.GUID, bait.GUID)

	st := srv.stateFor(uid)
	st.mu.Lock()
	st.Loc = "gb-test-loc"
	st.PosX, st.PosZ, st.HasPos = 100, 100, true
	st.CastPosX, st.CastPosZ, st.HasCastPos = 100, 600, true
	st.mu.Unlock()

	req := rigRequestTree(t, proto.Guid(hand.GUID),
		wiregen.MustMakeObj("olieodeoaof", map[int]*proto.Value{0: proto.F32(10), 1: proto.F32(10)}))
	if _, ok := hDynamicHanditemThrow(dynamicTestCtx(srv, req)); !ok {
		t.Fatal("13/5 未处理")
	}
	var cellX, cellZ int64
	if err := srv.DB.SQL.QueryRowContext(ctx,
		`SELECT bx,bz FROM groundbait_deposit WHERE uid=?`, uid).Scan(&cellX, &cellZ); err != nil {
		t.Fatalf("弹弓投放没落账: %v", err)
	}
	castCell := int64(600 / groundbaitCellSizeM)
	landCell := int64(110 / groundbaitCellSizeM)
	if cellZ == castCell {
		t.Fatalf("窝账仍记在抛竿落点格 %d,%d，land_cell 开关应该改掉它", cellX, cellZ)
	}
	if cellZ != landCell {
		t.Fatalf("窝账格 %d,%d，期望落点格 %d", cellX, cellZ, landCell)
	}
}
