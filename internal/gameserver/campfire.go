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

// defaultCampfireDurationMin 是游戏内分钟；24 倍速下 60 游戏分钟等于
// 150 秒现实时间。
const defaultCampfireDurationMin int64 = 60

// fireMaterialItemIDs 点火材料系统 ID：每次点燃消耗 1 木柴 + 1 火柴
// （客户端在发起点火前会按这两类材料做背包校验）。
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

// hCampfireStart 处理 3/11 player/campfire_start。
// 响应 802 的真实布局是：绝对结束游戏分钟、两个 onobdmohahp[] 物品数组、Guid[]。
// 官方语义：点燃消耗 1 木柴 + 1 火柴（客户端发起前会做材料拦截），
// f1/f2 携带扣减后的物品实例（结算基座消费）、f3 携带耗尽移除的 GUID。服务端另用 FILETIME 持久化真实到期点；
// 把 FILETIME 塞进 f1 会让客户端按物品数组解析并断线。
// 材料缺失或账务失败时对应槽位退化为空数组，不阻塞点火（客户端本地已有
// 材料拦截，正常流程不会走到）。
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

	duration := defaultCampfireDurationMin
	maxRealDuration := gameMinutesToRealDuration(duration)
	expiry := nowFT + maxRealDuration.Nanoseconds()/100
	if existing, err := srv.DB.GetCampfireState(cctx, ctx.Sess.Userid); err == nil &&
		existing != nil && existing.ExpiresFT > nowFT &&
		existing.ExpiresFT-nowFT <= maxRealDuration.Nanoseconds()/100+5*10_000_000 {
		duration = existing.DurationMin
		expiry = existing.ExpiresFT
	} else if err != nil {
		ctx.Log.Warn("读取篝火状态失败", "err", err)
	}

	state := store.CampfireState{
		UID: ctx.Sess.Userid, Loc: loc, Spot: spot,
		DurationMin: duration, ExpiresFT: expiry,
	}
	if err := srv.DB.UpsertCampfireState(cctx, state); err != nil {
		ctx.Log.Warn("保存篝火状态失败", "err", err)
	}

	remainingGameMinutes := float64(expiry-nowFT) / 10_000_000 / 60 * gameHoursPerRealHr
	// 到期分钟必须与 307.f13.f0 同时间线（worldTotalGameMin，世界总游戏分钟）：
	// 客户端以 307.f13.f0 校时，点燃门槛 `f0 > 客户端时钟` 按这条线比较。
	// sessionGameMinuteAt 是会话相对线（比世界总分钟小两千多万），用它永远
	// 过不了门槛（实机验证：会话线只走到门槛失败分支）。
	clientNowMinute := float64(worldTotalGameMin(now))
	clientExpiryMinute := uint32(math.Ceil(clientNowMinute + remainingGameMinutes))

	// 点火材料消耗：事务内只扣料并记录 GUID（ItemToTree 有嵌套写，绝不能在
	// 持锁事务内调用——会自死锁）；提交后在事务外建实例树。任一失败回滚并
	// 退化为空数组应答，不阻塞点火。
	type fireMaterialResult struct {
		guid    string
		removed bool
	}
	materialResults := make([]fireMaterialResult, 0, len(fireMaterialItemIDs))
	if tx, terr := beginEconomyTx(cctx, srv.DB); terr == nil {
		ok := true
		for _, itemID := range fireMaterialItemIDs {
			row, err := fireMaterialRowTx(cctx, tx, ctx.Sess.Userid, itemID)
			if err != nil {
				ok = false
				break
			}
			if row == nil {
				materialResults = append(materialResults, fireMaterialResult{})
				continue
			}
			remaining, err := consumeItemQtyTx(cctx, tx, row, 1)
			if err != nil {
				ok = false
				break
			}
			if remaining == 0 {
				materialResults = append(materialResults, fireMaterialResult{guid: row.GUID, removed: true})
				continue
			}
			materialResults = append(materialResults, fireMaterialResult{guid: row.GUID})
		}
		if !ok {
			_ = tx.Rollback()
			materialResults = nil
		} else if err := tx.Commit(); err != nil {
			ctx.Log.Warn("点火材料事务提交失败", "err", err)
			materialResults = nil
		}
	} else {
		ctx.Log.Warn("点火材料事务开启失败", "err", terr)
	}

	// 事务外构建扣减后的物品实例树（f1=木柴、f2=火柴，结算基座消费）。
	materialSlots := make([]*proto.Value, 0, len(fireMaterialItemIDs))
	var removedGUIDs, updatedGUIDs []string
	for _, res := range materialResults {
		switch {
		case res.removed:
			removedGUIDs = append(removedGUIDs, res.guid)
			materialSlots = append(materialSlots, nil)
		case res.guid == "":
			materialSlots = append(materialSlots, nil)
		default:
			if fresh, err := srv.DB.GetItemByGUID(cctx, res.guid); err == nil && fresh != nil {
				if tree, terr := srv.ItemToTree(cctx, fresh); terr == nil {
					updatedGUIDs = append(updatedGUIDs, res.guid)
					materialSlots = append(materialSlots, tree)
					continue
				}
			}
			materialSlots = append(materialSlots, nil)
		}
	}

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
		3: proto.GuidArr(removedGUIDs),
	})
	if err != nil {
		return nil, false
	}
	body, err := proto.EncodeValue(obj, ctx.Sess.Table())
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
		"client_now_min", math.Floor(clientNowMinute*10)/10, "client_expires_min", clientExpiryMinute,
		"expires_ft", expiry, "request_items", items, "expect_response", ctx.ExpectResponse)
	return body, true
}
