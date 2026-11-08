package gameserver

// 收藏册的会话内同步通道（2026-11-08 实机逆向 + 三轮验证）：
//
// 客户端 4/x 推送分发器（aedpdgpbcho）对 sub=21 按 guid 删除本地库存、
// sub=22 批量 upsert；但 upsert 不替换已存在的本地物品对象——增量推送更新
// 过的卡（父 guid/容器变化）在客户端保持旧值。可靠做法是先 4/21 移除再
// 4/22 重推全量，迫使按「新物品」路径重新入柜（新卡/奖座的发放即时可见
// 依赖同一路径）。
//
// 另一条实机规律：卡牌列表视图的重建滞后一个事件代——结算推送后立刻打开
// 列表看不到新卡，下一次推送（哪怕内容不变）触发重建时才显示上一代的
// 数据；同帧连发两遍无效（被合并成一次重建），第二代必须跨帧延迟发出。

import (
	"context"
	"time"
)

// tradingCardItemGUIDs 玩家在收藏册容器里的全部卡片与卡册行
// （category 78/82 的实例；入册卡在 card_deck 容器，随卡册树一起下发）。
func (s *Server) tradingCardItemGUIDs(ctx context.Context, uid int64) []string {
	rows, err := s.DB.ListItemsByContainer(ctx, uid, tradingCardContainer)
	if err != nil {
		return nil
	}
	guids := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.WireClass == cardClass || r.WireClass == deckClass {
			guids = append(guids, r.GUID)
		}
	}
	return guids
}

// resyncTradingCards 收藏册全量先删后推（一代，见文件头注释）。
func (s *Server) resyncTradingCards(sess *Session, uid int64) {
	ctx := sess.pushCtx()
	guids := s.tradingCardItemGUIDs(ctx, uid)
	if len(guids) == 0 {
		return
	}
	sess.Push4_21(guids)
	sess.Push4_22ItemGUIDs(guids)
}

// resyncTradingCardsTwice 全量重同步两代：第二化延迟 600ms 跨帧发出，
// 触发列表视图重建时读到的正是第一代写入的终态。
func (s *Server) resyncTradingCardsTwice(sess *Session, uid int64) {
	s.resyncTradingCards(sess, uid)
	time.AfterFunc(600*time.Millisecond, func() { s.resyncTradingCards(sess, uid) })
}
