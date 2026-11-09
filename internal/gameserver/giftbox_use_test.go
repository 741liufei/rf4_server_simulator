package gameserver

import (
	"context"
	"testing"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// 礼盒（ieopbeejfmc=true）购买/领附件保留本体、4/8 使用拆开的分级行为。
// SKU 用真实静态库条目：box_feeder_pro（礼盒）、box_food_egg_10（饵料盒）。

const (
	giftBoxID     = "box_feeder_pro"
	baitBoxID     = "box_food_egg_10"
	giftBoxStore  = "giftbox_store"
	giftBoxSilver = 100
)

func giftBoxTestCtx(t *testing.T, srv *Server, uid int64, tree *proto.Value) *Ctx {
	t.Helper()
	c := economyTestCtx(srv, uid, tree)
	c.ExpectResponse = true
	c.Sess.closed.Store(false)
	return c
}

func giftBoxItemCount(t *testing.T, srv *Server, uid int64, itemID string) int64 {
	t.Helper()
	var n int64
	if err := srv.DB.SQL.QueryRow(`SELECT COUNT(*) FROM item WHERE uid=? AND item_id=?`, uid, itemID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

type giftBoxRowProbe struct {
	guid      string
	wireClass string
	typeID    int64
	container string
}

func giftBoxRows(t *testing.T, srv *Server, uid int64, itemID string) []giftBoxRowProbe {
	t.Helper()
	rs, err := srv.DB.SQL.Query(`SELECT guid, wire_class, type_id, container FROM item WHERE uid=? AND item_id=?`, uid, itemID)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	var out []giftBoxRowProbe
	for rs.Next() {
		var p giftBoxRowProbe
		if err := rs.Scan(&p.guid, &p.wireClass, &p.typeID, &p.container); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	if err := rs.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

var giftBoxFeederProContents = []string{
	"feeder_10126_FD390", "spin_5184", "mono_15097", "feeder_30037", "clas_20163", "feeder_bell",
}

// TestGiftBoxPurchaseStaysBoxed 礼盒购买只入库礼盒本体（3100），不展开内容物；
// 饵料盒购买维持官服"购买即展开"（真实报文实证路径，不回退）。
func TestGiftBoxPurchaseStaysBoxed(t *testing.T) {
	t.Parallel()
	srv, db := newBundleTestServer(t)
	ctx := context.Background()
	const uid = int64(4201)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE profile SET silver=1000 WHERE uid=?`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO shop_item(store_id,item_id,silver,gold,stock) VALUES(?,?,?,0,10)`,
		giftBoxStore, giftBoxID, giftBoxSilver); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO shop_item(store_id,item_id,silver,gold,stock) VALUES(?,?,50,0,10)`,
		giftBoxStore, baitBoxID); err != nil {
		t.Fatal(err)
	}

	c := economyTestCtx(srv, uid, proto.Args524(
		proto.StrVal(giftBoxStore), proto.StrVal(giftBoxID), proto.U64(0)))
	c.ExpectResponse = true
	c.replyCapture = &ctxReplyCapture{}
	body, ok := hStoreBuy(false)(c)
	_, present, _, failure := c.replyCapture.snapshot()
	if !ok || failure || present || len(body) == 0 {
		t.Fatalf("礼盒购买失败: ok=%v failure=%v present=%v", ok, failure, present)
	}
	var silver int64
	if err := db.SQL.QueryRow(`SELECT silver FROM profile WHERE uid=?`, uid).Scan(&silver); err != nil {
		t.Fatal(err)
	}
	if silver != 1000-giftBoxSilver {
		t.Errorf("礼盒扣款不符: silver=%d", silver)
	}
	rows := giftBoxRows(t, srv, uid, giftBoxID)
	if len(rows) != 1 {
		t.Fatalf("礼盒应只入库一件本体，实得 %d 行", len(rows))
	}
	if rows[0].wireClass != "coacllgmnml" || rows[0].typeID != 3100 || rows[0].container != "backpack" {
		t.Errorf("礼盒本体线形态不符: class=%s type=%d container=%s", rows[0].wireClass, rows[0].typeID, rows[0].container)
	}
	for _, content := range giftBoxFeederProContents {
		if n := giftBoxItemCount(t, srv, uid, content); n != 0 {
			t.Errorf("礼盒内容物 %s 不应随购买入库，实得 %d 行", content, n)
		}
	}

	// 饵料盒回归：购买即展开，不落礼盒本体。
	c2 := economyTestCtx(srv, uid, proto.Args524(
		proto.StrVal(giftBoxStore), proto.StrVal(baitBoxID), proto.U64(0)))
	c2.ExpectResponse = true
	if body, ok := hStoreBuy(false)(c2); !ok || len(body) == 0 {
		t.Fatalf("饵料盒购买失败: ok=%v", ok)
	}
	if n := giftBoxItemCount(t, srv, uid, baitBoxID); n != 0 {
		t.Errorf("饵料盒不应以本体入库，实得 %d 行", n)
	}
	if n := giftBoxItemCount(t, srv, uid, "food_egg"); n != 1 {
		t.Errorf("饵料盒应展开为 food_egg 一行，实得 %d 行", n)
	}
}

// TestGiftBoxUseOpensBundle 4/8 使用礼盒：礼盒行删除、6 件内容物入库，
// 响应 800 f0 携带内容物树、f2 携带礼盒 GUID。
func TestGiftBoxUseOpensBundle(t *testing.T) {
	t.Parallel()
	srv, db := newBundleTestServer(t)
	ctx := context.Background()
	const uid = int64(4202)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	row, err := srv.prepareNewItem(ctx, uid, giftBoxID, "backpack", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertItem(ctx, row); err != nil {
		t.Fatal(err)
	}

	c := giftBoxTestCtx(t, srv, uid, proto.Args524(proto.U64(0), proto.Guid(row.GUID)))
	body, ok := hItemUse(c)
	if !ok || len(body) == 0 {
		t.Fatalf("礼盒使用无响应: ok=%v", ok)
	}
	value, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	if value.Class != "dlbahppbdad" {
		t.Fatalf("响应类不符: %s", value.Class)
	}
	f0, f2 := value.Fields[0].Val, value.Fields[2].Val
	if f0 == nil || f0.Kind != proto.KindArgs || len(f0.Items) != 6 {
		t.Fatalf("800.f0 应携带 6 件内容物，实得 kind=%v n=%d", f0.Kind, len(f0.Items))
	}
	if f2 == nil || f2.Kind != proto.KindGuidArr || len(f2.Items) != 1 || f2.Items[0].Hex != row.GUID {
		t.Fatalf("800.f2 应携带礼盒 GUID，实得 kind=%v items=%v", f2.Kind, f2.Items)
	}
	if n := giftBoxItemCount(t, srv, uid, giftBoxID); n != 0 {
		t.Errorf("礼盒使用后仍残留 %d 行", n)
	}
	for _, content := range giftBoxFeederProContents {
		if n := giftBoxItemCount(t, srv, uid, content); n != 1 {
			t.Errorf("内容物 %s 应入库 1 行，实得 %d 行", content, n)
		}
	}
}

// TestGiftBoxUseAbsoluteQuantityEntry 4/8 拆开 mlapkoadfnh=true 的绝对个数条目
// 应按条目数发放，不乘子物定义默认量。实机事故回归：box_worm_asurv_100 曾误发
// 30（worm_asurv def ljnnpokapig）×100 = 3000，官服语义应为一堆 100。
func TestGiftBoxUseAbsoluteQuantityEntry(t *testing.T) {
	srv, db := newBundleTestServer(t)
	ctx := context.Background()
	const uid = int64(4205)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	row, err := srv.prepareNewItem(ctx, uid, "box_worm_asurv_100", "backpack", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertItem(ctx, row); err != nil {
		t.Fatal(err)
	}

	c := giftBoxTestCtx(t, srv, uid, proto.Args524(proto.U64(0), proto.Guid(row.GUID)))
	body, ok := hItemUse(c)
	if !ok || len(body) == 0 {
		t.Fatalf("蚯蚓盒使用无响应: ok=%v", ok)
	}
	value, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	f0 := value.Fields[0].Val
	if f0 == nil || f0.Kind != proto.KindArgs || len(f0.Items) != 1 {
		t.Fatalf("800.f0 应携带 1 件内容物，实得 kind=%v n=%v", f0.Kind, len(f0.Items))
	}
	if n := giftBoxItemCount(t, srv, uid, "box_worm_asurv_100"); n != 0 {
		t.Errorf("蚯蚓盒使用后仍残留 %d 行", n)
	}
	rows := giftBoxRows(t, srv, uid, "worm_asurv")
	if len(rows) != 1 {
		t.Fatalf("应入库一行 worm_asurv，实得 %d 行", len(rows))
	}
	granted, err := db.GetItemByGUID(ctx, rows[0].guid)
	if err != nil || granted == nil {
		t.Fatalf("读取入库行: %v", err)
	}
	assertBundleItemQuantity(t, granted, 100)
}

// TestGiftBoxUseRejectsOutsideBackpack 非背包容器中的礼盒拒绝拆开且不产生任何变更。
func TestGiftBoxUseRejectsOutsideBackpack(t *testing.T) {
	t.Parallel()
	srv, db := newBundleTestServer(t)
	ctx := context.Background()
	const uid = int64(4203)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	row, err := srv.prepareNewItem(ctx, uid, giftBoxID, "car", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertItem(ctx, row); err != nil {
		t.Fatal(err)
	}

	c := giftBoxTestCtx(t, srv, uid, proto.Args524(proto.U64(0), proto.Guid(row.GUID)))
	c.replyCapture = &ctxReplyCapture{}
	body, ok := hItemUse(c)
	_, present, _, failure := c.replyCapture.snapshot()
	if !ok || body != nil || !present || !failure {
		t.Fatalf("非背包礼盒应走失败回执: ok=%v body=%v present=%v failure=%v", ok, body, present, failure)
	}
	if n := giftBoxItemCount(t, srv, uid, giftBoxID); n != 1 {
		t.Errorf("拒绝后礼盒不应消失，实得 %d 行", n)
	}
}

// TestGiftBoxUseFallsThroughForNormalItem 普通物品的 4/8 不被礼盒分支拦截，落回通用回显。
func TestGiftBoxUseFallsThroughForNormalItem(t *testing.T) {
	t.Parallel()
	srv, db := newBundleTestServer(t)
	ctx := context.Background()
	const uid = int64(4204)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	row, err := srv.prepareNewItem(ctx, uid, "mono_15065", "backpack", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertItem(ctx, row); err != nil {
		t.Fatal(err)
	}

	c := giftBoxTestCtx(t, srv, uid, proto.Args524(proto.U64(0), proto.Guid(row.GUID)))
	c.replyCapture = &ctxReplyCapture{}
	body, ok := hItemUse(c)
	_, present, _, failure := c.replyCapture.snapshot()
	if !ok || len(body) == 0 || present || failure {
		t.Fatalf("普通物品使用应回 800 回显: ok=%v failure=%v present=%v", ok, failure, present)
	}
	if n := giftBoxItemCount(t, srv, uid, "mono_15065"); n != 1 {
		t.Errorf("普通物品不应被礼盒分支删除，实得 %d 行", n)
	}
}

// TestGiftBoxMailAttachmentStaysBoxed 礼盒邮件附件领取后保留本体（按包数逐件）；
// 饵料盒附件维持官服"领取即展开"。
func TestGiftBoxMailAttachmentStaysBoxed(t *testing.T) {
	t.Parallel()
	srv, db := newBundleTestServer(t)
	ctx := context.Background()
	const uid = int64(4205)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	id, err := db.MailInsert(ctx, uid, 0, 0, "test", []store.MailItem{{ItemID: giftBoxID, Qty: 2}})
	if err != nil {
		t.Fatal(err)
	}
	baitID, err := db.MailInsert(ctx, uid, 0, 0, "test", []store.MailItem{{ItemID: baitBoxID, Qty: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if count, items := srv.acceptMails(giftBoxTestCtx(t, srv, uid, proto.Args524()), []int64{id, baitID}); count != 3 || len(items) != 3 {
		t.Fatalf("领取发放数不符（2 礼盒 + 1 饵料堆）: count=%d items=%d", count, len(items))
	}
	if rows := giftBoxRows(t, srv, uid, giftBoxID); len(rows) != 2 {
		t.Fatalf("礼盒附件应发放 2 件本体，实得 %d 行", len(rows))
	}
	if n := giftBoxItemCount(t, srv, uid, "feeder_10126_FD390"); n != 0 {
		t.Errorf("礼盒附件不应展开内容物，实得 feeder_10126_FD390 %d 行", n)
	}
	if n := giftBoxItemCount(t, srv, uid, baitBoxID); n != 0 {
		t.Errorf("饵料盒附件不应以本体入库，实得 %d 行", n)
	}
	if n := giftBoxItemCount(t, srv, uid, "food_egg"); n != 1 {
		t.Errorf("饵料盒附件应展开为 food_egg 一行，实得 %d 行", n)
	}
}

// TestGiftBoxMailAttachmentWithBuyPriceStaysBoxed 覆盖与 #288 组合后的路径：
// 商店购买会给礼盒写入 buy_price，邮件快照会携带该值；开盒礼盒仍应可领取。
func TestGiftBoxMailAttachmentWithBuyPriceStaysBoxed(t *testing.T) {
	t.Parallel()
	srv, db := newBundleTestServer(t)
	ctx := context.Background()
	const uid = int64(4206)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	row, err := srv.prepareNewItem(ctx, uid, giftBoxID, "backpack", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	row.BuyPrice = 123
	att, err := mailItemSnapshotFromRow(row, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	id, err := db.MailInsert(ctx, uid, 0, 0, "test", []store.MailItem{att})
	if err != nil {
		t.Fatal(err)
	}
	if count, items := srv.acceptMails(giftBoxTestCtx(t, srv, uid, proto.Args524()), []int64{id}); count != 1 || len(items) != 1 {
		t.Fatalf("购买价值礼盒应可领取本体: count=%d items=%d", count, len(items))
	}
	if rows := giftBoxRows(t, srv, uid, giftBoxID); len(rows) != 1 {
		t.Fatalf("应领取 1 件礼盒本体，实得 %d 行", len(rows))
	}
	if n := giftBoxItemCount(t, srv, uid, "feeder_10126_FD390"); n != 0 {
		t.Errorf("礼盒附件不应自动展开，实得内容物 %d 行", n)
	}
}

// TestGiftBoxMailAttachmentQtyCap 防止损坏附件用超大 Qty 触发无界构造。
func TestGiftBoxMailAttachmentQtyCap(t *testing.T) {
	t.Parallel()
	srv, db := newBundleTestServer(t)
	ctx := context.Background()
	const uid = int64(4207)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	id, err := db.MailInsert(ctx, uid, 0, 0, "test", []store.MailItem{{
		ItemID: giftBoxID, Qty: maxBundlePreparedItems + 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if count, items := srv.acceptMails(giftBoxTestCtx(t, srv, uid, proto.Args524()), []int64{id}); count != 0 || len(items) != 0 {
		t.Fatalf("超限礼盒附件不应领取: count=%d items=%d", count, len(items))
	}
	assertBundleMailUnclaimed(t, db, uid, id)
	if rows := giftBoxRows(t, srv, uid, giftBoxID); len(rows) != 0 {
		t.Errorf("超限附件不应入库，实得 %d 行", len(rows))
	}
}

// TestGiftBoxUseRollsBackWhenDeleteSkipped 删除未真正发生时不得发放内容物。
func TestGiftBoxUseRollsBackWhenDeleteSkipped(t *testing.T) {
	t.Parallel()
	srv, db := newBundleTestServer(t)
	ctx := context.Background()
	const uid = int64(4208)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	row, err := srv.prepareNewItem(ctx, uid, giftBoxID, "backpack", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertItem(ctx, row); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`CREATE TRIGGER skip_giftbox_delete BEFORE DELETE ON item
		WHEN OLD.item_id='box_feeder_pro' BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	c := giftBoxTestCtx(t, srv, uid, proto.Args524(proto.U64(0), proto.Guid(row.GUID)))
	c.replyCapture = &ctxReplyCapture{}
	body, ok := hItemUse(c)
	_, present, _, failed := c.replyCapture.snapshot()
	if !ok || body != nil || !present || !failed {
		t.Fatalf("删除未发生时礼盒应拒绝开盒: ok=%v body=%d present=%v failed=%v", ok, len(body), present, failed)
	}
	if n := giftBoxItemCount(t, srv, uid, giftBoxID); n != 1 {
		t.Errorf("失败后礼盒应保留，实得 %d 行", n)
	}
	for _, content := range giftBoxFeederProContents {
		if n := giftBoxItemCount(t, srv, uid, content); n != 0 {
			t.Errorf("失败后内容物 %s 不应入库，实得 %d 行", content, n)
		}
	}
}
