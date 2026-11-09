package gameserver

import (
	"context"
	"encoding/json"
	"testing"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

func TestMailSnapshotRoundTripPreservesItemInstance(t *testing.T) {
	t.Parallel()
	srv, db := newEconomyTxTestServer(t)
	ctx := context.Background()
	sender, recipient := int64(2301), int64(2302)
	seedEconomyItemDef(t, db, 9401, "mail_snapshot_float", 28)
	for _, uid := range []int64{sender, recipient} {
		if _, err := db.EnsureProfile(ctx, uid); err != nil {
			t.Fatal(err)
		}
	}
	source := economyTestItem(t, srv, sender, "mail_snapshot_float", "backpack", 1)
	if _, err := db.SQL.Exec(`UPDATE item
		SET durability=?, workmanship=?, buy_price=?, slot=?
		WHERE guid=?`, 0.37, 0.62, 88, 4, source.GUID); err != nil {
		t.Fatal(err)
	}
	source, err := db.GetItemByGUID(ctx, source.GUID)
	if err != nil {
		t.Fatal(err)
	}
	sendCtx := economyTestCtx(srv, sender, proto.Args524(
		proto.U32(uint32(recipient)), proto.U32(1), proto.Guid(source.GUID)))
	if body, ok := hMailSendItem(sendCtx); !ok || len(body) == 0 || body[0] == 0x0F {
		t.Fatalf("19/2 寄件应成功: ok=%v body=%x", ok, body)
	}

	var mailID, snapshotVersion int64
	var raw string
	if err := db.SQL.QueryRow(`SELECT id, items_json, attachment_snapshot_version
		FROM mail WHERE uid=? AND sender=?`, recipient, sender).Scan(&mailID, &raw, &snapshotVersion); err != nil {
		t.Fatal(err)
	}
	if snapshotVersion != mailItemSnapshotVersion {
		t.Fatalf("附件快照版本=%d，应为 %d", snapshotVersion, mailItemSnapshotVersion)
	}
	var attachments []store.MailItem
	if err := json.Unmarshal([]byte(raw), &attachments); err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 1 {
		t.Fatalf("附件数量=%d", len(attachments))
	}
	snap := attachments[0]
	if !snap.HasItemSnapshot() || snap.ItemID != source.ItemID || snap.GUID != source.GUID ||
		snap.WireClass != source.WireClass || snap.FieldsJSON != source.FieldsJSON ||
		snap.NumericID != source.NumericID || snap.TypeID != source.TypeID ||
		snap.Container != source.Container || snap.BuyPrice != source.BuyPrice ||
		snap.Durability != source.Durability || snap.Workmanship != source.Workmanship ||
		snap.CreatedTS != source.CreatedTS {
		t.Fatalf("快照未完整保存源实例:\nsource=%+v\nsnap=%+v", source, snap)
	}

	mail, err := db.MailGet(ctx, recipient, mailID)
	if err != nil {
		t.Fatal(err)
	}
	if mail.ItemsJSON != raw {
		t.Fatal("展示/领取读取的 items_json 与落库快照不一致")
	}
	acceptCtx := economyTestCtx(srv, recipient, proto.Args524(proto.Guid(mailIDToGUID(mailID))))
	if body, ok := hMailAccept(acceptCtx); !ok || len(body) != 1 {
		t.Fatalf("19/3 领取应回 null: ok=%v body=%x", ok, body)
	}
	got, err := db.GetItemByGUID(ctx, source.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.UID != recipient || got.Container != "backpack" ||
		got.FieldsJSON != source.FieldsJSON || got.Durability != source.Durability ||
		got.Workmanship != source.Workmanship || got.BuyPrice != source.BuyPrice ||
		got.CreatedTS != source.CreatedTS {
		t.Fatalf("领取后实例属性未完整还原:\nsource=%+v\ngot=%+v", source, got)
	}
}

func TestMailAttachmentWithoutSnapshotFallsBackToDefinition(t *testing.T) {
	t.Parallel()
	srv, db := newEconomyTxTestServer(t)
	ctx := context.Background()
	recipient := int64(2311)
	seedEconomyItemDef(t, db, 9411, "mail_definition_float", 28)
	if _, err := db.EnsureProfile(ctx, recipient); err != nil {
		t.Fatal(err)
	}
	guid := "bb000000000000000000000000000001"
	raw, err := json.Marshal([]store.MailItem{{GUID: guid, ItemID: "mail_definition_float", Qty: 1}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.SQL.Exec(`INSERT INTO mail(uid,sender,kind,text,items_json,created_ts,read)
		VALUES(?,0,1,'',?,0,0)`, recipient, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	mailID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	mail, err := db.MailGet(ctx, recipient, mailID)
	if err != nil {
		t.Fatal(err)
	}
	if mail.AttachmentSnapshotVersion != 0 {
		t.Fatalf("无快照附件版本=%d，应为 0", mail.AttachmentSnapshotVersion)
	}
	if buildMail11062(mail) == nil {
		t.Fatal("无快照附件展示失败")
	}
	acceptCtx := economyTestCtx(srv, recipient, proto.Args524(proto.Guid(mailIDToGUID(mailID))))
	if body, ok := hMailAccept(acceptCtx); !ok || len(body) != 1 {
		t.Fatalf("无快照附件领取应回 null: ok=%v body=%x", ok, body)
	}
	got, err := db.GetItemByGUID(ctx, guid)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.UID != recipient || got.ItemID != "mail_definition_float" ||
		got.WireClass == "" || got.FieldsJSON == "" {
		t.Fatalf("无快照附件未按定义回退重建: %+v", got)
	}
}

func TestMailSnapshotClaimFailureRollsBack(t *testing.T) {
	t.Parallel()
	srv, db := newEconomyTxTestServer(t)
	ctx := context.Background()
	sender, recipient := int64(2321), int64(2322)
	seedEconomyItemDef(t, db, 9421, "mail_snapshot_a", 27)
	seedEconomyItemDef(t, db, 9422, "mail_snapshot_b", 27)
	for _, uid := range []int64{sender, recipient} {
		if _, err := db.EnsureProfile(ctx, uid); err != nil {
			t.Fatal(err)
		}
	}
	a := economyTestItem(t, srv, sender, "mail_snapshot_a", "backpack", 1)
	b := economyTestItem(t, srv, sender, "mail_snapshot_b", "backpack", 1)
	aSnap, err := mailItemSnapshotFromRow(a, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	bSnap, err := mailItemSnapshotFromRow(b, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal([]store.MailItem{aSnap, bSnap})
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.SQL.Exec(`INSERT INTO mail(uid,sender,kind,text,items_json,attachment_snapshot_version,created_ts,read)
		VALUES(?,?,1,'',?,?,0,0)`, recipient, sender, string(raw), mailItemSnapshotVersion)
	if err != nil {
		t.Fatal(err)
	}
	mailID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`CREATE TRIGGER mail_snapshot_fail_b BEFORE INSERT ON item
		WHEN NEW.item_id='mail_snapshot_b' BEGIN SELECT RAISE(ABORT,'boom'); END`); err != nil {
		t.Fatal(err)
	}
	acceptCtx := economyTestCtx(srv, recipient, proto.Args524(proto.Guid(mailIDToGUID(mailID))))
	_, _ = hMailAccept(acceptCtx)
	var granted int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM item
		WHERE uid=? AND item_id IN ('mail_snapshot_a','mail_snapshot_b')`, recipient).Scan(&granted); err != nil {
		t.Fatal(err)
	}
	var read, version int
	var current string
	if err := db.SQL.QueryRow(`SELECT read, attachment_snapshot_version, items_json
		FROM mail WHERE id=?`, mailID).Scan(&read, &version, &current); err != nil {
		t.Fatal(err)
	}
	if granted != 0 || read != 0 || version != int(mailItemSnapshotVersion) || current != string(raw) {
		t.Fatalf("领取失败必须整批回滚: granted=%d read=%d version=%d raw_changed=%v",
			granted, read, version, current != string(raw))
	}
}

func TestMailSnapshotCountableAttachmentReassignsGUID(t *testing.T) {
	t.Parallel()
	srv, db := newEconomyTxTestServer(t)
	ctx := context.Background()
	sender, recipient := int64(2331), int64(2332)
	seedEconomyItemDef(t, db, 9431, "mail_snapshot_bait", 27)
	for _, uid := range []int64{sender, recipient} {
		if _, err := db.EnsureProfile(ctx, uid); err != nil {
			t.Fatal(err)
		}
	}
	source := economyTestItem(t, srv, sender, "mail_snapshot_bait", "backpack", 2)
	sendCtx := economyTestCtx(srv, sender, proto.Args524(
		proto.U32(uint32(recipient)), proto.U32(1), proto.Guid(source.GUID)))
	if body, ok := hMailSendItem(sendCtx); !ok || len(body) == 0 || body[0] == 0x0F {
		t.Fatalf("计件附件寄件应成功: ok=%v body=%x", ok, body)
	}
	var mailID int64
	if err := db.SQL.QueryRow(`SELECT id FROM mail WHERE uid=? AND sender=?`, recipient, sender).Scan(&mailID); err != nil {
		t.Fatal(err)
	}
	acceptCtx := economyTestCtx(srv, recipient, proto.Args524(proto.Guid(mailIDToGUID(mailID))))
	if body, ok := hMailAccept(acceptCtx); !ok || len(body) != 1 {
		t.Fatalf("计件附件领取应成功: ok=%v body=%x", ok, body)
	}
	var receivedGUID string
	if err := db.SQL.QueryRow(`SELECT guid FROM item WHERE uid=? AND item_id=?`, recipient, source.ItemID).Scan(&receivedGUID); err != nil {
		t.Fatal(err)
	}
	if receivedGUID == source.GUID {
		t.Fatalf("计件附件领取应重分配 GUID，仍为源 GUID %s", source.GUID)
	}
	got, err := db.GetItemByGUID(ctx, receivedGUID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Qty != 1 || got.FieldsJSON == "" {
		t.Fatalf("计件附件快照领取结果不完整: %+v", got)
	}
}

func TestMailTextAcceptAndAcceptAllMarkRead(t *testing.T) {
	t.Parallel()
	srv, db := newEconomyTxTestServer(t)
	ctx := context.Background()
	uid := int64(2341)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	insertTextMail := func(text string) int64 {
		t.Helper()
		res, err := db.SQL.Exec(`INSERT INTO mail(uid,sender,kind,text,items_json,created_ts,read)
			VALUES(?,0,0,?,NULL,0,0)`, uid, text)
		if err != nil {
			t.Fatal(err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	firstID := insertTextMail("single")
	acceptCtx := economyTestCtx(srv, uid, proto.Args524(proto.Guid(mailIDToGUID(firstID))))
	if body, ok := hMailAccept(acceptCtx); !ok || len(body) != 1 {
		t.Fatalf("19/3 纯文本信应回 null: ok=%v body=%x", ok, body)
	}
	var firstRead int
	if err := db.SQL.QueryRow(`SELECT read FROM mail WHERE id=?`, firstID).Scan(&firstRead); err != nil {
		t.Fatal(err)
	}
	if firstRead != 1 {
		t.Fatalf("19/3 纯文本信未标记已读: read=%d", firstRead)
	}

	secondID := insertTextMail("all")
	allCtx := economyTestCtx(srv, uid, proto.Args524())
	if body, ok := hMailAcceptAll(allCtx); !ok || len(body) != 1 {
		t.Fatalf("19/4 应回 null: ok=%v body=%x", ok, body)
	}
	var secondRead int
	if err := db.SQL.QueryRow(`SELECT read FROM mail WHERE id=?`, secondID).Scan(&secondRead); err != nil {
		t.Fatal(err)
	}
	if secondRead != 1 {
		t.Fatalf("19/4 纯文本信未标记已读: read=%d", secondRead)
	}
}

func TestMailAttachmentRepeatedAcceptIsIdempotent(t *testing.T) {
	t.Parallel()
	srv, db := newEconomyTxTestServer(t)
	ctx := context.Background()
	sender, recipient := int64(2351), int64(2352)
	seedEconomyItemDef(t, db, 9451, "mail_repeat_float", 28)
	for _, uid := range []int64{sender, recipient} {
		if _, err := db.EnsureProfile(ctx, uid); err != nil {
			t.Fatal(err)
		}
	}
	source := economyTestItem(t, srv, sender, "mail_repeat_float", "backpack", 1)
	sendCtx := economyTestCtx(srv, sender, proto.Args524(
		proto.U32(uint32(recipient)), proto.U32(1), proto.Guid(source.GUID)))
	if body, ok := hMailSendItem(sendCtx); !ok || len(body) == 0 || body[0] == 0x0F {
		t.Fatalf("附件寄件应成功: ok=%v body=%x", ok, body)
	}
	var mailID int64
	if err := db.SQL.QueryRow(`SELECT id FROM mail WHERE uid=? AND sender=?`, recipient, sender).Scan(&mailID); err != nil {
		t.Fatal(err)
	}
	acceptCtx := economyTestCtx(srv, recipient, proto.Args524(proto.Guid(mailIDToGUID(mailID))))
	if body, ok := hMailAccept(acceptCtx); !ok || len(body) != 1 {
		t.Fatalf("首次领取应成功: ok=%v body=%x", ok, body)
	}
	if body, ok := hMailAccept(acceptCtx); !ok || len(body) != 1 {
		t.Fatalf("重复领取应稳定回 null: ok=%v body=%x", ok, body)
	}
	var count int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM item WHERE uid=? AND item_id=?`,
		recipient, source.ItemID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("重复领取不应重复发件: count=%d", count)
	}
}
