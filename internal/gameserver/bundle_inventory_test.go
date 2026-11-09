package gameserver

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// 展开路径夹具用 ieopbeejfmc 为空的官服即拆类饵料盒；ieopbeejfmc=true 的
// 组合包（原夹具 AV_shamp1_pack_6_8、礼盒 box_feeder_pro）走礼盒路径，
// 见 giftbox_use_test.go。
const testBundleID = "box_food_egg_10"
const testBundleContentsID = "food_egg"

var bundleTestDBTemplate struct {
	once sync.Once
	path string
	err  error
}

// newBundleTestServer 缓存只含静态物品定义的模板，每个测试复制独立数据库。
// 模板准备失败必须使测试失败，不能跳过组合包的行为验证。
//
// newBundleTestServer caches a template containing only static item definitions and
// copies an isolated database for each test. Template failures must fail the test,
// rather than skip bundle behavior verification.
func newBundleTestServer(t *testing.T) (*Server, *store.DB) {
	t.Helper()
	bundleTestDBTemplate.once.Do(func() {
		bundleTestDBTemplate.path = filepath.Join(gameserverTestDBRoot, "bundle-item-def.db")
		bundleTestDBTemplate.err = buildTestDBTemplate(bundleTestDBTemplate.path, []string{"item_def"})
	})
	if bundleTestDBTemplate.err != nil {
		t.Fatalf("准备组合包测试数据库模板: %v", bundleTestDBTemplate.err)
	}
	path := filepath.Join(t.TempDir(), "bundle.db")
	copyTestDBFile(t, bundleTestDBTemplate.path, path)
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("打开组合包测试数据库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "bundle"}}, logging.Discard(), nil)
	srv.SetDB(db)
	return srv, db
}

func legacyBundleMailSnapshot(t *testing.T, srv *Server, uid int64) store.MailItem {
	t.Helper()
	it, err := srv.prepareNewItem(context.Background(), uid, "mono_15065", "backpack", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	def, err := srv.DB.GetItemDefByItemID(context.Background(), testBundleID)
	if err != nil {
		t.Fatal(err)
	}
	it.ItemID, it.NumericID = def.ItemID, def.NumericID
	fields, err := FieldsFromJSON(it.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	fields[fieldIdxByCS(it.WireClass, csNumericID)].Num = ptrU64(uint64(def.NumericID))
	it.FieldsJSON, err = FieldsToJSON(fields)
	if err != nil {
		t.Fatal(err)
	}
	att, err := mailItemSnapshotFromRow(it, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	return att
}

func assertBundleItemQuantity(t *testing.T, row *store.Item, quantity int64) {
	t.Helper()
	if row.Qty != quantity {
		t.Errorf("存储数量=%d，期望%d", row.Qty, quantity)
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	idx := itemQtyFieldIdx(row.WireClass)
	if idx < 0 || idx >= len(fields) {
		t.Fatalf("缺少权威数量槽: %s", row.WireClass)
	}
	if got := int64(u64Of(fields[idx].Num)); got != quantity {
		t.Errorf("权威数量=%d，期望%d", got, quantity)
	}
	if idx := qtyMirrorIdx(row.WireClass); idx >= 0 {
		if got := int64(u64Of(fields[idx].Num)); got != quantity {
			t.Errorf("UI镜像数量=%d，期望%d", got, quantity)
		}
	}
}

func assertBundleContents(t *testing.T, srv *Server, uid, quantity int64) {
	t.Helper()
	rows, err := srv.DB.ListItemsByContainer(context.Background(), uid, "backpack")
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, row := range rows {
		if row.ItemID == testBundleID {
			t.Error("组合包应展开为内含物")
		}
		if row.ItemID != testBundleContentsID {
			continue
		}
		total += row.Qty
		if row.WireClass != "aafdnnmleob" || row.TypeID != 128 {
			t.Errorf("包内物类型错误: %s/%d", row.WireClass, row.TypeID)
		}
		assertBundleItemQuantity(t, row, row.Qty)
	}
	if total != quantity {
		t.Errorf("包内总份数=%d，期望%d", total, quantity)
	}
}

func assertBundleMailUnclaimed(t *testing.T, db *store.DB, uid, id int64) {
	t.Helper()
	m, err := db.MailGet(context.Background(), uid, id)
	if err != nil || m.Read != 0 || m.ItemsJSON == "" {
		t.Fatalf("失败后邮件应保持未领取: %+v err=%v", m, err)
	}
}

func TestBundleMailClaimExpandsAndDoesNotDuplicate(t *testing.T) {
	t.Parallel()
	srv, db := newBundleTestServer(t)
	const uid = int64(4101)
	id, err := db.MailInsert(context.Background(), uid, 0, 0, "test", []store.MailItem{{ItemID: testBundleID, Qty: 2}})
	if err != nil {
		t.Fatal(err)
	}
	c := economyTestCtx(srv, uid, proto.Args524())
	if count, _ := srv.acceptMails(c, []int64{id, id}); count != 1 {
		t.Fatalf("领取物品条数=%d", count)
	}
	assertBundleContents(t, srv, uid, 20)
	if count, _ := srv.acceptMails(c, []int64{id}); count != 0 {
		t.Errorf("重复领取物品条数=%d", count)
	}
	assertBundleContents(t, srv, uid, 20)
}

func TestBundleMailClaimFailureRollsBack(t *testing.T) {
	t.Parallel()
	srv, db := newBundleTestServer(t)
	const uid = int64(4102)
	var ids []int64
	for _, items := range [][]store.MailItem{
		{{ItemID: "tool_shovel", Qty: 1}},
		{{ItemID: "tool_shovel", Qty: 1}, {ItemID: testBundleID, Qty: 1}},
	} {
		id, err := db.MailInsert(context.Background(), uid, 0, 0, "test", items)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if _, err := db.SQL.Exec(`CREATE TRIGGER reject_bundle_content BEFORE INSERT ON item
		WHEN NEW.item_id='food_egg' BEGIN SELECT RAISE(ABORT,'test bundle failure'); END`); err != nil {
		t.Fatal(err)
	}
	if count, _ := srv.acceptMails(economyTestCtx(srv, uid, proto.Args524()), ids); count != 0 {
		t.Errorf("失败领取不应提交: %d", count)
	}
	for _, id := range ids {
		assertBundleMailUnclaimed(t, db, uid, id)
	}
	rows, err := db.ListItemsByContainer(context.Background(), uid, "backpack")
	if err != nil || len(rows) != 0 {
		t.Fatalf("失败后残留物品: %+v err=%v", rows, err)
	}
}

func TestBundleMailNonStackableContentsAreSeparateItems(t *testing.T) {
	t.Parallel()
	srv, db := newBundleTestServer(t)
	seedEconomyItemDef(t, db, 990001, "test_tool_bundle", bundleCategory)
	if _, err := db.SQL.Exec(`UPDATE item_def SET attrs_json=? WHERE item_id='test_tool_bundle'`,
		`{"nbkhfhenbag":[{"cemjmlpafmc":"tool_shovel","ljnnpokapig":2}]}`); err != nil {
		t.Fatal(err)
	}
	const uid = int64(4103)
	id, err := db.MailInsert(context.Background(), uid, 0, 0, "test", []store.MailItem{{ItemID: "test_tool_bundle", Qty: 3}})
	if err != nil {
		t.Fatal(err)
	}
	if count, _ := srv.acceptMails(economyTestCtx(srv, uid, proto.Args524()), []int64{id}); count != 6 {
		t.Fatalf("领取物品条数=%d，期望6", count)
	}
	rows, err := db.ListItemsByContainer(context.Background(), uid, "backpack")
	if err != nil || len(rows) != 6 {
		t.Fatalf("非堆叠工具应发6个独立实例: count=%d err=%v", len(rows), err)
	}
	for _, row := range rows {
		if row.ItemID != "tool_shovel" || row.Qty != 1 || row.WireClass != "jceogpbfpjh" {
			t.Errorf("错误工具实例: %+v", row)
		}
	}
	items, bundle, err := srv.prepareStoreGoods(context.Background(), uid, "test_tool_bundle")
	if err != nil || !bundle || len(items) != 2 || items[0].GUID == items[1].GUID {
		t.Fatalf("商店应构造2个独立工具: count=%d bundle=%v err=%v", len(items), bundle, err)
	}
}

func TestBundleMailLegacySnapshotExpands(t *testing.T) {
	t.Parallel()
	srv, db := newBundleTestServer(t)
	const uid = int64(4110)
	att := legacyBundleMailSnapshot(t, srv, uid-1)
	raw, err := json.Marshal([]store.MailItem{att})
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.SQL.Exec(`INSERT INTO mail(uid,sender,kind,text,items_json,attachment_snapshot_version,created_ts,read)
		VALUES(?,?,1,'',?,?,0,0)`, uid, uid-1, string(raw), mailItemSnapshotVersion)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if count, _ := srv.acceptMails(economyTestCtx(srv, uid, proto.Args524()), []int64{id}); count != 1 {
		t.Fatalf("快照组合包领取物品条数=%d", count)
	}
	assertBundleContents(t, srv, uid, 20)
	if count, _ := srv.acceptMails(economyTestCtx(srv, uid, proto.Args524()), []int64{id}); count != 0 {
		t.Errorf("重复领取快照组合包: %d", count)
	}
	assertBundleContents(t, srv, uid, 20)
}

func TestBundleInvalidDefinitionsDoNotGrantItems(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, attrs, childAttrs string
		packs                   int64
	}{
		{"empty", `{}`, `{}`, 1},
		{"malformed", `{`, `{}`, 1},
		{"unknown_child", `{"nbkhfhenbag":[{"cemjmlpafmc":"no_such_item","ljnnpokapig":1}]}`, `{}`, 1},
		{"missing_child", `{"nbkhfhenbag":[{"ljnnpokapig":1}]}`, `{}`, 1},
		{"missing_count", `{"nbkhfhenbag":[{"cemjmlpafmc":"test_child"}]}`, `{}`, 1},
		{"zero_count", `{"nbkhfhenbag":[{"cemjmlpafmc":"test_child","ljnnpokapig":0}]}`, `{}`, 1},
		{"negative_count", `{"nbkhfhenbag":[{"cemjmlpafmc":"test_child","ljnnpokapig":-1}]}`, `{}`, 1},
		{"fractional_count", `{"nbkhfhenbag":[{"cemjmlpafmc":"test_child","ljnnpokapig":1.5}]}`, `{}`, 1},
		{"string_count", `{"nbkhfhenbag":[{"cemjmlpafmc":"test_child","ljnnpokapig":"2"}]}`, `{}`, 1},
		{"count_overflow", `{"nbkhfhenbag":[{"cemjmlpafmc":"test_child","ljnnpokapig":9223372036854775808}]}`, `{}`, 1},
		{"multiply_overflow", `{"nbkhfhenbag":[{"cemjmlpafmc":"test_child","ljnnpokapig":9223372036854775807}]}`, `{}`, 2},
		{"wire_overflow", `{"nbkhfhenbag":[{"cemjmlpafmc":"test_child","ljnnpokapig":4294967296}]}`, `{}`, 1},
		{"unit_overflow", `{"nbkhfhenbag":[{"cemjmlpafmc":"test_child","ljnnpokapig":2}]}`, `{"ljnnpokapig":2147483648}`, 1},
		{"total_overflow", `{"nbkhfhenbag":[{"cemjmlpafmc":"test_child","ljnnpokapig":4294967295},{"cemjmlpafmc":"test_child","ljnnpokapig":1}]}`, `{}`, 1},
		{"fractional_unit", `{"nbkhfhenbag":[{"cemjmlpafmc":"test_child","ljnnpokapig":1}]}`, `{"ljnnpokapig":1.5}`, 1},
		{"zero_unit", `{"nbkhfhenbag":[{"cemjmlpafmc":"test_child","ljnnpokapig":1}]}`, `{"ljnnpokapig":0}`, 1},
		{"negative_unit", `{"nbkhfhenbag":[{"cemjmlpafmc":"test_child","ljnnpokapig":1}]}`, `{"ljnnpokapig":-1}`, 1},
		{"self_cycle", `{"nbkhfhenbag":[{"cemjmlpafmc":"test_invalid_bundle","ljnnpokapig":1}]}`, `{}`, 1},
		{"nested", `{"nbkhfhenbag":[{"cemjmlpafmc":"AV_shamp1_pack_6_8","ljnnpokapig":1}]}`, `{}`, 1},
		{"too_many_instances", `{"nbkhfhenbag":[{"cemjmlpafmc":"tool_shovel","ljnnpokapig":10001}]}`, `{}`, 1},
		{"zero_packs", `{"nbkhfhenbag":[{"cemjmlpafmc":"test_child","ljnnpokapig":1}]}`, `{}`, 0},
		{"negative_packs", `{"nbkhfhenbag":[{"cemjmlpafmc":"test_child","ljnnpokapig":1}]}`, `{}`, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, db := newBundleTestServer(t)
			seedEconomyItemDef(t, db, 990002, "test_invalid_bundle", bundleCategory)
			seedEconomyItemDef(t, db, 990003, "test_child", 27)
			for itemID, attrs := range map[string]string{"test_invalid_bundle": tc.attrs, "test_child": tc.childAttrs} {
				if _, err := db.SQL.Exec(`UPDATE item_def SET attrs_json=? WHERE item_id=?`, attrs, itemID); err != nil {
					t.Fatal(err)
				}
			}
			const uid = int64(4111)
			id, err := db.MailInsert(context.Background(), uid, 0, 0, "test", []store.MailItem{{ItemID: "test_invalid_bundle", Qty: tc.packs}})
			if err != nil {
				t.Fatal(err)
			}
			if count, _ := srv.acceptMails(economyTestCtx(srv, uid, proto.Args524()), []int64{id}); count != 0 {
				t.Errorf("无效包发放了%d条物品", count)
			}
			assertBundleMailUnclaimed(t, db, uid, id)
			rows, err := db.ListItemsByContainer(context.Background(), uid, "backpack")
			if err != nil || len(rows) != 0 {
				t.Fatalf("无效包污染背包: count=%d err=%v", len(rows), err)
			}
		})
	}
}

func TestBundleMailRejectsPaidOrIncompleteSnapshot(t *testing.T) {
	t.Parallel()
	for _, incomplete := range []bool{false, true} {
		t.Run(strconv.FormatBool(incomplete), func(t *testing.T) {
			srv, db := newBundleTestServer(t)
			const uid = int64(4112)
			att := legacyBundleMailSnapshot(t, srv, uid-1)
			if incomplete {
				att.FieldsJSON = ""
			} else {
				att.BuyPrice = 123
			}
			raw, err := json.Marshal([]store.MailItem{att})
			if err != nil {
				t.Fatal(err)
			}
			res, err := db.SQL.Exec(`INSERT INTO mail(uid,sender,kind,text,items_json,attachment_snapshot_version,created_ts,read)
				VALUES(?,0,1,'',?,?,0,0)`, uid, string(raw), mailItemSnapshotVersion)
			if err != nil {
				t.Fatal(err)
			}
			id, err := res.LastInsertId()
			if err != nil {
				t.Fatal(err)
			}
			if count, _ := srv.acceptMails(economyTestCtx(srv, uid, proto.Args524()), []int64{id}); count != 0 {
				t.Fatalf("不可领取的快照发放了%d条物品", count)
			}
			assertBundleMailUnclaimed(t, db, uid, id)
			assertBundleContents(t, srv, uid, 0)
		})
	}
}

func TestBundleStorePurchaseAndFailureAreAtomic(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"success", "insert_failure", "invalid_definition"} {
		t.Run(name, func(t *testing.T) {
			srv, db := newBundleTestServer(t)
			const uid = int64(4113)
			if _, err := db.EnsureProfile(context.Background(), uid); err != nil {
				t.Fatal(err)
			}
			if _, err := db.SQL.Exec(`UPDATE profile SET silver=1000 WHERE uid=?`, uid); err != nil {
				t.Fatal(err)
			}
			if _, err := db.SQL.Exec(`INSERT INTO shop_item(store_id,item_id,silver,gold,stock) VALUES('bundle_store',?,100,0,10)`, testBundleID); err != nil {
				t.Fatal(err)
			}
			if name == "insert_failure" {
				if _, err := db.SQL.Exec(`CREATE TRIGGER reject_store_bundle BEFORE INSERT ON item
					WHEN NEW.item_id='food_egg' BEGIN SELECT RAISE(ABORT,'test store failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if name == "invalid_definition" {
				if _, err := db.SQL.Exec(`UPDATE item_def SET attrs_json='{}' WHERE item_id=?`, testBundleID); err != nil {
					t.Fatal(err)
				}
			}
			c := economyTestCtx(srv, uid, proto.Args524(proto.StrVal("bundle_store"), proto.StrVal(testBundleID), proto.U64(0)))
			c.ExpectResponse = true
			c.replyCapture = &ctxReplyCapture{}
			body, ok := hStoreBuy(false)(c)
			_, present, duplicates, failure := c.replyCapture.snapshot()
			fail := name != "success"
			if !ok || failure != fail || (fail && (!present || duplicates != 0)) || (!fail && len(body) == 0) {
				t.Fatalf("购买结果不符: fail=%v ok=%v body=%x present=%v duplicates=%d failure=%v", fail, ok, body, present, duplicates, failure)
			}
			var silver, txns int64
			if err := db.SQL.QueryRow(`SELECT silver FROM profile WHERE uid=?`, uid).Scan(&silver); err != nil {
				t.Fatal(err)
			}
			if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM txn WHERE uid=? AND kind='buy'`, uid).Scan(&txns); err != nil {
				t.Fatal(err)
			}
			wantQty, wantSilver, wantTxns := int64(10), int64(900), int64(1)
			if fail {
				wantQty, wantSilver, wantTxns = 0, 1000, 0
			}
			if silver != wantSilver || txns != wantTxns {
				t.Errorf("账实不符: silver=%d want=%d txns=%d want=%d", silver, wantSilver, txns, wantTxns)
			}
			assertBundleContents(t, srv, uid, wantQty)
		})
	}
}

func TestBundleMailOverflowPreservesExistingStack(t *testing.T) {
	t.Parallel()
	srv, db := newBundleTestServer(t)
	ctx := context.Background()
	const uid = int64(4114)
	row, err := srv.prepareNewItemWithCount(ctx, uid, testBundleContentsID, "backpack", math.MaxUint32, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertItem(ctx, row); err != nil {
		t.Fatal(err)
	}
	id, err := db.MailInsert(ctx, uid, 0, 0, "test", []store.MailItem{{ItemID: testBundleID, Qty: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if count, _ := srv.acceptMails(economyTestCtx(srv, uid, proto.Args524()), []int64{id}); count != 0 {
		t.Errorf("溢出领取不应提交: %d", count)
	}
	after, err := db.GetItemByGUID(ctx, row.GUID)
	if err != nil || *after != *row {
		t.Fatalf("溢出失败后原堆被改变: before=%+v after=%+v err=%v", row, after, err)
	}
	assertBundleMailUnclaimed(t, db, uid, id)
}

func TestBundleUnsupportedContentsPreserveMail(t *testing.T) {
	t.Parallel()
	srv, db := newBundleTestServer(t)
	seedEconomyItemDef(t, db, 990003, "test_unsupported_child", 999)
	if _, err := db.SQL.Exec(`UPDATE item_def SET attrs_json=? WHERE item_id=?`,
		`{"nbkhfhenbag":[{"cemjmlpafmc":"test_unsupported_child","ljnnpokapig":1}]}`, testBundleID); err != nil {
		t.Fatal(err)
	}
	const uid = int64(4115)
	id, err := db.MailInsert(context.Background(), uid, 0, 0, "test", []store.MailItem{{ItemID: testBundleID, Qty: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if count, _ := srv.acceptMails(economyTestCtx(srv, uid, proto.Args524()), []int64{id}); count != 0 {
		t.Errorf("不支持的子类别不能按兜底类型领取: %d", count)
	}
	assertBundleMailUnclaimed(t, db, uid, id)
	rows, err := db.ListItemsByContainer(context.Background(), uid, "backpack")
	if err != nil || len(rows) != 0 {
		t.Fatalf("不支持的子项不应入库: count=%d err=%v", len(rows), err)
	}
}

func TestBundleMetaQuantityAndMirrors(t *testing.T) {
	t.Parallel()
	for _, category := range []int64{19, 20, 45, 58} {
		t.Run(strconv.FormatInt(category, 10), func(t *testing.T) {
			srv, db := newBundleTestServer(t)
			seedEconomyItemDef(t, db, 990003, "test_meta_child", category)
			if _, err := db.SQL.Exec(`UPDATE item_def SET attrs_json='{"ljnnpokapig":5}' WHERE item_id='test_meta_child'`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.SQL.Exec(`UPDATE item_def SET attrs_json=? WHERE item_id=?`,
				`{"nbkhfhenbag":[{"cemjmlpafmc":"test_meta_child","ljnnpokapig":3}]}`, testBundleID); err != nil {
				t.Fatal(err)
			}
			const uid = int64(4116)
			items, bundle, err := srv.prepareStoreGoods(context.Background(), uid, testBundleID)
			if err != nil || !bundle || len(items) != 1 {
				t.Fatalf("商店构造失败: count=%d bundle=%v err=%v", len(items), bundle, err)
			}
			if items[0].WireClass != categoryWireClass[category].Class || items[0].TypeID != categoryWireClass[category].TypeID {
				t.Fatalf("子类别映射不符: %+v", items[0])
			}
			assertBundleItemQuantity(t, items[0], 15)
			original := economyTestItem(t, srv, uid, "test_meta_child", "backpack", 1)
			id, err := db.MailInsert(context.Background(), uid, 0, 0, "test", []store.MailItem{{ItemID: testBundleID, Qty: 2}})
			if err != nil {
				t.Fatal(err)
			}
			if count, granted := srv.acceptMails(economyTestCtx(srv, uid, proto.Args524()), []int64{id}); count != 1 || granted[0].GUID != original.GUID {
				t.Fatalf("邮件应合并到原堆: count=%d granted=%+v", count, granted)
			}
			row, err := db.GetItemByGUID(context.Background(), original.GUID)
			if err != nil {
				t.Fatal(err)
			}
			assertBundleItemQuantity(t, row, 35)
			tree, err := srv.ItemToTree(context.Background(), row)
			if err != nil || tree == nil {
				t.Fatalf("无法构造客户端物品树: %v", err)
			}
			for _, idx := range []int{itemQtyFieldIdx(row.WireClass), qtyMirrorIdx(row.WireClass)} {
				if got := u64Of(tree.Fields[idx].Num); got != 35 {
					t.Errorf("客户端数量槽%d=%d，期望35", idx, got)
				}
			}
			if _, err := db.SQL.Exec(`UPDATE item_def SET attrs_json=? WHERE item_id=?`,
				fmt.Sprintf(`{"nbkhfhenbag":[{"cemjmlpafmc":"test_meta_child","ljnnpokapig":%d}]}`, math.MaxUint32), testBundleID); err != nil {
				t.Fatal(err)
			}
			if _, _, err := srv.prepareStoreGoods(context.Background(), uid, testBundleID); err == nil {
				t.Error("权威数量槽溢出必须拒绝")
			}
		})
	}
}

// TestBundleAbsoluteQuantityHonorsFlag 条目 mlapkoadfnh=true 时数量为绝对个数，
// 不乘子物定义默认量；缺省条目维持乘法。SKU 用真实静态库：
// box_worm_asurv_100（worm_asurv def ljnnpokapig=30，flag=true → 应发 100 而非
// 3000，实机事故回归）、box_bloodworm_10（flag 缺省 → 30×10=300，价格自洽）。
// 官服语义见客户端 ahoacflhhno.ekfefhbgegl：flag 真直接返回条目数，否则乘 def 0x78。
func TestBundleAbsoluteQuantityHonorsFlag(t *testing.T) {
	srv, _ := newBundleTestServer(t)
	const uid = int64(4117)
	items, err := srv.prepareBundleItems(context.Background(), uid, "box_worm_asurv_100", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ItemID != "worm_asurv" {
		t.Fatalf("蚯蚓盒应展开为一行 worm_asurv: %+v", items)
	}
	assertBundleItemQuantity(t, items[0], 100)

	bw, err := srv.prepareBundleItems(context.Background(), uid, "box_bloodworm_10", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(bw) != 1 || bw[0].ItemID != "bloodworm" {
		t.Fatalf("血虫盒应展开为一行 bloodworm: %+v", bw)
	}
	assertBundleItemQuantity(t, bw[0], 300)
}

// TestBundleAbsoluteQuantityOverflowRejected 绝对个数条目超数量槽上限必须拒绝。
func TestBundleAbsoluteQuantityOverflowRejected(t *testing.T) {
	srv, db := newBundleTestServer(t)
	seedEconomyItemDef(t, db, 990004, "test_abs_bundle", bundleCategory)
	seedEconomyItemDef(t, db, 990005, "test_abs_child", 27)
	if _, err := db.SQL.Exec(`UPDATE item_def SET attrs_json=? WHERE item_id=?`,
		`{"nbkhfhenbag":[{"cemjmlpafmc":"test_abs_child","ljnnpokapig":4294967296,"mlapkoadfnh":true}]}`,
		"test_abs_bundle"); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.prepareBundleItems(context.Background(), int64(4118), "test_abs_bundle", "backpack", 1); err == nil {
		t.Error("绝对个数超过 U32 数量槽上限必须拒绝")
	}
}
