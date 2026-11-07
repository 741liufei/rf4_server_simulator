package gameserver

import (
	"bytes"
	"testing"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

func seedTradingCards(t *testing.T, db *store.DB) {
	t.Helper()
	for _, query := range []string{
		`INSERT OR REPLACE INTO item_def(numeric_id,item_id,category,attrs_json) VALUES
		(60001,'card_perch',78,'{"okppoalfakf":"perch"}'),
		(60002,'card_crucian',78,'{"okppoalfakf":"crucian"}'),
		(60003,'tcdeck_test',82,'{"cjmjfnheidh":["card_perch","card_crucian"],"lbdaoenhcbe":1}')`,
		`INSERT OR REPLACE INTO item_catalog(item_id,name_zh) VALUES('card_perch','河鲈卡片'),('card_crucian','鲫鱼卡片'),('tcdeck_test','测试卡册')`,
	} {
		if _, err := db.SQL.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTradingEmptyDeckAndGenericGrant(t *testing.T) {
	s, db := newProgressSrv(t)
	seedTradingCards(t, db)
	it, err := s.CreateItem(t.Context(), 7, "tcdeck_test", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := FieldsFromJSON(it.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	list := fields[0].Val
	if it.Container != "native:18" || fields[4].U32() != 18 {
		t.Fatal("album must belong to the client's collection storage")
	}
	if it.TypeID != 11304 || list == nil || list.Kind != proto.KindArgs || list.Code == nil || *list.Code != "362" || len(list.Items) != 0 {
		t.Fatalf("empty deck must be a typed array: %+v", it)
	}
	card, err := s.CreateItem(t.Context(), 7, "card_perch", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := s.ItemToTree(t.Context(), card)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := proto.EncodeValue(tree, s.Table())
	if err != nil {
		t.Fatal(err)
	}
	decoded, _, err := proto.DecodeValue(raw, 0, s.Table())
	if err != nil {
		t.Fatal(err)
	}
	if decoded.TypeID != 11301 || decoded.Fields[0].Val == nil || decoded.Fields[0].Val.TypeID != 25 || decoded.Fields[0].Val.Fields[0].StrOr() != "perch" {
		t.Fatal("card must contain its fish instance")
	}
	if card.Container != "native:18" || decoded.Fields[3].U32() != 18 {
		t.Fatal("card must belong to the client's collection storage")
	}
	decks, err := s.playerTradingDecks(t.Context(), 7)
	if err != nil || decks.Code == nil || *decks.Code != "364" || len(decks.Items) != 1 {
		t.Fatal("profile must read the inventory's album", err)
	}
}

func TestTradingArrayRegisteredCodes(t *testing.T) {
	// Literal registered codes read from the client resolver, not by_type_id.
	for tid, want := range map[uint32]string{11301: "362", 11304: "364"} {
		if got, ok := ContainerCodeForTypeID(tid); !ok || got != want {
			t.Fatalf("registered array %d = %s want %s", tid, got, want)
		}
	}
	for _, test := range []struct {
		list *proto.Value
		want []byte
	}{
		{tradingCardList(), []byte{3, 3, '3', '6', '2', 0, 0}},
		{tradingDeckList(), []byte{3, 3, '3', '6', '4', 0, 0}},
	} {
		got, err := proto.EncodeValue(test.list, nil)
		if err != nil || !bytes.Equal(got, test.want) {
			t.Fatalf("collection array = %x want %x: %v", got, test.want, err)
		}
	}
}

func TestTradingCollectionInventory(t *testing.T) {
	s, db := newProgressSrv(t)
	seedTradingCards(t, db)
	c := t.Context()
	card, err := s.CreateItem(c, 7, "card_perch", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	deck, err := s.prepareNewItem(c, 7, "tcdeck_test", "backpack", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertItem(c, deck); err != nil {
		t.Fatal(err)
	}
	foreign, err := s.CreateItem(c, 8, "card_perch", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	request := func(kind uint64, args ...*proto.Value) *Ctx {
		ctx := nativeInventoryCtx(s, kind, args...)
		ctx.Sess.Userid = 7
		return ctx
	}
	decode := func(body []byte, ok bool) *proto.Value {
		t.Helper()
		v, end, err := proto.DecodeValue(body, 0, s.Table())
		if !ok || err != nil || end != len(body) {
			t.Fatalf("invalid inventory response: handled=%t err=%v", ok, err)
		}
		return v
	}
	for _, kind := range []uint64{0, 18} {
		want := 0
		if kind == 18 {
			want = 2
		}
		count := decode(hItemCount(request(kind)))
		if count.Fields[0].U32() != uint32(want) {
			t.Fatalf("storage %d count = %d want %d", kind, count.Fields[0].U32(), want)
		}
		guids := decode(hItemGUIDs(request(kind)))
		if len(guids.Items) != want {
			t.Fatalf("storage %d has %d GUIDs, want %d", kind, len(guids.Items), want)
		}
		details := decode(hItemDetails(request(kind, proto.GuidArr([]string{card.GUID, deck.GUID, foreign.GUID}))))
		if len(details.Items) != want {
			t.Fatalf("storage %d has %d item details, want %d", kind, len(details.Items), want)
		}
		for _, item := range details.Items {
			if item.Fields[fieldIdxByCS(item.Class, csSlotEnum)].U32() != 18 {
				t.Fatal("collection item returned with a different StorageType")
			}
		}
	}
}
