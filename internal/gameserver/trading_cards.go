package gameserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

const (
	cardClass            = "dapkdcghfmg"
	deckClass            = "ckfmndabaei"
	tradingCardContainer = "native:18"
	tradingCardArrayCode = "362"
	tradingDeckArrayCode = "364"
)

func tradingItemContainer(class, container string) string {
	if container == "backpack" && (class == cardClass || class == deckClass) {
		return tradingCardContainer
	}
	return container
}

func rootItemStorageKind(class string) uint64 {
	if class == cardClass || class == deckClass {
		return containerTradingCards
	}
	return containerBackpack
}

// Client resolver codes for ipdpoaiekem[] and imdcjllgdcn[].
// These instance arrays are distinct from the album definition array (363).
func tradingCardList(cards ...*proto.Value) *proto.Value {
	return proto.Args(proto.S(tradingCardArrayCode), cards...)
}
func tradingDeckList(decks ...*proto.Value) *proto.Value {
	return proto.Args(proto.S(tradingDeckArrayCode), decks...)
}

// initializeTradingItem also repairs empty payloads of cards previously granted
// by the generic item tool. It never changes an existing catch or album entry.
func (s *Server) initializeTradingItem(ctx context.Context, def *store.ItemDef, obj *proto.Value) error {
	if def.Category != 78 && def.Category != 82 {
		return nil
	}
	if obj.Fields[0].Val != nil && obj.Fields[0].Val.Kind != proto.KindNull {
		return nil
	}
	if def.Category == 82 {
		obj.Fields[0].Val = tradingCardList()
		return nil
	}
	var attrs struct {
		Fish string `json:"okppoalfakf"`
	}
	if err := json.Unmarshal([]byte(def.AttrsJSON), &attrs); err != nil || attrs.Fish == "" {
		return fmt.Errorf("卡片缺少鱼种定义：%s", def.ItemID)
	}
	if s.rollGradeDB(ctx, attrs.Fish, 1) != 1 {
		return fmt.Errorf("%s：没有普通鱼档位", attrs.Fish)
	}
	k, p := s.DB.SpeciesLengthKP(ctx, attrs.Fish)
	k, _ = calibratedLengthKP(attrs.Fish, k, p)
	obj.Fields[0].Val = s.tradingCardFish(ctx, &RolledFish{Species: attrs.Fish, Grade: 1, WeightG: 1,
		LengthM: k, FishGUID: NewItemGUID()}, time.Now())
	return nil
}

func (s *Server) tradingCardFish(ctx context.Context, fish *RolledFish, caught time.Time) *proto.Value {
	return buildKeepnet25(&store.KeepnetEntry{EchoGUID: fish.FishGUID, Species: fish.Species,
		WeightG: fish.WeightG, LengthM: fish.LengthM, Cat: fish.Grade, KeepLevel: fish.LocLevel,
		NameZh: fish.Name, TS: float64(caught.UnixNano()) / 1e9}, fish.LocLevel, s.DB.SpeciesRarity(ctx, fish.Species))
}

type tradingQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func tradingInventory(ctx context.Context, q tradingQueryer, uid int64) ([]*store.Item, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+txItemCols+` FROM item WHERE uid=? AND container IN (?,?)
		AND numeric_id IN (SELECT numeric_id FROM item_def WHERE category IN (78,82)) ORDER BY created_ts,guid`, uid, tradingCardContainer, "card_deck")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []*store.Item{}
	for rows.Next() {
		it, err := scanTxItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

func cardsInDeck(it *store.Item) ([]*proto.Value, error) {
	if it.WireClass != deckClass {
		return nil, fmt.Errorf("卡册类型待修复，请先重启服务")
	}
	fields, err := FieldsFromJSON(it.FieldsJSON)
	if err != nil || len(fields) == 0 {
		return nil, fmt.Errorf("卡册数据损坏")
	}
	v := fields[0].Val
	if v == nil || v.Kind == proto.KindNull {
		return nil, nil
	}
	// Accept the stored legacy code; outgoing projections use the instance code.
	if v.Kind != proto.KindArgs || v.Code == nil || (*v.Code != tradingCardArrayCode && *v.Code != "363") {
		return nil, fmt.Errorf("卡册数组类型不正确")
	}
	for _, card := range v.Items {
		if card == nil || card.Class != cardClass || len(card.Fields) != 14 {
			return nil, fmt.Errorf("卡册包含无效卡片")
		}
	}
	return v.Items, nil
}

func (s *Server) playerTradingDecks(ctx context.Context, uid int64) (*proto.Value, error) {
	items, err := tradingInventory(ctx, s.DB.SQL, uid)
	if err != nil {
		return nil, err
	}
	decks := []*proto.Value{}
	for _, it := range items {
		if it.WireClass != deckClass {
			continue
		}
		tree, err := s.ItemToTree(ctx, it)
		if err != nil {
			return nil, err
		}
		decks = append(decks, tree)
	}
	return tradingDeckList(decks...), nil
}
