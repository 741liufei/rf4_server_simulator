package gameserver

// 27/1 卡片出售（客户端收藏册服务 bbpmdcodcee.igchdonlnmf；命令号 capstone
// 实锤：send 调用点 mov r9b,0x1b + sub=1）。只卖收藏册容器（native:18）里
// 未入册的散卡——已入册卡（container=card_deck）必须先 27/3 取回，与客户
// 端入口一致。请求参数 [GuidArr 卡实例, U64 客户端报价]；报价只作对账
// 日志，成交价以服务端 def 属性 obcephkkfhi 为准（防作弊口径）。
// 响应 obj-800：f2=已售 guid 列表，客户端按 guid 从本地库存移除。
// 幂等：同 call 重放返回缓存 ACK，不重复删除入账。

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"rf4server/internal/proto"
)

func hTradingCardSell(ctx *Ctx) ([]byte, bool) {
	if !ctx.Sess.Srv.requireDB(ctx) {
		return nil, false
	}
	if !ctx.DecodeOK || ctx.Tree == nil || ctx.Tree.Kind != proto.KindArgs || ctx.Tree.ArgCount() < 1 ||
		ctx.Tree.ArgItem(0) == nil || ctx.Tree.ArgItem(0).Kind != proto.KindGuidArr {
		ctx.ReplyFailure("System.ArgumentException", "卡片出售参数无效")
		return nil, true
	}
	clientQuote := uint64(0)
	if ctx.Tree.ArgCount() >= 2 && ctx.Tree.ArgItem(1) != nil && ctx.Tree.ArgItem(1).Kind == proto.KindU64 {
		clientQuote = ctx.Tree.ArgItem(1).U64Val()
	}
	s := ctx.Sess.Srv
	c, uid := ctx.Sess.pushCtx(), ctx.Sess.Userid
	key, sha := requestReceiptKey(ctx)
	receiptKey := fmt.Sprintf("card-sell:%d:%s:%d:%d:%d", uid, key.TokenHash, key.Main, key.Sub, key.CallID)
	tx, err := beginEconomyTx(c, s.DB)
	if err != nil {
		ctx.Log.Warn("卡片出售开事务失败", "err", err)
		ctx.ReplyFailure("System.IO.IOException", "卡片出售失败，请重试")
		return nil, true
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(c, `UPDATE kv SET v=v WHERE k=?`, receiptKey); err != nil {
		ctx.Log.Warn("卡片出售写锁失败", "err", err)
		ctx.ReplyFailure("System.IO.IOException", "卡片出售失败，请重试")
		return nil, true
	}
	var raw string
	if err := tx.QueryRowContext(c, `SELECT v FROM kv WHERE k=?`, receiptKey).Scan(&raw); err == nil {
		var receipt cardAlbumReceipt
		if err := json.Unmarshal([]byte(raw), &receipt); err != nil || receipt.RequestSHA != sha {
			ctx.Log.Warn("卡片出售回执不匹配", "err", err)
			ctx.ReplyFailure("System.InvalidOperationException", "重复请求的内容不一致")
			return nil, true
		}
		return receipt.Body, true
	} else if err != sql.ErrNoRows {
		ctx.Log.Warn("卡片出售读回执失败", "err", err)
		ctx.ReplyFailure("System.IO.IOException", "卡片出售失败，请重试")
		return nil, true
	}
	sold := make([]string, 0, len(ctx.Tree.ArgItem(0).Items))
	total := int64(0)
	for _, item := range ctx.Tree.ArgItem(0).Items {
		card, err := getOwnedItemTx(c, tx, uid, item.Hex)
		if err != nil || card.WireClass != cardClass || card.Qty != 1 ||
			card.Container != tradingCardContainer || card.RigGUID != "" {
			ctx.Log.Warn("卡片出售跳过无效卡", "guid", item.Hex, "err", err)
			continue
		}
		def, err := itemDefByItemIDTx(c, tx, card.ItemID)
		if err != nil || def.Category != 78 {
			ctx.Log.Warn("卡片出售跳过无定义卡", "guid", item.Hex, "item", card.ItemID)
			continue
		}
		price, _ := defAttrU64(def, "obcephkkfhi")
		if price <= 0 {
			ctx.Log.Warn("卡片出售跳过无价卡", "guid", item.Hex, "item", card.ItemID)
			continue
		}
		if _, err := tx.ExecContext(c, `DELETE FROM item WHERE guid=? AND uid=?`, item.Hex, uid); err != nil {
			ctx.Log.Warn("卡片出售删卡失败", "guid", item.Hex, "err", err)
			ctx.ReplyFailure("System.IO.IOException", "卡片出售失败，请重试")
			return nil, true
		}
		sold = append(sold, item.Hex)
		total += int64(price)
	}
	if len(sold) == 0 {
		ctx.ReplyFailure("System.InvalidOperationException", "没有可出售的卡片")
		return nil, true
	}
	if clientQuote != 0 && clientQuote != uint64(total) {
		ctx.Log.Info("卡片出售报价与成交价不一致（以服务端为准）",
			"uid", uid, "client", clientQuote, "server", total)
	}
	if err := applyWalletTx(c, tx, uid, total, 0, "sell_card", "", "",
		fmt.Sprintf("收藏册出售 %d 张", len(sold))); err != nil {
		ctx.Log.Warn("卡片出售入账失败", "err", err)
		ctx.ReplyFailure("System.IO.IOException", "卡片出售失败，请重试")
		return nil, true
	}
	body, err := proto.EncodeValue(build800(
		proto.Args(proto.S("61")),
		proto.Args(proto.S("61")),
		proto.GuidArr(sold)), s.Table())
	if err != nil {
		ctx.Log.Warn("卡片出售编码失败", "err", err)
		ctx.ReplyFailure("System.IO.IOException", "卡片出售失败，请重试")
		return nil, true
	}
	receipt, err := json.Marshal(cardAlbumReceipt{RequestSHA: sha, Body: body})
	if err != nil {
		ctx.ReplyFailure("System.IO.IOException", "卡片出售失败，请重试")
		return nil, true
	}
	if _, err := tx.ExecContext(c, `INSERT INTO kv(k,v) VALUES(?,?)`, receiptKey, string(receipt)); err != nil {
		ctx.Log.Warn("卡片出售写回执失败", "err", err)
		ctx.ReplyFailure("System.IO.IOException", "卡片出售失败，请重试")
		return nil, true
	}
	if err := tx.Commit(); err != nil {
		ctx.Log.Warn("卡片出售提交失败", "err", err)
		ctx.ReplyFailure("System.IO.IOException", "卡片出售失败，请重试")
		return nil, true
	}
	// 会话内刷新：已售卡 4/21 移除（客户端删除通道可靠），列表/余额随后同步
	ctx.Sess.Push4_21(sold)
	s.resyncTradingCardsTwice(ctx.Sess, uid)
	ctx.Sess.Push2_1()
	ctx.Log.Info("卡片出售完成", "uid", uid, "cards", len(sold), "silver", total)
	return body, true
}
