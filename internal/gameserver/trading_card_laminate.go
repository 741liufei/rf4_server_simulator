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
	"database/sql"
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

// deckStatuettePlan 奖座发放计划（全部事务外准备——静态库查询不进事务的
// SQLite 单连接纪律）：def 顺序的奖座清单、已有行、缺发构造件，以及两种
// 来源的响应树。事务内只做权威判定（是否已有/是否补发）与 INSERT；
// ItemToTree 读静态库，不得在事务内调用（写锁期间池化读会饿死）。
type deckStatuettePlan struct {
	statuettes []string
	prepared   map[string]*store.Item
	trees      map[string]*proto.Value // GUID -> 事务外预生成的响应树
	containers map[string]string       // 已有奖座 GUID -> 事务外快照容器
}

// firstOwnedItemByItemID 事务外查玩家全部容器中的首个同定义物品。
// 奖座可被 4/6 移到房屋等容器，判断“已拥有”不能只看背包。
func (s *Server) firstOwnedItemByItemID(ctx context.Context, uid int64, itemID string) (*store.Item, error) {
	row := s.DB.SQL.QueryRowContext(ctx, `SELECT `+txItemCols+` FROM item
		WHERE uid=? AND item_id=? ORDER BY created_ts,guid LIMIT 1`, uid, itemID)
	it, err := scanTxItem(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return it, nil
}

// prepareDeckStatuettes 事务外读 def、已有奖座与缺发构造件，并预生成响应树。
// 已有行按 GUID 保存树和容器快照；缺发构造件落库时 GUID 不变。
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
	plan := &deckStatuettePlan{
		prepared:   map[string]*store.Item{},
		trees:      map[string]*proto.Value{},
		containers: map[string]string{},
	}
	for _, st := range attrs.Statuettes {
		if st == "" {
			continue
		}
		plan.statuettes = append(plan.statuettes, st)
		existing, err := s.firstOwnedItemByItemID(ctx, uid, st)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			tree, err := s.ItemToTree(ctx, existing)
			if err != nil || tree == nil {
				return nil, fmt.Errorf("已有奖座树构造失败: %s", st)
			}
			plan.trees[existing.GUID] = tree
			plan.containers[existing.GUID] = existing.Container
			continue
		}
		prepared, err := s.prepareNewItem(ctx, uid, st, "backpack", 1, "")
		if err != nil {
			return nil, err
		}
		tree, err := s.ItemToTree(ctx, prepared)
		if err != nil || tree == nil {
			return nil, fmt.Errorf("待发奖座树构造失败: %s", st)
		}
		plan.prepared[st] = prepared
		plan.trees[prepared.GUID] = tree
	}
	return plan, nil
}

// deckComplete 集齐校验：f0 内嵌卡的鱼种数字 id 去重后须恰好覆盖卡组
// def 清单（成员校验而非只比数量——数量相同但混入外组卡/重复卡不得塑封）。
func deckComplete(cards []*proto.Value, wantNumeric map[uint32]bool) bool {
	seen := make(map[uint32]bool, len(cards))
	for _, entry := range cards {
		if len(entry.Fields) <= 4 {
			return false
		}
		id := entry.Fields[4].U32()
		if !wantNumeric[id] || seen[id] {
			return false
		}
		seen[id] = true
	}
	return len(seen) == len(wantNumeric)
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
	// 事务外只做静态库读取与奖座准备（SQLite 单连接纪律）：卡册 def、
	// 集齐判定的鱼种数字 id 集合、缺发奖座的构造件。塑封状态与集齐的
	// 权威校验在下方事务内以事务快照复查——否则并发的同 call 请求能同时
	// 通过预检，第二个获写锁后再插一遍奖座（2026-11-08 review P1）。
	deckDef, err := s.DB.GetItemDefByItemID(c, deckIDByGUIDLoose(c, s, uid, deckGUID))
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
	wantNumeric := make(map[uint32]bool, len(attrs.Cards))
	for _, cardID := range attrs.Cards {
		cardDef, cerr := s.DB.GetItemDefByItemID(c, cardID)
		if cerr != nil || cardDef == nil || cardDef.Category != 78 {
			ctx.ReplyFailure("System.InvalidOperationException", "卡册定义无效")
			return nil, true
		}
		wantNumeric[uint32(cardDef.NumericID)] = true
	}
	plan, err := s.prepareDeckStatuettes(c, uid, deckDef.ItemID)
	if err != nil {
		ctx.Log.Warn("卡册塑封奖座准备失败", "deck", deckDef.ItemID, "err", err)
		ctx.ReplyFailure("System.IO.IOException", "卡册塑封失败，请重试")
		return nil, true
	}
	tx, err := beginEconomyTx(c, s.DB)
	if err != nil {
		ctx.ReplyFailure("System.IO.IOException", "卡册塑封失败，请重试")
		return nil, true
	}
	defer tx.Rollback()
	// ── 事务内权威校验与写入（review P1：并发/重放防线）──
	deck, err := getOwnedItemTx(c, tx, uid, deckGUID)
	if err != nil || deck == nil || deck.WireClass != deckClass || deck.Container != tradingCardContainer ||
		deck.ItemID != deckDef.ItemID {
		ctx.ReplyFailure("System.InvalidOperationException", "卡册不存在或不属于当前玩家")
		return nil, true
	}
	fields, err := FieldsFromJSON(deck.FieldsJSON)
	if err != nil {
		ctx.ReplyFailure("System.IO.IOException", "卡册塑封失败，请重试")
		return nil, true
	}
	if deckSealed(fields) > 0 {
		// 重放/并发的第二发在此拦截（首个事务已提交密封）
		ctx.ReplyFailure("System.InvalidOperationException", "卡册已塑封")
		return nil, true
	}
	cards, err := cardsInDeck(deck)
	if err != nil {
		ctx.ReplyFailure("System.IO.IOException", "卡册塑封失败，请重试")
		return nil, true
	}
	if !deckComplete(cards, wantNumeric) {
		ctx.ReplyFailure("System.InvalidOperationException", "卡册尚未集齐")
		return nil, true
	}
	fields[1].Num = ptrU64(uint64(proto.WindowsFiletimeNow()))
	if err := updateItemFieldsTx(c, s.DB, tx, deck, fields); err != nil {
		ctx.ReplyFailure("System.IO.IOException", "卡册塑封失败，请重试")
		return nil, true
	}
	// 奖座幂等补发：是否已有以事务内查询为准，只插缺的
	trees := proto.Args(proto.S("61"))
	granted := 0
	for _, st := range plan.statuettes {
		existing, terr := scanTxItem(tx.QueryRowContext(c, `SELECT `+txItemCols+
			` FROM item WHERE uid=? AND item_id=? ORDER BY created_ts,guid LIMIT 1`, uid, st))
		if terr == nil {
			tree := plan.trees[existing.GUID]
			if tree == nil || plan.containers[existing.GUID] != existing.Container {
				// 事务外预生成的树必须与事务内实际命中的行一致，避免 ACK 幽灵 GUID。
				ctx.Log.Warn("卡册奖座在事务外准备后状态变化", "item", st, "guid", existing.GUID)
				ctx.ReplyFailure("System.IO.IOException", "卡册塑封失败，请重试")
				return nil, true
			}
			trees.Items = append(trees.Items, tree)
			continue
		} else if terr != sql.ErrNoRows {
			ctx.ReplyFailure("System.IO.IOException", "卡册塑封失败，请重试")
			return nil, true
		}
		prepared := plan.prepared[st]
		if prepared == nil {
			// 事务外已拥有但事务内查无：行被并发移动/删除，拒绝重试
			ctx.ReplyFailure("System.IO.IOException", "卡册塑封失败，请重试")
			return nil, true
		}
		row, _, gerr := grantPreparedItemTx(c, tx, prepared, false)
		if gerr != nil {
			ctx.ReplyFailure("System.IO.IOException", "卡册塑封失败，请重试")
			return nil, true
		}
		tree := plan.trees[row.GUID]
		if tree == nil {
			ctx.ReplyFailure("System.IO.IOException", "卡册塑封失败，请重试")
			return nil, true
		}
		granted++
		s.log.Info("卡册塑封发奖座", "uid", uid, "deck", deck.ItemID, "item", st)
		trees.Items = append(trees.Items, tree)
	}
	// 响应在提交前构造完毕：提交后不再有可失败步骤（review 次要项）。
	// f0 plhpmhpadcg[] 的 args code 未在真实注册表实证（已实证仅 61/362/
	// 363/364，见 trading-card-array-types spec；DTO 名直发会 GetType not
	// found，错码会 InvalidCastException），按实测安全值发 null。
	body, err := proto.EncodeValue(wiregen.MustMakeObj("gglppphenoi", map[int]*proto.Value{
		0: proto.Null(), 1: trees, 2: proto.Args(proto.S("61")), 3: proto.GuidArr(nil),
	}), s.Table())
	if err != nil {
		ctx.ReplyFailure("System.IO.IOException", "卡册塑封失败，请重试")
		return nil, true
	}
	if err := tx.Commit(); err != nil {
		ctx.ReplyFailure("System.IO.IOException", "卡册塑封失败，请重试")
		return nil, true
	}
	// 会话内刷新：卡册 f1 已变、新奖座进背包
	s.resyncTradingCardsTwice(ctx.Sess, uid)
	for _, prepared := range plan.prepared {
		if row, gerr := s.DB.GetItemByGUID(c, prepared.GUID); gerr == nil && row != nil {
			s.pushItemUpdate(uid, row)
		}
	}
	ctx.Log.Info("卡册塑封完成", "uid", uid, "deck", deck.ItemID, "rewards", len(trees.Items), "granted", granted)
	return body, true
}

// deckIDByGUIDLoose 事务外按卡册实例 guid 粗取 def id（仅用于静态库预读；
// 权威归属校验在事务内）。
func deckIDByGUIDLoose(ctx context.Context, s *Server, uid int64, deckGUID string) string {
	row, err := s.DB.GetItemByGUID(ctx, deckGUID)
	if err != nil || row == nil || row.UID != uid {
		return deckGUID // 查无此行时原样返回，让 def 校验拒绝
	}
	return row.ItemID
}
