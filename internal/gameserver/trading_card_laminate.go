package gameserver

// 27/4 卡册塑封/覆膜（客户端 lkokodffmln(Guid deck)；命令号 capstone 实锤
// 0x1805f994d：mov byte[rsp+0x20],4 + mov r9b,0x1b）。响应类型
// llgjfoldbcc（wire gglppphenoi/809 = pcnefbpgdhl 派生加 f0 奖励描述数组），
// 四值序 (f0 描述 plhpmhpadcg[], f1 奖励物品树, f2 备用, f3 Guid[])——解码
// 器 gglppphenoi.plphdfehkjl 依次读四值；奖励弹窗（blehdkolhfl）吃 f1 的
// 物品树，响应先经 eiihgcpdpbb 应用再回调。
//
// 语义（2026-11-08 实机）：集齐（f0 每物种一张）才能塑封；f1 密封时间戳
// 落卡册行（FILETIME！.NET Ticks 会被客户端 FromFileTime 渲染成 +1600 年
// → 3626/xx/xx）；奖座 = 卡册 def 属性 pnaapkplkfo，幂等补发；塑封后卡册
// 冻结——入册/取回一律拒绝（changeTradingCardAlbum 服务端守卫）。

import (
	"context"
	"encoding/json"
	"fmt"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

// deckSealed 读卡册行塑封时间戳（ckfmndabaei f1，U64；>0=已塑封）。
func deckSealed(fields []proto.Field) uint64 {
	if len(fields) > 1 && fields[1].Num != nil {
		return *fields[1].Num
	}
	return 0
}

// deckStatuettePlan 奖座发放计划：已有的行 + 待补发的新物品（事务外准备，
// 静态库查询不进事务——SQLite 单连接纪律，事务内只做 INSERT）。
type deckStatuettePlan struct {
	existing []*store.Item
	prepared []*store.Item
}

// prepareDeckStatuettes 事务外读 def 与已有奖座、构造缺发物品。
func (s *Server) prepareDeckStatuettes(ctx context.Context, uid int64, deckID string) (*deckStatuettePlan, error) {
	def, err := s.DB.GetItemDefByItemID(ctx, deckID)
	if err != nil || def == nil || def.Category != 82 {
		return nil, fmt.Errorf("卡册定义无效")
	}
	var attrs struct {
		Statuettes []string `json:"pnaapkplkfo"`
	}
	if err := json.Unmarshal([]byte(def.AttrsJSON), &attrs); err != nil {
		return nil, err
	}
	plan := &deckStatuettePlan{}
	for _, st := range attrs.Statuettes {
		if st == "" {
			continue
		}
		rows, err := s.DB.ListItemsByContainer(ctx, uid, "backpack")
		if err != nil {
			return nil, err
		}
		found := false
		for _, r := range rows {
			if r.ItemID == st {
				plan.existing = append(plan.existing, r)
				found = true
				break
			}
		}
		if found {
			continue
		}
		prepared, err := s.prepareNewItem(ctx, uid, st, "backpack", 1, "")
		if err != nil {
			return nil, err
		}
		plan.prepared = append(plan.prepared, prepared)
	}
	return plan, nil
}

func hTradingCardLaminate(ctx *Ctx) ([]byte, bool) {
	if !ctx.Sess.Srv.requireDB(ctx) {
		return nil, false
	}
	if !ctx.DecodeOK || ctx.Tree == nil || ctx.Tree.Kind != proto.KindArgs || ctx.Tree.ArgCount() != 1 ||
		ctx.Tree.ArgItem(0) == nil || ctx.Tree.ArgItem(0).Kind != proto.KindGuid {
		ctx.ReplyFailure("System.ArgumentException", "卡册塑封参数无效")
		return nil, true
	}
	s := ctx.Sess.Srv
	c, uid := ctx.Sess.pushCtx(), ctx.Sess.Userid
	deckGUID := ctx.Tree.ArgItem(0).GuidHex()
	if deckGUID == "" || deckGUID == zeroGUID {
		ctx.ReplyFailure("System.ArgumentException", "卡册塑封参数无效")
		return nil, true
	}
	// 事务外：卡册行、def、集齐校验、奖座准备（静态库查询不进事务）
	deck, err := s.DB.GetItemByGUID(c, deckGUID)
	if err != nil || deck == nil || deck.UID != uid || deck.WireClass != deckClass ||
		deck.Container != tradingCardContainer {
		ctx.ReplyFailure("System.InvalidOperationException", "卡册不存在或不属于当前玩家")
		return nil, true
	}
	fields, err := FieldsFromJSON(deck.FieldsJSON)
	if err != nil {
		ctx.ReplyFailure("System.IO.IOException", "卡册塑封失败，请重试")
		return nil, true
	}
	if deckSealed(fields) > 0 {
		ctx.ReplyFailure("System.InvalidOperationException", "卡册已塑封")
		return nil, true
	}
	cards, err := cardsInDeck(deck)
	if err != nil {
		ctx.ReplyFailure("System.IO.IOException", "卡册塑封失败，请重试")
		return nil, true
	}
	deckDef, err := s.DB.GetItemDefByItemID(c, deck.ItemID)
	if err != nil || deckDef == nil || deckDef.Category != 82 {
		ctx.ReplyFailure("System.InvalidOperationException", "卡册定义无效")
		return nil, true
	}
	var attrs struct {
		Cards []string `json:"cjmjfnheidh"`
	}
	if err := json.Unmarshal([]byte(deckDef.AttrsJSON), &attrs); err != nil || len(attrs.Cards) == 0 {
		ctx.ReplyFailure("System.InvalidOperationException", "卡册定义无效")
		return nil, true
	}
	if len(cards) < len(attrs.Cards) {
		ctx.ReplyFailure("System.InvalidOperationException", "卡册尚未集齐")
		return nil, true
	}
	plan, err := s.prepareDeckStatuettes(c, uid, deck.ItemID)
	if err != nil {
		ctx.Log.Warn("卡册塑封奖座准备失败", "deck", deck.ItemID, "err", err)
		ctx.ReplyFailure("System.IO.IOException", "卡册塑封失败，请重试")
		return nil, true
	}
	// 事务内：密封时间戳 + 奖座入库（事务外已复查塑封状态）
	tx, err := beginEconomyTx(c, s.DB)
	if err != nil {
		ctx.ReplyFailure("System.IO.IOException", "卡册塑封失败，请重试")
		return nil, true
	}
	defer tx.Rollback()
	fields[1].Num = ptrU64(uint64(proto.WindowsFiletimeNow()))
	if err := updateItemFieldsTx(c, s.DB, tx, deck, fields); err != nil {
		ctx.ReplyFailure("System.IO.IOException", "卡册塑封失败，请重试")
		return nil, true
	}
	granted := make([]*store.Item, 0, len(plan.prepared))
	for _, prepared := range plan.prepared {
		row, _, gerr := grantPreparedItemTx(c, tx, prepared, false)
		if gerr != nil {
			ctx.ReplyFailure("System.IO.IOException", "卡册塑封失败，请重试")
			return nil, true
		}
		granted = append(granted, row)
		s.log.Info("卡册塑封发奖座", "uid", uid, "deck", deck.ItemID, "item", prepared.ItemID)
	}
	if err := tx.Commit(); err != nil {
		ctx.ReplyFailure("System.IO.IOException", "卡册塑封失败，请重试")
		return nil, true
	}
	// ACK：obj-809 四值（f1=奖座树，可为空数组）
	trees := proto.Args(proto.S("61"))
	for _, row := range append(plan.existing, granted...) {
		if tree, terr := s.ItemToTree(c, row); terr == nil && tree != nil {
			trees.Items = append(trees.Items, tree)
		}
	}
	body, err := proto.EncodeValue(wiregen.MustMakeObj("gglppphenoi", map[int]*proto.Value{
		0: proto.Null(), 1: trees, 2: proto.Null(), 3: proto.Null(),
	}), s.Table())
	if err != nil {
		ctx.ReplyFailure("System.IO.IOException", "卡册塑封失败，请重试")
		return nil, true
	}
	// 会话内刷新：卡册 f1 已变、奖座进背包
	s.resyncTradingCardsTwice(ctx.Sess, uid)
	for _, row := range granted {
		s.pushItemUpdate(uid, row)
	}
	ctx.Log.Info("卡册塑封完成", "uid", uid, "deck", deck.ItemID, "rewards", len(trees.Items))
	return body, true
}
