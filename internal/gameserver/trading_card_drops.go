package gameserver

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

func cardProbability(value, fallback float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		value = fallback
	}
	return math.Max(0, math.Min(1, value))
}

func (s *Server) catchCardProbability(grade, rarity int64) float64 {
	key, fallback := "card_drop_normal", .001
	switch grade {
	case 3:
		key, fallback = "card_drop_star", .5
	case 4:
		key, fallback = "card_drop_blue", .8
	}
	probability := cardProbability(s.tunableF(key, fallback), fallback)
	if rarity > 0 {
		probability *= cardProbability(s.tunableF("card_drop_rare_scale", .25), .25)
	}
	return probability
}

// Stable draws make retrying the same catch reuse its original roll.
func catchCardDraw(uid int64, fishGUID string) (float64, string) {
	seed := fmt.Sprintf("rf4-card-drop:%d:%s", uid, strings.ToLower(fishGUID))
	hash := sha256.Sum256([]byte(seed))
	roll := float64(binary.LittleEndian.Uint64(hash[:8])>>11) / (1 << 53)
	guid := fmt.Sprintf("%x", hash[16:])
	return roll, guid
}

func (s *Server) prepareCatchCard(ctx context.Context, uid int64, fish *RolledFish, bait string) (*store.Item, error) {
	roll, guid := catchCardDraw(uid, fish.FishGUID)
	if roll >= s.catchCardProbability(fish.Grade, s.DB.SpeciesRarity(ctx, fish.Species)) {
		return nil, nil
	}
	var cardID string
	err := s.DB.SQL.QueryRowContext(ctx, `SELECT item_id FROM item_def WHERE category=78
		AND json_extract(attrs_json,'$.okppoalfakf')=? ORDER BY numeric_id LIMIT 1`, fish.Species).Scan(&cardID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	it, err := s.prepareNewItem(ctx, uid, cardID, tradingCardContainer, 1, guid)
	if err != nil {
		return nil, err
	}
	fields, err := FieldsFromJSON(it.FieldsJSON)
	if err != nil {
		return nil, err
	}
	caught := *fish
	if caught.LocLevel == "" {
		caught.LocLevel = s.currentLevel(uid)
	}
	fields[0].Val = s.tradingCardFish(ctx, &caught, time.Now())
	if bait != "" {
		fields[0].Val.Fields[8].Str = proto.S(bait)
	}
	it.FieldsJSON, err = FieldsToJSON(fields)
	return it, err
}
