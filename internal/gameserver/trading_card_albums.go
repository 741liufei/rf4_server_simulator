package gameserver

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

func registerTradingCardHandlers(d *Dispatch) {
	d.Register(27, 1, hTradingCardSell)
	d.Register(27, 2, hTradingCardAdd)
	d.Register(27, 3, hTradingCardRemove)
	d.Register(27, 4, hTradingCardLaminate)
	for _, sub := range []uint8{1, 2, 3} {
		d.RegisterResponseContract(27, sub, ResponseContractRule{
			Kind: proto.KindObj, TypeIDs: []uint32{800}, Classes: []string{"dlbahppbdad"},
		})
	}
	// 27/4 塑封响应 = llgjfoldbcc（pcnefbpgdhl 派生 + f0 奖励描述数组）。
	d.RegisterResponseContract(27, 4, ResponseContractRule{
		Kind: proto.KindObj, TypeIDs: []uint32{809}, Classes: []string{"gglppphenoi"},
	})
}

func hTradingCardAdd(ctx *Ctx) ([]byte, bool) {
	if !ctx.Sess.Srv.requireDB(ctx) {
		return nil, false
	}
	if !ctx.DecodeOK || ctx.Tree == nil || ctx.Tree.Kind != proto.KindArgs || len(ctx.Tree.Items) != 3 ||
		ctx.Tree.ArgItem(0) == nil || ctx.Tree.ArgItem(0).Kind != proto.KindStr ||
		ctx.Tree.ArgItem(1) == nil || ctx.Tree.ArgItem(1).Kind != proto.KindGuid ||
		ctx.Tree.ArgItem(2) == nil || ctx.Tree.ArgItem(2).Kind != proto.KindGuid {
		ctx.ReplyFailure("System.ArgumentException", "卡片入册参数无效")
		return nil, true
	}
	key := ctx.Tree.ArgItem(0).StrOr()
	deck := ctx.Tree.ArgItem(1).GuidHex()
	card := ctx.Tree.ArgItem(2).GuidHex()
	body, err := ctx.Sess.Srv.changeTradingCardAlbum(ctx, key, deck, card, true)
	if err != nil {
		ctx.Log.Warn("卡片入册失败", "err", err)
		ctx.ReplyFailure("System.InvalidOperationException", err.Error())
		return nil, true
	}
	return body, true
}

func hTradingCardRemove(ctx *Ctx) ([]byte, bool) {
	if !ctx.Sess.Srv.requireDB(ctx) {
		return nil, false
	}
	if !ctx.DecodeOK || ctx.Tree == nil || ctx.Tree.Kind != proto.KindArgs || len(ctx.Tree.Items) != 1 ||
		ctx.Tree.ArgItem(0) == nil || ctx.Tree.ArgItem(0).Kind != proto.KindGuid {
		ctx.ReplyFailure("System.ArgumentException", "卡片取回参数无效")
		return nil, true
	}
	body, err := ctx.Sess.Srv.changeTradingCardAlbum(ctx, "", "", ctx.Tree.ArgItem(0).GuidHex(), false)
	if err != nil {
		ctx.Log.Warn("卡片取回失败", "err", err)
		ctx.ReplyFailure("System.InvalidOperationException", err.Error())
		return nil, true
	}
	return body, true
}

type cardAlbumReceipt struct {
	RequestSHA string
	Body       []byte
}

func (s *Server) changeTradingCardAlbum(ctx *Ctx, deckKey, deckGUID, cardGUID string, add bool) ([]byte, error) {
	c, uid := ctx.Sess.pushCtx(), ctx.Sess.Userid
	if cardGUID == "" || cardGUID == zeroGUID {
		return nil, fmt.Errorf("请选择已有卡片")
	}
	if deckGUID == zeroGUID {
		deckGUID = ""
	}
	var candidate *store.Item
	if add && deckGUID == "" {
		def, err := s.DB.GetItemDefByItemID(c, deckKey)
		if err != nil || def.Category != 82 {
			return nil, fmt.Errorf("卡册定义不存在")
		}
		candidate, err = s.prepareNewItem(c, uid, deckKey, tradingCardContainer, 1, "")
		if err != nil {
			return nil, err
		}
	}
	key, sha := requestReceiptKey(ctx)
	receiptKey := fmt.Sprintf("card-album:%d:%s:%d:%d:%d", uid, key.TokenHash, key.Main, key.Sub, key.CallID)
	tx, err := beginEconomyTx(c, s.DB)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(c, `UPDATE kv SET v=v WHERE k=?`, receiptKey); err != nil {
		return nil, err
	}
	var raw string
	if err := tx.QueryRowContext(c, `SELECT v FROM kv WHERE k=?`, receiptKey).Scan(&raw); err == nil {
		var receipt cardAlbumReceipt
		if err := json.Unmarshal([]byte(raw), &receipt); err != nil {
			return nil, err
		}
		if receipt.RequestSHA != sha {
			return nil, fmt.Errorf("重复请求的内容不一致")
		}
		return receipt.Body, nil
	} else if err != sql.ErrNoRows {
		return nil, err
	}
	card, err := getOwnedItemTx(c, tx, uid, cardGUID)
	if err != nil || card.WireClass != cardClass || card.Qty != 1 {
		return nil, fmt.Errorf("卡片不存在或不属于当前玩家")
	}
	var deck *store.Item
	if add {
		if card.Container == "card_deck" {
			if deckGUID != "" && deckGUID != card.RigGUID {
				return nil, fmt.Errorf("卡片已在其他卡册中")
			}
			deckGUID = card.RigGUID
		} else if card.Container != tradingCardContainer || card.RigGUID != "" {
			return nil, fmt.Errorf("只能将收藏中的散卡入册")
		}
		if deckGUID == "" {
			deck, err = scanTxItem(tx.QueryRowContext(c, `SELECT `+txItemCols+` FROM item
				WHERE uid=? AND item_id=? AND container=? ORDER BY created_ts,guid LIMIT 1`, uid, deckKey, tradingCardContainer))
			if err == sql.ErrNoRows {
				deck, err = candidate, nil
				if err = insertItemTx(c, tx, deck); err != nil {
					return nil, err
				}
			}
		} else {
			deck, err = getOwnedItemTx(c, tx, uid, deckGUID)
		}
	} else {
		if card.Container != "card_deck" || card.RigGUID == "" {
			return nil, fmt.Errorf("卡片不在卡册中")
		}
		deck, err = getOwnedItemTx(c, tx, uid, card.RigGUID)
	}
	if err != nil || deck == nil || deck.WireClass != deckClass || deck.Container != tradingCardContainer ||
		(add && deckKey != "" && deck.ItemID != deckKey) {
		return nil, fmt.Errorf("卡册不存在或不属于当前玩家")
	}
	// 塑封后的卡册冻结：入册/取回一律拒绝（27/4 塑封语义）。
	if sealedFields, serr := FieldsFromJSON(deck.FieldsJSON); serr == nil && deckSealed(sealedFields) > 0 {
		return nil, fmt.Errorf("卡册已塑封")
	}
	cards, err := cardsInDeck(deck)
	if err != nil {
		return nil, err
	}
	cardFields, err := FieldsFromJSON(card.FieldsJSON)
	if err != nil || len(cardFields) != 14 || cardFields[0].Val == nil || cardFields[0].Val.TypeID != 25 {
		return nil, fmt.Errorf("卡片钓获资料无效")
	}
	cardFields[3].Num = ptrU64(containerTradingCards)
	idx := -1
	for i, entry := range cards {
		if strings.EqualFold(entry.Fields[1].Hex, cardGUID) {
			idx = i
		}
	}
	if add {
		def, err := itemDefByItemIDTx(c, tx, deck.ItemID)
		if err != nil || def.Category != 82 {
			return nil, fmt.Errorf("卡册定义无效")
		}
		var attrs struct {
			Cards []string `json:"cjmjfnheidh"`
		}
		if err := json.Unmarshal([]byte(def.AttrsJSON), &attrs); err != nil || !containsString(attrs.Cards, card.ItemID) {
			return nil, fmt.Errorf("该卡片不属于此卡册")
		}
		for _, entry := range cards {
			if idx < 0 && entry.Fields[4].U32() == uint32(card.NumericID) {
				return nil, fmt.Errorf("该鱼种已入册，请先取回原卡")
			}
		}
		if idx < 0 {
			if card.Container != tradingCardContainer {
				return nil, fmt.Errorf("卡册与卡片归属不一致")
			}
			card.RigGUID, card.Container = deck.GUID, "card_deck"
			cardFields[2].Hex = deck.GUID
			if err := updateItemFieldsTx(c, s.DB, tx, card, cardFields); err != nil {
				return nil, err
			}
			cards = append(cards, proto.Obj(cardClass, 11301, cardFields...))
		}
	} else {
		if idx < 0 {
			return nil, fmt.Errorf("卡册中未找到该卡片")
		}
		cards = append(cards[:idx:idx], cards[idx+1:]...)
		card.RigGUID, card.Container = "", tradingCardContainer
		clearRigFieldsForBackpack(cardFields, cardClass)
		if err := updateItemFieldsTx(c, s.DB, tx, card, cardFields); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(c, `UPDATE item SET container=?,slot=0 WHERE guid=? AND uid=?`, card.Container, cardGUID, uid); err != nil {
		return nil, err
	}
	deckFields, err := FieldsFromJSON(deck.FieldsJSON)
	if err != nil {
		return nil, err
	}
	deckFields[0].Val = tradingCardList(cards...)
	deckFields[4].Num = ptrU64(containerTradingCards)
	if err := updateItemFieldsTx(c, s.DB, tx, deck, deckFields); err != nil {
		return nil, err
	}
	// The client fills album slots from collection cards whose parent GUID
	// matches the album. Removing the GUID would delete that client-side card.
	upserts := []*proto.Value{
		proto.Obj(deckClass, 11304, deckFields...),
		proto.Obj(cardClass, 11301, cardFields...),
	}
	body, err := proto.EncodeValue(build800(proto.Args(proto.S("61")), proto.Args(proto.S("61"), upserts...), proto.GuidArr(nil)), s.Table())
	if err != nil {
		return nil, err
	}
	receipt, err := json.Marshal(cardAlbumReceipt{RequestSHA: sha, Body: body})
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(c, `INSERT INTO kv(k,v) VALUES(?,?)`, receiptKey, string(receipt)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return body, nil
}
