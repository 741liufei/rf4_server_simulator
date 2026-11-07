package gameserver

import (
	"bytes"
	"testing"
	"time"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
)

func tradingAlbumRequest(t *testing.T, s *Server, sub int, call uint32, values ...*proto.Value) *Ctx {
	t.Helper()
	raw, err := proto.EncodeValue(proto.Args524(values...), s.Table())
	if err != nil {
		t.Fatal(err)
	}
	tree, _, err := proto.DecodeValue(raw, 0, s.Table())
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(s)
	sess.Userid, sess.Token = 7, "album-test"
	return &Ctx{Sess: sess, Main: 27, Sub: sub, CallID: call, Body: raw, Tree: tree,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard()}
}

func runTradingAlbumRequest(t *testing.T, s *Server, ctx *Ctx, failure bool) []byte {
	t.Helper()
	result := s.dispatch.executeRequest(ctx, true)
	if !result.hasResponse || result.failed || result.nativeFailure != failure {
		t.Fatalf("album response: %+v", result)
	}
	if !failure {
		v, _, err := proto.DecodeValue(result.body, 0, s.Table())
		if err != nil || v.TypeID != 800 || v.Fields[0].Val.Kind != proto.KindArgs ||
			v.Fields[0].Val.Code == nil || *v.Fields[0].Val.Code != "61" || len(v.Fields[0].Val.Items) != 0 ||
			v.Fields[1].Val.Kind != proto.KindArgs || v.Fields[1].Val.Code == nil || *v.Fields[1].Val.Code != "61" ||
			v.Fields[2].Val.Kind != proto.KindGuidArr || len(v.Fields[2].Val.Items) != 0 {
			t.Fatalf("invalid 800 delta: %+v %v", v, err)
		}
	}
	return result.body
}

func TestTradingAlbumRoundTripAndReplay(t *testing.T) {
	s, db := newProgressSrv(t)
	seedTradingCards(t, db)
	c := t.Context()
	card, err := s.CreateItem(c, 7, "card_perch", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	fields, _ := FieldsFromJSON(card.FieldsJSON)
	fields[0].Val = s.tradingCardFish(c, &RolledFish{FishGUID: "11111111111111111111111111111111",
		Species: "perch", WeightG: 2100, LengthM: .48, Grade: 4, LocLevel: "level_001_lake"}, time.Now())
	original, err := proto.EncodeValue(fields[0].Val, s.Table())
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := FieldsToJSON(fields)
	if err := db.UpdateItemFields(c, card.GUID, encoded, card.Qty, card.Durability, card.Workmanship, ""); err != nil {
		t.Fatal(err)
	}
	add := tradingAlbumRequest(t, s, 2, 1, proto.Str(proto.S("tcdeck_test")), proto.Guid(zeroGUID), proto.Guid(card.GUID))
	first := runTradingAlbumRequest(t, s, add, false)
	stored, _ := db.GetItemByGUID(c, card.GUID)
	if stored.Container != "card_deck" || stored.RigGUID == "" {
		t.Fatal("card must move out of loose inventory")
	}
	deckGUID := stored.RigGUID
	response, _, _ := proto.DecodeValue(first, 0, s.Table())
	if list := response.Fields[1].Val.Items[0].Fields[0].Val; list.Code == nil || *list.Code != "362" {
		t.Fatal("album delta must contain a card instance array")
	}
	if len(response.Fields[1].Val.Items) != 2 {
		t.Fatal("response must update both the album and its original card")
	}
	moved := response.Fields[1].Val.Items[1]
	if moved.TypeID != 11301 || moved.Fields[1].Hex != card.GUID || moved.Fields[2].Hex != deckGUID || moved.Fields[3].U32() != 18 {
		t.Fatal("response must move the same card into its parent album in collection storage")
	}
	// Existing saves contain the previous numeric array code. Loading them must
	// project a card instance array without rewriting or replacing the card.
	legacyDeck, _ := db.GetItemByGUID(c, deckGUID)
	legacyFields, _ := FieldsFromJSON(legacyDeck.FieldsJSON)
	legacyFields[0].Val.Code = proto.S("363")
	legacyJSON, _ := FieldsToJSON(legacyFields)
	if err := db.UpdateItemFields(c, deckGUID, legacyJSON, legacyDeck.Qty,
		legacyDeck.Durability, legacyDeck.Workmanship, legacyDeck.RigGUID); err != nil {
		t.Fatal(err)
	}
	s = newFishingRecoveryServer(t, db)
	decks, err := s.playerTradingDecks(c, 7)
	if err != nil || len(decks.Items) != 1 || len(decks.Items[0].Fields[0].Val.Items) != 1 {
		t.Fatal("album must survive server recreation", err)
	}
	if *decks.Code != "364" || *decks.Items[0].Fields[0].Val.Code != "362" {
		t.Fatal("profile and inventory must project the instance array types")
	}
	legacyDeck, _ = db.GetItemByGUID(c, deckGUID)
	if legacyDeck.FieldsJSON != legacyJSON {
		t.Fatal("reading an old album must not mutate its stored payload")
	}
	nested := decks.Items[0].Fields[0].Val.Items[0]
	gotFish, _ := proto.EncodeValue(nested.Fields[0].Val, s.Table())
	if nested.Fields[1].Hex != card.GUID || !bytes.Equal(gotFish, original) {
		t.Fatal("album must preserve the original card and catch")
	}
	runTradingAlbumRequest(t, s, tradingAlbumRequest(t, s, 3, 2, proto.Guid(card.GUID)), false)
	stored, _ = db.GetItemByGUID(c, card.GUID)
	fields, _ = FieldsFromJSON(stored.FieldsJSON)
	gotFish, _ = proto.EncodeValue(fields[0].Val, s.Table())
	if stored.Container != "native:18" || stored.RigGUID != "" || fields[2].Hex != "" || fields[3].U32() != 18 || !bytes.Equal(gotFish, original) {
		t.Fatal("removal must return the same unmodified card")
	}
	replay := runTradingAlbumRequest(t, s, tradingAlbumRequest(t, s, 2, 1,
		proto.Str(proto.S("tcdeck_test")), proto.Guid(zeroGUID), proto.Guid(card.GUID)), false)
	stored, _ = db.GetItemByGUID(c, card.GUID)
	if !bytes.Equal(first, replay) || stored.Container != "native:18" {
		t.Fatal("replaying old add must not undo the subsequent removal")
	}
	deck, _ := db.GetItemByGUID(c, deckGUID)
	cards, err := cardsInDeck(deck)
	if err != nil || len(cards) != 0 {
		t.Fatal("album must be empty after removal", err)
	}
	// Discarding a parent album must not strand its original card row.
	runTradingAlbumRequest(t, s, tradingAlbumRequest(t, s, 2, 3,
		proto.Str(proto.S("tcdeck_test")), proto.Guid(deckGUID), proto.Guid(card.GUID)), false)
	if _, _, err := s.removeItemsAndRepairRigs(c, 7, []string{deckGUID}); err != nil {
		t.Fatal(err)
	}
	stored, _ = db.GetItemByGUID(c, card.GUID)
	if stored.Container != "native:18" || stored.RigGUID != "" {
		t.Fatal("discarded album must return its card")
	}
}

func TestTradingAlbumRejectsInapplicableDuplicateAndForeignCards(t *testing.T) {
	s, db := newProgressSrv(t)
	seedTradingCards(t, db)
	c := t.Context()
	makeCard := func(uid int64, id string) *store.Item {
		it, err := s.CreateItem(c, uid, id, "backpack", 1)
		if err != nil {
			t.Fatal(err)
		}
		return it
	}
	first, duplicate, foreign := makeCard(7, "card_perch"), makeCard(7, "card_perch"), makeCard(8, "card_crucian")
	runTradingAlbumRequest(t, s, tradingAlbumRequest(t, s, 2, 1,
		proto.Str(proto.S("tcdeck_test")), proto.Guid(zeroGUID), proto.Guid(first.GUID)), false)
	for i, card := range []*store.Item{duplicate, foreign} {
		runTradingAlbumRequest(t, s, tradingAlbumRequest(t, s, 2, uint32(i+2),
			proto.Str(proto.S("tcdeck_test")), proto.Guid(zeroGUID), proto.Guid(card.GUID)), true)
		row, _ := db.GetItemByGUID(c, card.GUID)
		if row.Container != "native:18" {
			t.Fatal("rejected card was moved")
		}
	}
	if _, err := db.SQL.Exec(`INSERT INTO item_def(numeric_id,item_id,category,attrs_json)
		VALUES(60004,'tcdeck_other',82,'{"cjmjfnheidh":["card_crucian"]}')`); err != nil {
		t.Fatal(err)
	}
	runTradingAlbumRequest(t, s, tradingAlbumRequest(t, s, 2, 4,
		proto.Str(proto.S("tcdeck_other")), proto.Guid(zeroGUID), proto.Guid(duplicate.GUID)), true)
	items, _ := tradingInventory(c, db.SQL, 7)
	if len(items) != 3 {
		t.Fatal("rejected operations must not leave empty albums")
	}
	runTradingAlbumRequest(t, s, tradingAlbumRequest(t, s, 2, 5, proto.Guid(first.GUID)), true)
}

func TestTradingAlbumCollectionBootstrap(t *testing.T) {
	s, db := newProgressSrv(t)
	seedTradingCards(t, db)
	c := t.Context()
	var cards []*store.Item
	deckGUID := zeroGUID
	for i, key := range []string{"card_perch", "card_crucian"} {
		card, err := s.CreateItem(c, 7, key, "backpack", 1)
		if err != nil {
			t.Fatal(err)
		}
		body := runTradingAlbumRequest(t, s, tradingAlbumRequest(t, s, 2, uint32(i+1),
			proto.Str(proto.S("tcdeck_test")), proto.Guid(deckGUID), proto.Guid(card.GUID)), false)
		response, _, _ := proto.DecodeValue(body, 0, s.Table())
		deckGUID = response.Fields[1].Val.Items[0].Fields[2].Hex
		cards = append(cards, card)
	}
	// Login reloads storage 18 through count, GUID paging and item details.
	// A profile's nested album snapshot alone cannot populate these slots.
	s = newFishingRecoveryServer(t, db)
	request := func(values ...*proto.Value) *Ctx {
		ctx := nativeInventoryCtx(s, containerTradingCards, values...)
		ctx.Sess.Userid = 7
		return ctx
	}
	decode := func(body []byte, ok bool) *proto.Value {
		t.Helper()
		v, end, err := proto.DecodeValue(body, 0, s.Table())
		if !ok || err != nil || end != len(body) {
			t.Fatalf("collection response: handled=%t end=%d err=%v", ok, end, err)
		}
		return v
	}
	if got := decode(hItemCount(request())).Fields[0].U32(); got != 3 {
		t.Fatalf("collection must count album and both parented cards: %d", got)
	}
	guids := decode(hItemGUIDs(request(proto.U32(0), proto.U32(500))))
	if len(guids.Items) != 3 {
		t.Fatalf("collection must list album and both parented cards: %d", len(guids.Items))
	}
	details := decode(hItemDetails(request(guids)))
	byGUID := map[string]*proto.Value{}
	for _, item := range details.Items {
		byGUID[item.Fields[fieldIdxByCS(item.Class, csGUID)].Hex] = item
	}
	if len(byGUID) != 3 || byGUID[deckGUID] == nil {
		t.Fatal("collection details must contain the album and its cards")
	}
	for _, card := range cards {
		tree := byGUID[card.GUID]
		if tree == nil || tree.TypeID != 11301 || tree.Fields[2].Hex != deckGUID || tree.Fields[3].U32() != 18 {
			t.Fatal("login must retain each album card as a parented collection item")
		}
	}
	if len(byGUID[deckGUID].Fields[0].Val.Items) != 2 {
		t.Fatal("profile snapshot must agree with both parented cards")
	}
	body := runTradingAlbumRequest(t, s, tradingAlbumRequest(t, s, 3, 3, proto.Guid(cards[0].GUID)), false)
	response, _, _ := proto.DecodeValue(body, 0, s.Table())
	returned := response.Fields[1].Val.Items[1]
	if returned.Fields[1].Hex != cards[0].GUID || returned.Fields[2].Hex != "" || returned.Fields[3].U32() != 18 {
		t.Fatal("removal must move the original card back to loose collection storage")
	}
	if got := decode(hItemCount(request())).Fields[0].U32(); got != 3 {
		t.Fatal("album operations must not consume collection cards")
	}
}

func TestTradingAlbumReceiptFailureRollsBack(t *testing.T) {
	s, db := newProgressSrv(t)
	seedTradingCards(t, db)
	card, err := s.CreateItem(t.Context(), 7, "card_perch", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`CREATE TRIGGER fail_album_receipt BEFORE INSERT ON kv
		WHEN NEW.k LIKE 'card-album:%' BEGIN SELECT RAISE(ABORT,'test rollback'); END`); err != nil {
		t.Fatal(err)
	}
	runTradingAlbumRequest(t, s, tradingAlbumRequest(t, s, 2, 1,
		proto.Str(proto.S("tcdeck_test")), proto.Guid(zeroGUID), proto.Guid(card.GUID)), true)
	items, _ := tradingInventory(t.Context(), db.SQL, 7)
	stored, _ := db.GetItemByGUID(t.Context(), card.GUID)
	if len(items) != 1 || stored.Container != "native:18" || stored.RigGUID != "" || stored.FieldsJSON != card.FieldsJSON {
		t.Fatal("failed receipt must roll back album creation and card move")
	}
}
