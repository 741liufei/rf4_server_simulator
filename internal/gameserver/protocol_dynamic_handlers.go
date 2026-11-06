package gameserver

import (
	"context"
	"math"
	"strings"

	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

// registerDynamicProtocolHandlers 注册动态协议命令。
func registerDynamicProtocolHandlers(d *Dispatch) {
	d.Register(13, 5, hDynamicHanditemThrow)
	d.Register(13, 6, hDynamicRigSnapshot)
	d.Register(17, 9, hDynamicRodBreak)
	d.Register(17, 10, hRodCutLine)
	d.Register(17, 11, hDynamicRigSnapshot)
	d.Register(3, 40, hDynamicRewardEntry)
	d.Register(3, 41, hDynamicEquippedItems)
	d.Register(1, 10, hDynamicSonar)

	objRule := func(typeID uint32, class string) ResponseContractRule {
		return ResponseContractRule{Kind: proto.KindObj, TypeIDs: []uint32{typeID}, Classes: []string{class}}
	}
	d.RegisterResponseContract(13, 5, objRule(805, "lmbomkliaad"))
	d.RegisterResponseContract(13, 6, objRule(800, "dlbahppbdad"))
	d.RegisterResponseContract(17, 9, objRule(11028, "ikmdmfoojfg"))
	d.RegisterResponseContract(17, 10, objRule(800, "dlbahppbdad"))
	d.RegisterResponseContract(17, 11, objRule(800, "dlbahppbdad"))
	d.RegisterResponseContract(3, 40, objRule(2015, "enikeammfen"))
	d.RegisterResponseContract(3, 41, ResponseContractRule{
		Kind: proto.KindArgs, ArgCode: "61", ArgItemsAreItems: true,
	})
	d.RegisterResponseContract(1, 10, objRule(15003, "efnipphkbce"))
}

// hDynamicHanditemThrow 13/5 handitem/throw。
// 请求 [手持物 Guid, olieodeoaof 投掷目标]，响应 805 lmbomkliaad。
// 距离从目标 f0 的 f32 位模式读取；实际抛出时消耗 1 份已装填物。
func hDynamicHanditemThrow(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	dist := throwDistance(ctx.Tree)
	if math.IsNaN(dist) || math.IsInf(dist, 0) {
		dist = 20
	}
	if dist <= 0 {
		dist = 0
	} else {
		dist = math.Max(1, math.Min(150, dist))
	}
	handGUID := ""
	for _, g := range deepGUIDs(ctx.Tree) {
		handGUID = normalizeDynamicGUID(g)
		if handGUID != "" && handGUID != zeroGUID {
			break
		}
	}
	var upserts []*proto.Value
	var removed []string
	if dist > 0 && handGUID != "" && handGUID != zeroGUID {
		got := srv.consumeHanditemLoadedForThrow(ctx.Sess.pushCtx(), handGUID)
		upserts = got.upserts
		removed = got.removed
	}
	// f2 是姿态块 pmdcpeegbbd：客户端用它写全局时间与玩家生物特征；
	// f4 是本次抛出的物件实例数组，客户端读到新实例才会生成飞行中的饵。
	// 两个字段各由一个 tunable 控制，便于实机单变量验证（缺省都开）。
	withPose := srv.tunableF("handitem_throw_pose", 1) != 0
	withProj := srv.tunableF("handitem_throw_projectile", 1) != 0
	var pose *proto.Value
	if withPose {
		pose = srv.buildPose308(ctx.Sess.pushCtx(), ctx.Sess.Userid)
	} else {
		pose = proto.Null()
	}
	thrown := proto.Args(proto.S("61"))
	thrownGUID := ""
	if pj := handitemThrowProjectile(upserts, handGUID); withProj && pj != nil {
		thrown = proto.Args(proto.S("61"), pj)
		thrownGUID = throwProjectileGUID(pj)
	}
	// 一次性自清：把抛出物 GUID 同时放进 f5 移除列表，客户端建好飞行体后
	// 这条临时实例就不会留在弹弓弹药格里（实测缺省 0=不启用）。
	if thrownGUID != "" && srv.tunableF("handitem_throw_selfclean", 0) != 0 {
		removed = append(removed, thrownGUID)
	}
	ctx.Log.Info("805 落点与抛出物", "hand", shortGUID(handGUID), "dist_m", dist,
		"upserts", len(upserts), "removed", len(removed), "pose", withPose, "projectile", thrown.ArgCount())
	obj, err := wiregen.MakeObj("lmbomkliaad", map[int]*proto.Value{
		0: proto.F32(float32(dist)),
		1: proto.F32(5.0),
		2: pose,
		3: proto.Args(proto.S("61"), upserts...),
		4: thrown,
		5: proto.GuidArr(removed),
	})
	if err != nil {
		return nil, false
	}
	body, err := proto.EncodeValue(obj, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// hDynamicRigSnapshot 为 13/6、17/11 返回当前容器状态的 800 全量快照。
func hDynamicRigSnapshot(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	body := srv.rigFullSnapshotBody(ctx)
	if len(body) == 0 || body[0] != 0x01 {
		return nil, false
	}
	return body, true
}

// hDynamicRodBreak 17/9 rod/break：按断件 GUID 持久化断线/断竿并回 11028。
// 请求参数顺序为 [竿 GUID, 断件 GUID, 物理模型]；没有断件时回退到当前竿。
func hDynamicRodBreak(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	rodGUID := normalizeDynamicGUID(ctx.Tree.ArgItem(0).GuidHex())
	brokenGUID := normalizeDynamicGUID(ctx.Tree.ArgItem(1).GuidHex())
	if brokenGUID == zeroGUID {
		brokenGUID = ""
	}
	st := srv.fishingStateForRequest(ctx)
	if rodGUID == "" || rodGUID == zeroGUID {
		st.mu.Lock()
		rodGUID = normalizeDynamicGUID(st.GearGuid)
		st.mu.Unlock()
	}
	if rodGUID == "" {
		rodGUID = zeroGUID
	}

	// 已结算、等待 14/5 入护 / 14/6 放生的渔获：断件上报迟于落网（同一
	// realtime 连接 TCP 有序，14/4 之后到达的只能是迟到/重复帧）。此刻
	// 不再动装备，也不清鱼，否则 XP 已入账而渔获无处安放（幽灵鱼）；
	// 回零断件结果保持 11028 形状，搏鱼中（CastActive=true）不受影响。
	st.mu.Lock()
	settledPending := completedCatchPending(st)
	st.mu.Unlock()
	if settledPending {
		ctx.Log.Info("17/9 已结算渔获等待处理，忽略断件上报",
			"rod", shortGUID(rodGUID), "part", shortGUID(brokenGUID))
		echo := brokenGUID
		if echo == "" {
			echo = rodGUID
		}
		obj, berr := buildGearBreakResult(&gearBreakOutcome{BrokenGUID: echo})
		if berr != nil {
			return nil, false
		}
		body, berr := proto.EncodeValue(obj, ctx.Sess.Table())
		if berr != nil {
			return nil, false
		}
		return body, true
	}

	if srv.DB != nil && rodGUID != zeroGUID && ctx.DecodeOK {
		outcome, err := srv.breakLineOrRod(ctx.Sess.pushCtx(), ctx.Sess.Userid, rodGUID, brokenGUID)
		if err == nil {
			st.mu.Lock()
			if st.biteTimer != nil {
				st.biteTimer.Stop()
			}
			pending := st.Pending
			st.Pending = nil
			st.CastActive = false
			st.Hooked = false
			st.Resolved = true
			if !outcome.LineBroken && !outcome.ReelDamaged && !outcome.ComponentBroken {
				st.GearGuid = ""
			}
			st.mu.Unlock()
			if outcome.ReelDamaged {
				// 轮子只是降一个耐久阶段，仍留在竿上；本次鱼按超载跑鱼处理。
				srv.bumpFishingCounter(ctx.Sess.Userid, "escapes")
			} else if outcome.LineBroken {
				srv.bumpFishingCounter(ctx.Sess.Userid, "line_breaks")
			} else if outcome.ComponentBroken {
				// 钩/拟饵等终端件断裂：竿仍在手上，钓组摘除损坏件。
				srv.bumpFishingCounter(ctx.Sess.Userid, "component_breaks")
			} else {
				srv.bumpFishingCounter(ctx.Sess.Userid, "rod_breaks")
			}
			obj, berr := buildGearBreakResult(outcome)
			if berr != nil {
				return nil, false
			}
			body, berr := proto.EncodeValue(obj, ctx.Sess.Table())
			if berr != nil {
				return nil, false
			}
			ctx.Log.Info("17/9 渔具超载", "rod", shortGUID(rodGUID), "part", outcome.BreakPart,
				"broken", shortGUID(outcome.BrokenGUID), "line", outcome.LineBroken,
				"comp", outcome.ComponentBroken, "reel_broken", outcome.ReelBroken,
				"line_loss_m", outcome.LineLossM, "removed", len(outcome.RemovedGUIDs))
			// 断件会改变竿/轮的耐久与装配状态，需要全量同步刷新客户端物品模型。
			// 断竿时配件只是摘回背包，11028 的移除清单会让客户端先删掉本地模型，
			// 所以刷新必须排在回包之后，否则配件要到重登才重新出现。
			// 轮子只掉耐久、不进移除清单：不把 BrokenGUID 一并推过去的话，
			// 客户端缓存停在旧耐久，玩家看不到"损坏"。
			syncGUIDs := append([]string(nil), outcome.RemovedGUIDs...)
			// 拆分子饵并回来源堆时，来源堆数量变了但客户端没有收到 4/22，
			// 需要和移除件一起刷新，否则背包仍显示旧数量。
			syncGUIDs = append(syncGUIDs, outcome.StackRefreshGUIDs...)
			if g := strings.TrimSpace(outcome.BrokenGUID); g != "" {
				syncGUIDs = append(syncGUIDs, g)
			}
			reelBroken := outcome.ReelBroken
			frictionWorn := outcome.FrictionWorn
			ctx.AfterResponse(func() {
				ctx.Sess.Push4_22Items(syncGUIDs)
				switch {
				case reelBroken:
					// 轮子本体耐久归零，整体损坏。
					ctx.Sess.pushSystemChat("渔线轮已损坏，请到工坊修理后再使用")
				case frictionWorn:
					// 摩擦片磨穿、泄力失效，本体没坏，换摩擦片即可。
					ctx.Sess.pushSystemChat("摩擦片磨穿、泄力失效，请到工坊更换摩擦片")
				}
			})
			// 断件必丢鱼：11028 只表达装备损坏，另经聊天频道提示跑鱼。
			reason := "断竿"
			switch {
			case outcome.ComponentBroken:
				reason = "钓组组件断裂"
			case outcome.ReelBroken:
				// 轮子本体磨穿，真坏了。
				reason = "渔线轮超载"
			case outcome.FrictionWorn:
				// 摩擦片磨穿、泄力失效：鱼借线跑了，轮子本体没坏。
				reason = "泄力失效"
			case outcome.ReelDamaged:
				// 泄力打滑：鱼借线跑了，摩擦片只是轻微磨损。
				reason = "泄力打滑"
			case outcome.LineBroken && outcome.BreakPart == "leader":
				reason = "断子线"
			case outcome.LineBroken:
				reason = "断主线"
			}
			srv.notifyFishEscape(ctx.Sess, pending, reason)
			return body, true
		}
		ctx.Log.Warn("17/9 断件处理失败", "rod", shortGUID(rodGUID), "err", err)
	}
	if srv.DB != nil {
		ctx.ReplyFailure("System.InvalidOperationException", "断件处理失败，请重新取竿后重试")
		return nil, true
	}

	// 无存储时保留原有兼容响应。
	fallbackGUID := brokenGUID
	if fallbackGUID == "" {
		fallbackGUID = rodGUID
	}
	obj, err := wiregen.MakeObj("ikmdmfoojfg", map[int]*proto.Value{
		0: proto.U32(0),
		1: proto.Guid(fallbackGUID),
		2: proto.Args(proto.S("61")),
		3: proto.Null(),
		4: proto.Null(),
	})
	if err != nil {
		return nil, false
	}
	body, err := proto.EncodeValue(obj, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// hRodCutLine 17/10 rod/cutline：玩家按 X 主动剪线。
// 请求参数为 [竿 GUID, 本次放出的线长（米）]，响应 800 容器全量快照。
// 语义：放出多少米就从竿上主线剩余长度里扣多少米，主线以下的前导、子线、
// 钩、饵、拟饵、浮漂全部随线丢失；主线扣到 0 时整条消失。
// 客户端只负责播剪线动画，物品模型仍以服务端为准。800 快照的三段语义是
// f0 新增物品 / f1 更新物品 / f2 移除 GUID（见 buildRigSnapshot800），剪线没有新物品，
// 所以竿和主线走 f1、剪掉的钩组走 f2。不能把整个背包塞进 f0：那会让客户端把
// 已有物品当新物品重新入库，表现为剪掉的配件又冒出来、甚至落进背包。
// 回包之后再补 4/21 删除和 4/22 竿线刷新，保证搏鱼中的模型也同步。
func hRodCutLine(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	snapshot := func() ([]byte, bool) {
		body := srv.rigFullSnapshotBody(ctx)
		if len(body) == 0 || body[0] != 0x01 {
			return nil, false
		}
		return body, true
	}
	rodGUID, cutM := parseRodGuidInt(ctx.Tree)
	rodGUID = normalizeDynamicGUID(rodGUID)
	if cutM < 0 {
		cutM = 0
	}
	st := srv.fishingStateForRequest(ctx)
	if rodGUID == "" || rodGUID == zeroGUID {
		st.mu.Lock()
		rodGUID = normalizeDynamicGUID(st.GearGuid)
		st.mu.Unlock()
	}
	st.mu.Lock()
	inWater := st.CastActive || st.InWater
	pending := st.Pending
	st.mu.Unlock()
	if rodGUID == "" || rodGUID == zeroGUID {
		ctx.Log.Warn("17/10 剪线被拒：没有在手的鱼竿", "cut_m", cutM)
		return snapshot()
	}
	// 没有放线时不接受剪线：客户端误触也不该把整套钩组清空。
	if cutM <= 0 && !inWater {
		ctx.Log.Info("17/10 剪线被拒：当前没有放线", "rod", shortGUID(rodGUID))
		return snapshot()
	}
	outcome, err := srv.cutLineTx(ctx.Sess.pushCtx(), ctx.Sess.Userid, rodGUID, cutM)
	if err != nil {
		ctx.Log.Warn("17/10 剪线失败", "rod", shortGUID(rodGUID), "cut_m", cutM, "err", err)
		return snapshot()
	}
	// 线已剪断，本次抛投到此结束；线上还挂着鱼就算跑鱼。
	st.mu.Lock()
	if st.biteTimer != nil {
		st.biteTimer.Stop()
	}
	st.Pending = nil
	st.CastActive = false
	st.Hooked = false
	st.Resolved = true
	st.InWater = false
	st.NeedBite = false
	st.mu.Unlock()
	if pending != nil {
		srv.bumpFishingCounter(ctx.Sess.Userid, "escapes")
		srv.notifyFishEscape(ctx.Sess, pending, "剪线跑鱼")
	}
	ctx.Log.Info("17/10 剪线", "rod", shortGUID(rodGUID), "cut_m", cutM,
		"line_loss_m", outcome.LineLossM, "line_left", srv.rodLineLengthLeft(ctx.Sess.pushCtx(), rodGUID),
		"removed", len(outcome.RemovedGUIDs))
	removed := append([]string(nil), outcome.RemovedGUIDs...)
	stackRefresh := append([]string(nil), outcome.StackRefreshGUIDs...)
	ctx.AfterResponse(func() {
		if len(removed) > 0 {
			ctx.Sess.Push4_21(removed)
		}
		if len(stackRefresh) > 0 {
			// 与正常吃饵一致：4/22 只推变更的背包堆，不能夹带全套竿线。
			ctx.Sess.Push4_22ItemGUIDs(stackRefresh)
		} else {
			ctx.Sess.Push4_22Full()
		}
	})
	body, err := proto.EncodeValue(buildRigSnapshot800(nil, outcome.UpdatedTrees, removed), ctx.Sess.Table())
	if err != nil || len(body) == 0 || body[0] != 0x01 {
		return snapshot()
	}
	return body, true
}

// hDynamicRewardEntry 处理 3/40，返回 2015 奖励条目并回显请求中的 guid。
// 没有奖励持久化表时保留空物品 ID，不生成不存在的 systemID。
func hDynamicRewardEntry(ctx *Ctx) ([]byte, bool) {
	guid := zeroGUID
	for _, g := range deepGUIDs(ctx.Tree) {
		if n := normalizeDynamicGUID(g); n != "" {
			guid = n
			break
		}
	}
	obj, err := wiregen.MakeObj("enikeammfen", map[int]*proto.Value{
		0:  proto.U32(0),
		1:  proto.U32(1),
		2:  proto.Guid(guid),
		3:  proto.Guid(""),
		4:  proto.U32(10),
		5:  proto.U32(0),
		6:  proto.U32(0),
		7:  proto.U32(0),
		8:  proto.U64(0),
		9:  proto.U64(0),
		10: proto.U32(0),
		11: proto.Null(),
		12: proto.Null(),
		13: proto.U32(0),
		14: proto.U32(0),
	})
	if err != nil {
		return nil, false
	}
	body, err := proto.EncodeValue(obj, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// hDynamicEquippedItems 3/41：args("61")[container=rig 的 DB 物品树]。
func hDynamicEquippedItems(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	cctx := ctx.Sess.pushCtx()
	rows, err := srv.DB.ListItemsByContainer(cctx, ctx.Sess.Userid, "rig")
	if err != nil {
		return nil, false
	}
	out := proto.Args(proto.S("61"))
	for _, row := range rows {
		tree, terr := srv.ItemToTree(cctx, row)
		if terr != nil {
			continue
		}
		out.Items = append(out.Items, tree)
	}
	body, err := proto.EncodeValue(out, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// hDynamicSonar 处理 1/10，返回 15003 声呐响应并以请求水域名作为 f0。
// 已配置水域生成 15004/15005 回声；未知或空水域返回空网格。
func hDynamicSonar(ctx *Ctx) ([]byte, bool) {
	key := argFirstStr(ctx.Tree)
	obj, err := ctx.Sess.Srv.sonarResponse(ctx.Sess.pushCtx(), key)
	if err != nil {
		return nil, false
	}
	body, err := proto.EncodeValue(obj, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

type handitemThrowConsume struct {
	upserts []*proto.Value
	removed []string
}

// consumeHanditemLoadedForThrow 消耗一份手持工具中的已装填物：
// 只扣 1 份，未用尽更新子实例；用尽删除子实例并清手持物装填引用。
// 弹弓只装 1 颗模式下（handitem_load_one=1），用尽后自动从背包补 1 颗同种饵，
// 保持弹药格始终 1 颗 ready，可连抛；背包无来源堆时不补（弹药格空）。
func (s *Server) consumeHanditemLoadedForThrow(ctx context.Context, parentGUID string) handitemThrowConsume {
	var out handitemThrowConsume
	loaded := s.handitemLoadedGuid(ctx, parentGUID)
	if loaded == "" || loaded == zeroGUID {
		return out
	}
	row, err := s.DB.GetItemByGUID(ctx, loaded)
	if err != nil || row == nil {
		s.setHanditemLoadedRef(ctx, parentGUID, "")
		if parent, perr := s.DB.GetItemByGUID(ctx, parentGUID); perr == nil && parent != nil {
			if tree, terr := s.itemTree(ctx, parent); terr == nil {
				out.upserts = append(out.upserts, tree)
			}
		}
		return out
	}
	itemID, uid := row.ItemID, row.UID // 扣料前记下，供用尽后按同种饵补填
	if s.consumeItemQty(ctx, row, 1) < 1 {
		return out
	}
	left, err := s.DB.GetItemByGUID(ctx, loaded)
	if err == nil && left != nil {
		if tree, terr := s.itemTree(ctx, left); terr == nil {
			out.upserts = append(out.upserts, tree)
		}
		return out
	}
	// 用完：清引用，被消耗的子实例 guid 进 f5。
	s.setHanditemLoadedRef(ctx, parentGUID, "")
	out.removed = append(out.removed, loaded)
	// 自动补 1 颗：新子实例进 f3（客户端据此重建皮兜里的 ready 球），
	// res.UpdatedTrees 已含装好新引用的手持物与减 1 后的背包来源堆。
	if res := s.reloadHanditemAfterThrow(ctx, uid, parentGUID, itemID); res != nil {
		out.upserts = append(out.upserts, res.AddedTrees...)
		out.upserts = append(out.upserts, res.UpdatedTrees...)
		out.removed = append(out.removed, res.RemovedGUIDs...)
		return out
	}
	// 无来源堆可补：弹药格空，回更新后的手持物（引用已清）。
	if parent, perr := s.DB.GetItemByGUID(ctx, parentGUID); perr == nil && parent != nil {
		if tree, terr := s.itemTree(ctx, parent); terr == nil {
			out.upserts = append(out.upserts, tree)
		}
	}
	return out
}

// reloadHanditemAfterThrow 抛完一颗后从背包补 1 颗同种饵到手持工具，保持弹药格
// 始终 1 颗 ready。仅在 handitem_load_one 开启且弹药格已空时生效；按 item_id 找
// 背包里最早的同种来源堆，复用 rigSetComponent 整套装填 wiring（写装填引用、
// 派生索引、快照素材）。找不到可补的来源堆时返回 nil。
func (s *Server) reloadHanditemAfterThrow(ctx context.Context, uid int64, parentGUID, itemID string) *rigSetResult {
	if s.DB == nil || uid == 0 || parentGUID == "" || parentGUID == zeroGUID || itemID == "" {
		return nil
	}
	if s.tunableF("handitem_load_one", 1) == 0 {
		return nil // 整堆模式不自动补填
	}
	if loaded := s.handitemLoadedGuid(ctx, parentGUID); loaded != "" && loaded != zeroGUID {
		if row, _ := s.DB.GetItemByGUID(ctx, loaded); row != nil {
			return nil // 弹药格还有料，不补
		}
	}
	rows, err := s.DB.SQL.QueryContext(ctx,
		`SELECT guid FROM item WHERE uid=? AND item_id=? AND container='backpack' AND qty>0 ORDER BY created_ts,guid`,
		uid, itemID)
	if err != nil {
		return nil
	}
	var candidates []string
	for rows.Next() {
		var g string
		if serr := rows.Scan(&g); serr == nil {
			candidates = append(candidates, g)
		}
	}
	rows.Close()
	for _, g := range candidates {
		row, rerr := s.DB.GetItemByGUID(ctx, g)
		if rerr != nil || row == nil || s.stackCountOf(ctx, row) < 1 {
			continue
		}
		res := s.rigSetComponent(ctx, uid, parentGUID, 0, g, 0,
			rigSetOpts{ParentType: familyHanditem, EnforceCategory: true, HanditemFullStack: true})
		if res.Rejected || res.MountedGUID == "" {
			continue
		}
		s.log.Info("弹弓抛后自动补填", "parent", shortGUID(parentGUID), "item", itemID,
			"src", shortGUID(g), "child", shortGUID(res.MountedGUID))
		return res
	}
	return nil
}

// throwProjectileGUID 读出抛出物实例的 GUID，供 f5 自清使用。
func throwProjectileGUID(pj *proto.Value) string {
	if pj == nil {
		return ""
	}
	i := fieldIdxByCS(pj.Class, csGUID)
	if i < 0 || i >= len(pj.Fields) {
		return ""
	}
	f := pj.Field(i)
	return f.GuidHex()
}

// handitemThrowProjectile 复制一份「这一发抛出去的饵」：沿用装填堆的实例记录，
// 换成新 GUID、数量 1、父件为手持工具本身。805.f4 是物品实例数组，客户端读到
// 新实例才会生成飞行物。这条记录不落库，紧随其后的 13/6 全量快照会把它清掉，
// 不会留在背包里。
func handitemThrowProjectile(upserts []*proto.Value, parentGUID string) *proto.Value {
	var src *proto.Value
	for _, u := range upserts {
		if u != nil && u.Kind == proto.KindObj {
			src = u
			break
		}
	}
	if src == nil {
		return nil
	}
	out := *src
	out.Fields = append([]proto.Field(nil), src.Fields...)
	set := func(csName, op string, apply func(*proto.Field)) {
		i := fieldIdxByCS(src.Class, csName)
		if i < 0 || i >= len(out.Fields) {
			return
		}
		if out.Fields[i].Op == "" {
			out.Fields[i].Op = op
		}
		apply(&out.Fields[i])
	}
	set(csGUID, "GUID16", func(f *proto.Field) { f.Hex = NewItemGUID() })
	set(csParent, "OPTGUID", func(f *proto.Field) { f.Hex = parentGUID })
	set(csQty, "U32", func(f *proto.Field) { f.Num = ptrU64(1) })
	set(csCreated, "U64", func(f *proto.Field) { f.Num = ptrU64(uint64(netTicksNow())) })
	return &out
}

// throwDistance 从 olieodeoaof.f0 读取投掷距离。
// 字段缺失时使用 20m；值不大于 0 时返回 0，客户端会跳过落点创建。
func throwDistance(root *proto.Value) float64 {
	var target *proto.Value
	var walk func(*proto.Value)
	walk = func(v *proto.Value) {
		if v == nil || target != nil {
			return
		}
		if v.Kind == proto.KindObj {
			if v.Class == "olieodeoaof" {
				target = v
				return
			}
			for i := range v.Fields {
				walk(v.Fields[i].Val)
			}
			return
		}
		for _, it := range v.Items {
			walk(it)
		}
	}
	walk(root)
	if target == nil || target.FieldCount() == 0 || target.Field(0).Num == nil {
		return 20
	}
	bits := uint32(u64Of(target.Field(0).Num))
	return float64(math.Float32frombits(bits))
}

func normalizeDynamicGUID(g string) string {
	g = strings.ToLower(strings.ReplaceAll(g, "-", ""))
	if len(g) == 32 && g != zeroGUID {
		return g
	}
	if g == zeroGUID {
		return zeroGUID
	}
	return ""
}
