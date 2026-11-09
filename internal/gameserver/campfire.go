package gameserver

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

// tunableCampfireDurationGameMin 是燃烧时长的 tunable 键。单位是游戏内分钟；
// 世界钟 24 倍速，1 游戏日（1440 游戏分钟）= 1 现实小时。
const tunableCampfireDurationGameMin = "campfire_duration_game_min"

// defCampfireDurationGameMin 保持机制引入时的既有值，使本改动在默认配置下
// 行为逐字节不变；运维按需调长（如 480 游戏分钟 = 20 现实分钟）。
const defCampfireDurationGameMin int64 = 60

// 燃烧时长取值边界：至少 1 游戏分钟，至多 1 游戏日（1440 游戏分钟 = 1 现实小时），
// 避免误填（如把现实秒当游戏分钟）产生数日燃烧的异常到期值。
const (
	minCampfireDurationGameMin int64 = 1
	maxCampfireDurationGameMin int64 = 24 * 60
)

// campfireDurationGameMin 读取燃烧时长 tunable 并钳制到合法区间。
func (s *Server) campfireDurationGameMin() int64 {
	if s == nil {
		return defCampfireDurationGameMin
	}
	v := int64(math.Round(s.tunableF(tunableCampfireDurationGameMin, float64(defCampfireDurationGameMin))))
	if v < minCampfireDurationGameMin {
		return minCampfireDurationGameMin
	}
	if v > maxCampfireDurationGameMin {
		return maxCampfireDurationGameMin
	}
	return v
}

// fireMaterialItemIDs 点火材料系统 ID：每次点燃消耗 1 木柴 + 1 火柴
// （客户端在发起点火前会按这两类材料做背包校验，服务端仍必须自行校验）。
var fireMaterialItemIDs = []string{"woodstack", "matches"}

// fireMaterialRowTx 取背包内指定点火材料的一份堆叠（数量多的优先）。
func fireMaterialRowTx(ctx context.Context, tx *sql.Tx, uid int64, itemID string) (*store.Item, error) {
	var guid string
	err := tx.QueryRowContext(ctx, `
		SELECT i.guid FROM item i JOIN item_def d ON d.numeric_id = i.numeric_id
		WHERE i.uid=? AND d.item_id=? AND i.container='backpack'
		ORDER BY i.qty DESC LIMIT 1`, uid, itemID).Scan(&guid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return getItemTx(ctx, tx, guid)
}

// reusableCampfire 判断已有篝火是否仍在"自己那次点燃"的有效窗口内。
// 窗口上限取该记录自身的 duration_min，而不是当前 tunable 值：
// 运维调短燃烧时长后，正在燃烧的旧篝火若按新上限判定会掉出窗口，
// 导致玩家为一堆已经点着的火重复付材料。上限只用于拦异常的超长到期值。
func reusableCampfire(state *store.CampfireState, nowFT int64) bool {
	if state == nil || state.ExpiresFT <= nowFT {
		return false
	}
	maxDurationFT := gameMinutesToRealDuration(state.DurationMin).Nanoseconds() / 100
	return state.ExpiresFT-nowFT <= maxDurationFT+5*10_000_000
}

func campfireExpiryMinute(now time.Time, nowFT, expiry int64) uint32 {
	remainingGameMinutes := float64(expiry-nowFT) / 10_000_000 / 60 * gameHoursPerRealHr
	// 到期分钟必须与 307.f13.f0 同时间线（worldTotalGameMin）：客户端以
	// 307.f13.f0 校时，点燃门槛按这条线比较。
	clientNowMinute := float64(worldTotalGameMin(now))
	return uint32(math.Ceil(clientNowMinute + remainingGameMinutes))
}

func campfireResponseBody(ctx *Ctx, now time.Time, nowFT, expiry int64, materialSlots []*proto.Value, removed []string) ([]byte, error) {
	clientExpiryMinute := campfireExpiryMinute(now, nowFT, expiry)

	f1 := proto.Args(proto.S("61"))
	if len(materialSlots) > 0 && materialSlots[0] != nil {
		f1.Items = append(f1.Items, materialSlots[0])
	}
	f2 := proto.Args(proto.S("61"))
	if len(materialSlots) > 1 && materialSlots[1] != nil {
		f2.Items = append(f2.Items, materialSlots[1])
	}
	obj, err := wiregen.MakeObj("ihaaofcmopp", map[int]*proto.Value{
		0: proto.U32(clientExpiryMinute),
		1: f1,
		2: f2,
		3: proto.GuidArr(removed),
	})
	if err != nil {
		return nil, err
	}
	return proto.EncodeValue(obj, ctx.Sess.Table())
}

// hCampfireStart 处理 3/11 player/campfire_start。
//
// 官方语义：点燃消耗 1 木柴 + 1 火柴；应答 802 的 f1/f2 携带扣减后的物品实例，
// f3 携带耗尽移除的 GUID。服务端用 FILETIME 持久化真实到期点。
//
// 已有有效篝火只返回当前到期状态，不重复扣材料；新点火要求两种材料同时存在，
// 并把篝火状态与材料扣减放在同一事务中，避免免费点火或半扣材料。
func hCampfireStart(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	cctx := ctx.Sess.pushCtx()
	now := time.Now()
	nowFT := int64(proto.WindowsFiletimeNow())
	st := srv.stateFor(ctx.Sess.Userid)
	st.mu.Lock()
	loc, spot := st.Loc, int64(st.Spot)
	st.mu.Unlock()

	duration := srv.campfireDurationGameMin()
	maxDurationFT := gameMinutesToRealDuration(duration).Nanoseconds() / 100
	defaultExpiry := nowFT + maxDurationFT

	existing, err := srv.DB.GetCampfireState(cctx, ctx.Sess.Userid)
	if err != nil {
		ctx.Log.Warn("读取篝火状态失败", "err", err)
		ctx.ReplyFailure("System.InvalidOperationException", "篝火状态读取失败，请重试")
		return nil, true
	}
	if reusableCampfire(existing, nowFT) {
		body, err := campfireResponseBody(ctx, now, nowFT, existing.ExpiresFT, nil, nil)
		if err != nil {
			return nil, false
		}
		ctx.Log.Info("篝火仍在燃烧，复用到期状态", "loc", existing.Loc, "spot", existing.Spot,
			"expires_ft", existing.ExpiresFT, "duration_game_min", existing.DurationMin)
		return body, true
	}

	tx, err := beginEconomyTx(cctx, srv.DB)
	if err != nil {
		ctx.Log.Warn("点火材料事务开启失败", "err", err)
		ctx.ReplyFailure("System.InvalidOperationException", "点火未完成，请重试")
		return nil, true
	}
	defer tx.Rollback()

	// 事务内重新读一次，避免两个会话同时通过上面的读检查后重复扣料。
	existing, err = store.GetCampfireStateTx(cctx, tx, ctx.Sess.Userid)
	if err != nil {
		ctx.Log.Warn("事务内读取篝火状态失败", "err", err)
		ctx.ReplyFailure("System.InvalidOperationException", "点火未完成，请重试")
		return nil, true
	}
	if reusableCampfire(existing, nowFT) {
		_ = tx.Rollback()
		body, err := campfireResponseBody(ctx, now, nowFT, existing.ExpiresFT, nil, nil)
		if err != nil {
			return nil, false
		}
		return body, true
	}

	rows := make([]*store.Item, 0, len(fireMaterialItemIDs))
	for _, itemID := range fireMaterialItemIDs {
		row, err := fireMaterialRowTx(cctx, tx, ctx.Sess.Userid, itemID)
		if err != nil {
			ctx.Log.Warn("读取点火材料失败", "item_id", itemID, "err", err)
			ctx.ReplyFailure("System.InvalidOperationException", "点火未完成，请重试")
			return nil, true
		}
		if row == nil {
			ctx.Log.Info("3/11 点火被拒：缺少材料", "item_id", itemID)
			ctx.ReplyFailure("System.InvalidOperationException", "缺少木柴或火柴")
			return nil, true
		}
		rows = append(rows, row)
	}

	type fireMaterialResult struct {
		guid    string
		removed bool
	}
	results := make([]fireMaterialResult, 0, len(rows))
	for _, row := range rows {
		remaining, err := consumeItemQtyTx(cctx, tx, row, 1)
		if err != nil {
			ctx.Log.Warn("扣减点火材料失败", "item", row.ItemID, "err", err)
			ctx.ReplyFailure("System.InvalidOperationException", "点火未完成，请重试")
			return nil, true
		}
		results = append(results, fireMaterialResult{guid: row.GUID, removed: remaining == 0})
	}

	state := store.CampfireState{
		UID: ctx.Sess.Userid, Loc: loc, Spot: spot,
		DurationMin: duration, ExpiresFT: defaultExpiry,
	}
	if err := store.UpsertCampfireStateTx(cctx, tx, state); err != nil {
		ctx.Log.Warn("保存篝火状态失败", "err", err)
		ctx.ReplyFailure("System.InvalidOperationException", "点火未完成，请重试")
		return nil, true
	}
	if err := tx.Commit(); err != nil {
		ctx.Log.Warn("点火事务提交失败", "err", err)
		ctx.ReplyFailure("System.InvalidOperationException", "点火未完成，请重试")
		return nil, true
	}

	// 事务外构建扣减后的物品实例树（ItemToTree 有嵌套写，不能在持锁事务内调用）。
	materialSlots := make([]*proto.Value, 0, len(results))
	var removedGUIDs, updatedGUIDs []string
	for _, res := range results {
		if res.removed {
			removedGUIDs = append(removedGUIDs, res.guid)
			materialSlots = append(materialSlots, nil)
			continue
		}
		fresh, err := srv.DB.GetItemByGUID(cctx, res.guid)
		if err != nil || fresh == nil {
			materialSlots = append(materialSlots, nil)
			continue
		}
		tree, err := srv.ItemToTree(cctx, fresh)
		if err != nil {
			materialSlots = append(materialSlots, nil)
			continue
		}
		updatedGUIDs = append(updatedGUIDs, res.guid)
		materialSlots = append(materialSlots, tree)
	}

	body, err := campfireResponseBody(ctx, now, nowFT, defaultExpiry, materialSlots, removedGUIDs)
	if err != nil {
		return nil, false
	}
	if len(updatedGUIDs) > 0 || len(removedGUIDs) > 0 {
		change := baitChange{Updated: updatedGUIDs, Removed: removedGUIDs}
		ctx.AfterResponse(func() { ctx.Sess.pushBaitChange(change) })
	}

	items := make([]string, 0, 2)
	for _, guid := range deepGUIDs(ctx.Tree) {
		if row, ierr := srv.DB.GetItemByGUID(cctx, guid); ierr == nil && row != nil {
			items = append(items, row.ItemID)
		}
	}
	ctx.Log.Info("篝火点火", "loc", loc, "spot", spot, "duration_game_min", duration,
		"client_now_min", math.Floor(float64(worldTotalGameMin(now))*10)/10,
		"client_expires_min", campfireExpiryMinute(now, nowFT, defaultExpiry), "expires_ft", defaultExpiry,
		"request_items", items, "expect_response", ctx.ExpectResponse)
	return body, true
}
