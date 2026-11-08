package gameserver

import (
	"testing"
	"time"

	"rf4server/internal/proto"
)

func TestFishQueryContactAndMailReplay(t *testing.T) {
	s, db := newProgressSrv(t)
	const uid int64 = 1000001
	if _, err := db.EnsureProfile(t.Context(), uid); err != nil {
		t.Fatal(err)
	}
	first := newMailTestSession(t, s, 1, uid)
	second := newMailTestSession(t, s, 2, uid+1)
	first.sess.Token = "fish-query-test"
	env := &dlineTestEnv{srv: s, db: db, sess: first.sess}
	members := env.invoke(t, 24, 4, proto.Args524(proto.U32(0), proto.StrVal("")))
	if members.Kind != proto.KindU32Arr || len(members.Items) != 3 || members.Items[0].Num != uint64(fishQueryContactUID) {
		t.Fatalf("contact missing from members: %+v", members)
	}
	card := decodeWrappedBytes(t, env.invoke(t, 24, 19, proto.Args524(proto.U32(uint32(fishQueryContactUID)))))
	if card.TypeID != 2002 || card.Fields[1].StrOr() != fishQueryContactName || card.Fields[3].U32() != 0 {
		t.Fatalf("invalid contact card: %+v", card)
	}
	body, err := proto.EncodeValue(proto.Args524(proto.U32(uint32(fishQueryContactUID)), proto.StrVal("帮助")), s.Table())
	if err != nil {
		t.Fatal(err)
	}
	send := func() {
		s.handleEnvelopePayload(first.sess, proto.BuildRequestEnvelope(601, 19, 1, append(append([]byte(nil), body...), 0)))
		select {
		case response := <-first.envelopes:
			if response.Marker != -2 || response.CallID != 601 || len(response.Body) != 1 || response.Body[0] != 0 {
				t.Fatalf("message acknowledgement must precede reply: %+v", response)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no message acknowledgement")
		}
	}
	send()
	mails, err := db.MailList(t.Context(), uid, false)
	if err != nil || len(mails) != 1 || mails[0].Sender != fishQueryContactUID || mails[0].Text != fishQueryHelp {
		t.Fatalf("query reply not persisted: mails=%+v err=%v", mails, err)
	}
	assertMailTestNotification(t, first, mails[0])
	assertNoMailTestPush(t, second)
	send()
	assertNoMailTestPush(t, first)
	mails, err = db.MailList(t.Context(), uid, false)
	if err != nil || len(mails) != 1 {
		t.Fatalf("replay created another reply: mails=%+v err=%v", mails, err)
	}
	var profiles int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM profile WHERE uid=?`, fishQueryContactUID).Scan(&profiles); err != nil || profiles != 0 {
		t.Fatalf("virtual contact created a player account: count=%d err=%v", profiles, err)
	}
}

func TestFishQueryContactDoesNotTakeItems(t *testing.T) {
	env := newDLineTestEnv(t, 1000001)
	// A valid owned item would be transferred by the regular player-mail path.
	seedEconomyItemDef(t, env.db, 9451, "query_mail_item", 27)
	item := economyTestItem(t, env.srv, env.sess.Userid, "query_mail_item", "backpack", 3)
	env.invoke(t, 19, 2, proto.Args524(proto.U32(uint32(fishQueryContactUID)), proto.U32(1), proto.Guid(item.GUID)))
	var qty int
	if err := env.db.SQL.QueryRow(`SELECT qty FROM item WHERE guid=? AND uid=?`, item.GUID, env.sess.Userid).Scan(&qty); err != nil || qty != 3 {
		t.Fatalf("assistant took player items: qty=%d err=%v", qty, err)
	}
}
