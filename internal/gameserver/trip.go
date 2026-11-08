package gameserver

// 钓鱼游计时（3/38 购买，见 world.go hPlaceTripTx）。
//
// 计时规则（2026-11-06 反编译与实机复核）：每次购买 5 现实小时，**购买
// 即启表**；购买后只要会话在线就消耗，离线暂停，在线不论身处哪张地图都
// 计时。startTripTimers 仅用于兼容 last_consume_ts=0 的历史存档，在进入
// 地图时补启表；新购票在 3/38 事务中直接写入 last_consume_ts=now。之后由
// keepalive tick（session.go startKeepalive）每拍调用 consumeTripTimes 扣减，
// 会话关闭时再消费一次即停表。

import (
	"context"
	"time"
)

// tripLevelLocked 钓鱼游地图在无有效访问时不能作为出生/进入地图。
// 有效访问 = 买断（buyout）、有效钓鱼游（trip）或该地图的房屋（house，
// place 为 level_key 或 level_key+"_house"，房屋视为地图的长期访问）。
// 有效钓鱼游 = place_access(kind='trip') 行存在且 trip_state.remaining_sec>0：
// 到期流程曾出现状态已清零而 access 删除失败的残留行（补偿分支见
// consumeTripTimes），仅凭 access 行会让已到期玩家继续进图。
// 不在票价表里的地图（非钓鱼游地图）恒为 false。
func (s *Server) tripLevelLocked(ctx context.Context, uid int64, level string) bool {
	if s == nil || s.DB == nil {
		return false
	}
	if _, ok := placeTripPrices[level]; !ok {
		return false
	}
	var n int
	if err := s.DB.SQL.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM place_access a
		JOIN trip_state t ON t.uid=a.uid AND t.place=a.place
		WHERE a.uid=? AND a.place=? AND a.kind='trip' AND t.remaining_sec>0`,
		uid, level).Scan(&n); err != nil {
		return false
	}
	if n > 0 {
		return false
	}
	if err := s.DB.SQL.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM place_access
		WHERE uid=? AND place=? AND kind='buyout'`,
		uid, level).Scan(&n); err != nil {
		return false
	}
	if n > 0 {
		return false
	}
	if err := s.DB.SQL.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM place_access
		WHERE uid=? AND place IN (?,?) AND kind='house'`,
		uid, level, level+"_house").Scan(&n); err != nil {
		return false
	}
	return n == 0
}

// loginRestoreTripExpired 判断登录恢复的地图是否只依赖已经到期的钓鱼游。
//
// 银币门票按次进入但停留期间没有 place_access 记录；玩家下线重登时位置
// 本身就是仍在图内的凭据，不能直接用 tripLevelLocked 把空请求退回出生图。
// 只有该地点存在已到期购票记录，且到期后没有新的 place_travel 银币票流水
// 时，才视为“仅钓鱼游资格已到期”。
func (s *Server) loginRestoreTripExpired(ctx context.Context, uid int64, level string) bool {
	if !s.tripLevelLocked(ctx, uid, level) {
		return false
	}
	var n int
	err := s.DB.SQL.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM trip_state t
		WHERE t.uid=? AND t.place=? AND t.purchases>0 AND t.remaining_sec<=0
		  AND NOT EXISTS (
			SELECT 1 FROM txn x
			WHERE x.uid=t.uid AND x.item_id=t.place
			  AND x.note='place_travel' AND x.ts>=t.updated_ts
		  )`, uid, level).Scan(&n)
	return err == nil && n > 0
}

// startTripTimers 兼容旧存档：只把 last_consume_ts=0 的历史已购行置为当前
// 时刻。新购票已经在 3/38 事务中启表；已启表或未购票的行不动。
func (s *Server) startTripTimers(ctx context.Context, uid int64, level string) bool {
	if s == nil || s.DB == nil {
		return false
	}
	st, err := s.DB.GetTripState(ctx, uid, level)
	if err != nil || st == nil || st.LastConsumeTS != 0 || st.RemainingSec <= 0 {
		return false
	}
	st.LastConsumeTS = float64(time.Now().Unix())
	if err := s.DB.UpsertTripState(ctx, *st); err != nil {
		return false
	}
	s.log.Info("钓鱼游进入地图开始计时", "place", level, "remaining_sec", st.RemainingSec)
	return true
}

// worldTotalGameMin 世界纪元以来的总游戏分钟（2015-08-01 基线 ×24 倍速）。
// 钓鱼游锚点（f23.oieepfcgjgn）必须用这个坐标：客户端世界时钟 W 由 2/2
// keepalive（f7 FILETIME）每 5 秒拉回服务端游戏时刻（rf4_hook 运行时证实：
// started=1、锚点会话坐标下仍立即判结束；2026-11-05 14:40 会话），
// gcepbaokfie.pioghgjeelp 按 剩余 = 时长 + min(start, W+3) − W 计算。
// f13.f0（日内分钟）只承担昼夜显示校准，与钓鱼游锚点无关。
func worldTotalGameMin(now time.Time) int64 {
	return int64(float64(now.Unix()-worldDayEpochUnix) * gameMinutesPerRealSecond)
}

// consumeTripTimes 消费该会话玩家所有钓鱼游的在线时长。
// 返回是否发生了到期回收，调用方据此推送档案刷新（f22 变化）。
func (s *Session) consumeTripTimes(now time.Time) bool {
	if s == nil || s.Srv == nil || s.Srv.DB == nil {
		return false
	}
	cctx := s.pushCtx()
	states, err := s.Srv.DB.TripStates(cctx, s.Userid)
	if err != nil || len(states) == 0 {
		return false
	}
	nowF := float64(now.Unix())
	startedF := float64(s.startedAt.Unix())
	revoked := false
	for _, st := range states {
		if st.RemainingSec <= 0 || st.LastConsumeTS == 0 {
			// 到期残留：补偿回收装备包（覆盖离线跨到期后重新登录的场景）；
			// 残留的 place_access 一并重试删除——状态清零成功而删除失败的行
			// 只会走到这里，不重试则 tripLevelLocked 仍认有效钓鱼游，
			// 已到期玩家可以重新进图（审查 P2 实证）。
			if st.RemainingSec <= 0 && st.Purchases > 0 {
				s.Srv.recallTripEquipment(cctx, s.Userid, st.Place)
				if rerr := s.Srv.DB.RevokeExpiredTripAccess(cctx, s.Userid, st.Place); rerr != nil {
					s.log.Warn("钓鱼游到期 access 补偿回收失败", "place", st.Place, "err", rerr)
				}
			}
			continue // 到期残留 / 已购未进图（倒计时未启动）
		}
		// 上次消费早于本会话启动：中间是离线间隔，不计时只对表。
		if st.LastConsumeTS < startedF {
			st.LastConsumeTS = nowF
			_ = s.Srv.DB.UpsertTripState(cctx, *st)
			continue
		}
		elapsed := nowF - st.LastConsumeTS
		if elapsed <= 0 {
			continue
		}
		expected := *st
		st.LastConsumeTS = nowF
		if elapsed < st.RemainingSec {
			st.RemainingSec -= elapsed
			_ = s.Srv.DB.UpsertTripState(cctx, *st)
			continue
		}
		// 到期：状态清零与回收 place_access 同事务（失败整体回滚下一拍
		// 重试），档案 f22 不再包含该地点；钓鱼游装备包（租用物品）一并
		// 从背包移除（trip_equip.go）。拆开执行曾在删除失败时留下 access
		// 残留行，资格判断因此放行已到期玩家。
		st.RemainingSec = 0
		expired, uerr := s.Srv.DB.ExpireTripAccessTx(cctx, expected, nowF)
		if uerr != nil {
			s.log.Warn("钓鱼游到期回收失败", "place", st.Place, "err", uerr)
			continue
		}
		if !expired {
			s.log.Info("钓鱼游状态已并发变化，本次不回收", "place", st.Place)
			continue
		}
		revoked = true
		s.Srv.recallTripEquipment(cctx, s.Userid, st.Place)
		s.log.Info("钓鱼游到期已回收", "place", st.Place)
	}
	return revoked
}

// hasTickingTrips 是否存在已启表且未到期的钓鱼游。保活循环据此低频推送
// 档案刷新——客户端徽章不自行重绘，剩余时间只在档案对象被替换时重算
// （同船票 pushTicketRemaining 的定期推送原理）。
func (s *Session) hasTickingTrips(ctx context.Context) bool {
	if s == nil || s.Srv == nil || s.Srv.DB == nil {
		return false
	}
	var n int
	if err := s.Srv.DB.SQL.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM trip_state WHERE uid=? AND last_consume_ts>0 AND remaining_sec>0`,
		s.Userid).Scan(&n); err != nil {
		return false
	}
	return n > 0
}
