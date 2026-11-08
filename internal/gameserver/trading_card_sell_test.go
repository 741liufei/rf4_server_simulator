package gameserver

import (
	"testing"

	"rf4server/internal/proto"
)

// 27/1 出售：只卖散卡（native:18 且未入册），成交价取 def 属性
// obcephkkfhi，回执幂等，f2=已售 guid。
func TestTradingCardSell(t *testing.T) {
	s, db := newProgressSrv(t)
	seedTradingCards(t, db)
	// 补售价定义
	if _, err := db.SQL.Exec(`UPDATE item_def SET attrs_json=json_set(attrs_json,'$.obcephkkfhi',12000)
		WHERE item_id='card_perch'`); err != nil {
		t.Fatal(err)
	}
	c := t.Context()
	if _, err := db.EnsureProfile(c, 7); err != nil {
		t.Fatal(err)
	}
	card, err := s.CreateItem(c, 7, "card_perch", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	var silverBefore int64
	if err := db.SQL.QueryRow(`SELECT silver FROM profile WHERE uid=7`).Scan(&silverBefore); err != nil {
		t.Fatal(err)
	}
	ctx := tradingAlbumRequest(t, s, 1, 601,
		proto.GuidArr([]string{card.GUID, "ffffffffffffffffffffffffffffffff"}), proto.U64(12000))
	result := s.dispatch.executeRequest(ctx, true)
	if !result.hasResponse || result.failed {
		t.Fatalf("sell response: %+v", result)
	}
	body := result.body
	v, _, err := proto.DecodeValue(body, 0, s.Table())
	if err != nil {
		t.Fatal(err)
	}
	sold := v.Fields[2].Val
	if sold.Kind != proto.KindGuidArr || len(sold.Items) != 1 || sold.Items[0].Hex != card.GUID {
		t.Fatalf("已售清单应只含有效卡: %+v", sold)
	}
	var silver int64
	if err := db.SQL.QueryRow(`SELECT silver FROM profile WHERE uid=7`).Scan(&silver); err != nil {
		t.Fatal(err)
	}
	if silver-silverBefore != 12000 {
		t.Fatalf("入账错误: %d", silver-silverBefore)
	}
	// 外星 guid 不计价；卡已删除
	if _, err := db.GetItemByGUID(c, card.GUID); err == nil {
		t.Fatal("出售后卡应被删除")
	}
	// 回执幂等：同 call 重放返回缓存 ACK，不重复入账
	replay := tradingAlbumRequest(t, s, 1, 601,
		proto.GuidArr([]string{card.GUID}), proto.U64(12000))
	replayResult := s.dispatch.executeRequest(replay, true)
	if !replayResult.hasResponse || replayResult.failed || len(replayResult.body) == 0 {
		t.Fatalf("sell replay: %+v", replayResult)
	}
	if err := db.SQL.QueryRow(`SELECT silver FROM profile WHERE uid=7`).Scan(&silver); err != nil {
		t.Fatal(err)
	}
	if silver-silverBefore != 12000 {
		t.Fatalf("重放不得重复入账: %d", silver-silverBefore)
	}
}

// 已入册的卡不能直接出售（先取回），与客户端入口一致。
func TestTradingCardSellRejectsAlbumCard(t *testing.T) {
	s, db := newProgressSrv(t)
	seedTradingCards(t, db)
	if _, err := db.SQL.Exec(`UPDATE item_def SET attrs_json=json_set(attrs_json,'$.obcephkkfhi',12000)
		WHERE item_id='card_perch'`); err != nil {
		t.Fatal(err)
	}
	c := t.Context()
	card, err := s.CreateItem(c, 7, "card_perch", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	addCtx := tradingAlbumRequest(t, s, 2, 602, proto.Str(proto.S("tcdeck_test")), proto.Guid(zeroGUID), proto.Guid(card.GUID))
	runTradingAlbumRequest(t, s, addCtx, false)
	sellCtx := tradingAlbumRequest(t, s, 1, 603, proto.GuidArr([]string{card.GUID}), proto.U64(12000))
	runTradingAlbumRequest(t, s, sellCtx, true)
	if row, err := db.GetItemByGUID(c, card.GUID); err != nil || row.Container != "card_deck" {
		t.Fatalf("入册卡不得被出售: %v %s", err, row.Container)
	}
}
