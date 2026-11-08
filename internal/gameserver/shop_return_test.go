package gameserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

func storeReturnTestCtx(t *testing.T, s *Server, uid int64, shop, guid string, call uint32) *Ctx {
	t.Helper()
	c := economyTestCtx(s, uid, proto.Args524(proto.StrVal(shop), proto.Guid(guid)))
	c.Main, c.Sub, c.CallID, c.ExpectResponse = 22, 4, call, true
	c.Body, _ = proto.EncodeValue(c.Tree, s.Table())
	c.replyCapture = &ctxReplyCapture{}
	return c
}

func buyReturnTestItem(t *testing.T, s *Server, uid int64, shop, item string, gold bool) *store.Item {
	t.Helper()
	c := economyTestCtx(s, uid, proto.Args524(proto.StrVal(shop), proto.StrVal(item), proto.U64(999999)))
	c.ExpectResponse = true
	c.replyCapture = &ctxReplyCapture{}
	if b, ok := hStoreBuy(gold)(c); !ok || len(b) == 0 {
		t.Fatal("purchase failed")
	}
	var guid string
	if err := s.DB.SQL.QueryRow(`SELECT guid FROM item WHERE uid=? AND item_id=? ORDER BY rowid DESC LIMIT 1`, uid, item).Scan(&guid); err != nil {
		t.Fatal(err)
	}
	row, err := s.DB.GetItemByGUID(context.Background(), guid)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func buyReturnTestRod(t *testing.T, s *Server, db *store.DB, uid int64) *store.Item {
	t.Helper()
	const rodID = "boatr_10213_012"
	if _, err := db.EnsureProfile(context.Background(), uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE profile SET silver=200000 WHERE uid=?`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO shop_item(store_id,item_id,silver,gold,stock) VALUES('shop',?,100000,0,10)`, rodID); err != nil {
		t.Fatal(err)
	}
	return buyReturnTestItem(t, s, uid, "shop", rodID, false)
}

func TestStoreReturnFourShopsBothCurrenciesAndReplay(t *testing.T) {
	for _, shop := range []string{"fishing_store_01", "tool_store_01", "beluga_store_01", "zeiman_store_01"} {
		for _, gold := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/gold=%t", shop, gold), func(t *testing.T) {
				s, db := newEconomyTxTestServer(t)
				const uid = int64(4501)
				seedEconomyItemDef(t, db, 991001, "return_float", 28)
				if _, err := db.SQL.Exec(`INSERT INTO shop_item(store_id,item_id,silver,gold,stock) VALUES(?,'return_float',135,7,10)`, shop); err != nil {
					t.Fatal(err)
				}
				db.EnsureProfile(context.Background(), uid)
				db.SQL.Exec(`UPDATE profile SET silver=1000,gold=100 WHERE uid=?`, uid)
				row := buyReturnTestItem(t, s, uid, shop, "return_float", gold)
				fields, _ := FieldsFromJSON(row.FieldsJSON)
				i := fieldIdxByCS(row.WireClass, csOrigin)
				if i < 0 || fields[i].Val == nil || fields[i].Val.TypeID != 166 {
					t.Fatal("purchase receipt not persisted")
				}
				receipt := fields[i].Val
				minute := uint32(worldTotalGameMin(time.Unix(int64(row.CreatedTS), 0)))
				if receipt.Fields[0].Str == nil || *receipt.Fields[0].Str != shop || receipt.Fields[2].U32() != minute || minute == 0 {
					t.Fatal("receipt would be excluded from client return list")
				}
				// A fresh server reads the same persisted purchase after reconnect.
				s2 := NewServer(s.cfg, s.log, nil)
				s2.SetDB(db)
				c := storeReturnTestCtx(t, s2, uid, shop, row.GUID, 20)
				body, ok := hStoreReturn(c)
				if !ok || len(body) == 0 {
					t.Fatal("return failed")
				}
				v, _, err := proto.DecodeValue(body, 0, s.Table())
				if err != nil || v.TypeID != 800 || v.Fields[0].Val.ArgCodeStr() != "61" || v.Fields[1].Val.ArgCodeStr() != "61" || v.Fields[2].Val.Items[0].Hex != row.GUID {
					t.Fatalf("bad removal response: %v %v", v, err)
				}
				var silver, g int64
				db.SQL.QueryRow(`SELECT silver,gold FROM profile WHERE uid=?`, uid).Scan(&silver, &g)
				if silver != 1000 || g != 100 {
					t.Fatalf("wrong refund: silver=%d gold=%d", silver, g)
				}
				replay, ok := hStoreReturn(c)
				if !ok || !bytes.Equal(body, replay) {
					t.Fatal("business receipt did not replay")
				}
				c2 := storeReturnTestCtx(t, s2, uid, shop, row.GUID, 21)
				hStoreReturn(c2)
				if _, _, _, failure := c2.replyCapture.snapshot(); !failure {
					t.Fatal("returned item accepted twice")
				}
				var count int
				db.SQL.QueryRow(`SELECT COUNT(*) FROM txn WHERE uid=? AND kind='return'`, uid).Scan(&count)
				if count != 1 {
					t.Fatal("duplicate refund")
				}
			})
		}
	}
}

func TestStoreReturnRejectsModifiedForeignAndWrongShop(t *testing.T) {
	for _, mode := range []string{"quantity", "durability", "workmanship", "mounted", "storage", "wrong_store", "foreign", "receipt_failure", "wallet_failure", "delete_failure"} {
		t.Run(mode, func(t *testing.T) {
			s, db := newEconomyTxTestServer(t)
			const uid = int64(4502)
			seedEconomyItemDef(t, db, 991002, "return_hook", 12)
			db.SQL.Exec(`INSERT INTO shop_item(store_id,item_id,silver,gold,stock) VALUES('shop','return_hook',135,7,10)`)
			db.EnsureProfile(context.Background(), uid)
			db.SQL.Exec(`UPDATE profile SET silver=1000,gold=100 WHERE uid=?`, uid)
			row := buyReturnTestItem(t, s, uid, "shop", "return_hook", false)
			shop, who := "shop", uid
			wantCode := 196
			switch mode {
			case "quantity":
				wantCode = 150
				db.SQL.Exec(`UPDATE item SET qty=qty+1 WHERE guid=?`, row.GUID)
			case "durability":
				wantCode = 150
				db.SQL.Exec(`UPDATE item SET durability=0.9 WHERE guid=?`, row.GUID)
			case "workmanship":
				wantCode = 150
				db.SQL.Exec(`UPDATE item SET workmanship=0.9 WHERE guid=?`, row.GUID)
			case "mounted":
				wantCode = 151
				db.SQL.Exec(`UPDATE item SET rig_guid='parent',container='rig' WHERE guid=?`, row.GUID)
			case "storage":
				wantCode = 111
				db.SQL.Exec(`UPDATE item SET container='home' WHERE guid=?`, row.GUID)
			case "wrong_store":
				shop = "other_shop"
			case "foreign":
				who++
			case "receipt_failure":
				db.SQL.Exec(`CREATE TRIGGER fail_return BEFORE INSERT ON kv WHEN NEW.k LIKE 'store-return:%' BEGIN SELECT RAISE(ABORT,'test'); END`)
			case "wallet_failure":
				db.SQL.Exec(`CREATE TRIGGER fail_return BEFORE INSERT ON txn WHEN NEW.kind='return' BEGIN SELECT RAISE(ABORT,'test'); END`)
			case "delete_failure":
				db.SQL.Exec(`CREATE TRIGGER fail_return BEFORE DELETE ON item BEGIN SELECT RAISE(ABORT,'test'); END`)
			}
			c := storeReturnTestCtx(t, s, who, shop, row.GUID, 30)
			hStoreReturn(c)
			failureBody, _, _, failure := c.replyCapture.snapshot()
			if !failure {
				t.Fatal("invalid return accepted")
			}
			if want := proto.BuildFailureEnvelope(c.CallID, "RF4.API.APIError", strconv.Itoa(wantCode)); !bytes.Equal(failureBody, want) {
				t.Fatalf("return must display native reason %d, got %x", wantCode, failureBody)
			}
			if _, err := db.GetItemByGUID(context.Background(), row.GUID); err != nil {
				t.Fatal("failed return lost item")
			}
			var silver, gold int64
			db.SQL.QueryRow(`SELECT silver,gold FROM profile WHERE uid=?`, uid).Scan(&silver, &gold)
			if silver != 865 || gold != 100 {
				t.Fatal("failed return changed wallet")
			}
		})
	}
}

func TestStoreReturnKeepsExistingStackAndRefundsEachPurchase(t *testing.T) {
	s, db := newEconomyTxTestServer(t)
	const uid = int64(4503)
	seedEconomyItemDef(t, db, 991003, "stack_hook", 12)
	db.SQL.Exec(`INSERT INTO shop_item(store_id,item_id,silver,gold,stock) VALUES('shop','stack_hook',135,7,10)`)
	db.EnsureProfile(context.Background(), uid)
	db.SQL.Exec(`UPDATE profile SET silver=1000 WHERE uid=?`, uid)
	original := economyTestItem(t, s, uid, "stack_hook", "backpack", 3)
	a := buyReturnTestItem(t, s, uid, "shop", "stack_hook", false)
	b := buyReturnTestItem(t, s, uid, "shop", "stack_hook", false)
	if a.GUID == b.GUID || a.GUID == original.GUID {
		t.Fatal("purchase merged into unrelated stack")
	}
	for i, row := range []*store.Item{a, b} {
		if body, ok := hStoreReturn(storeReturnTestCtx(t, s, uid, "shop", row.GUID, uint32(40+i))); !ok || len(body) == 0 {
			t.Fatal("stack return failed")
		}
	}
	after, err := db.GetItemByGUID(context.Background(), original.GUID)
	if err != nil || after.Qty != original.Qty {
		t.Fatal("return touched original stack")
	}
	var silver int64
	db.SQL.QueryRow(`SELECT silver FROM profile WHERE uid=?`, uid).Scan(&silver)
	if silver != 1000 {
		t.Fatal("two purchases did not refund exactly 270")
	}
}

func TestRecoverStorePurchasesOnlyUntouchedUniqueItems(t *testing.T) {
	for _, mode := range []string{"untouched", "used", "ambiguous"} {
		t.Run(mode, func(t *testing.T) {
			s, db := newEconomyTxTestServer(t)
			const uid = int64(4504)
			seedEconomyItemDef(t, db, 991004, "legacy_float", 28)
			row := economyTestItem(t, s, uid, "legacy_float", "backpack", 1)
			_, err := db.SQL.Exec(`INSERT INTO txn(uid,ts,kind,item_id,silver_delta,gold_delta,note) VALUES(?,?,'buy',?, -135,0,'store=shop currency=silver')`, uid, row.CreatedTS, row.ItemID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "used" {
				db.SQL.Exec(`UPDATE item SET durability=0.9 WHERE guid=?`, row.GUID)
			}
			if mode == "ambiguous" {
				economyTestItem(t, s, uid, "legacy_float", "backpack", 1)
			}
			n, err := s.RecoverStorePurchases(context.Background())
			want := 0
			if mode == "untouched" {
				want = 1
			}
			if err != nil || n != want {
				t.Fatalf("recovered=%d want=%d err=%v", n, want, err)
			}
			if n, err := s.RecoverStorePurchases(context.Background()); err != nil || n != 0 {
				t.Fatalf("recovery not idempotent: %d %v", n, err)
			}
			if mode == "untouched" {
				if b, ok := hStoreReturn(storeReturnTestCtx(t, s, uid, "shop", row.GUID, 50)); !ok || len(b) == 0 {
					t.Fatal("recovered purchase not refundable")
				}
			}
		})
	}
}

func TestStoreReturnBundleRefundsOnceAndPreservesUnrelatedStack(t *testing.T) {
	s, db := newEconomyTxTestServer(t)
	const uid = int64(4505)
	const bundleID = "return_bundle"
	seedEconomyItemDef(t, db, 991005, "return_hook", 12)
	if _, err := db.SQL.Exec(`INSERT OR REPLACE INTO item_def(numeric_id,item_id,category,kind,brand,attrs_json)
		VALUES(?,?,?, '','',?)`, 991006, bundleID, bundleCategory,
		`{"nbkhfhenbag":[{"cemjmlpafmc":"return_hook","ljnnpokapig":1}]}`); err != nil {
		t.Fatal(err)
	}
	db.EnsureProfile(context.Background(), uid)
	db.SQL.Exec(`UPDATE profile SET silver=1000 WHERE uid=?`, uid)
	db.SQL.Exec(`INSERT INTO shop_item(store_id,item_id,silver,gold,stock) VALUES('shop',?,135,0,10)`, bundleID)
	original := economyTestItem(t, s, uid, "return_hook", "backpack", 3)
	c := economyTestCtx(s, uid, proto.Args524(proto.StrVal("shop"), proto.StrVal(bundleID), proto.U64(0)))
	if b, ok := hStoreBuy(false)(c); !ok || len(b) == 0 {
		t.Fatal("bundle purchase failed")
	}
	var guid string
	if err := db.SQL.QueryRow(`SELECT guid FROM item WHERE uid=? AND guid<>? ORDER BY rowid LIMIT 1`, uid, original.GUID).Scan(&guid); err != nil {
		t.Fatal(err)
	}
	if b, ok := hStoreReturn(storeReturnTestCtx(t, s, uid, "shop", guid, 51)); !ok || len(b) == 0 {
		t.Fatal("bundle refund failed")
	}
	var count int
	db.SQL.QueryRow(`SELECT COUNT(*) FROM item WHERE uid=?`, uid).Scan(&count)
	if count != 1 {
		t.Fatal("bundle return left child items")
	}
	if row, err := db.GetItemByGUID(context.Background(), original.GUID); err != nil || row.Qty != original.Qty {
		t.Fatal("bundle return changed preexisting stack")
	}
	var silver int64
	db.SQL.QueryRow(`SELECT silver FROM profile WHERE uid=?`, uid).Scan(&silver)
	if silver != 1000 {
		t.Fatalf("bundle refund=%d", silver)
	}
}

func TestStoreReturnEnforces48HourWindow(t *testing.T) {
	s, db := newEconomyTxTestServer(t)
	const uid = int64(4506)
	seedEconomyItemDef(t, db, 991006, "window_float", 28)
	db.SQL.Exec(`INSERT INTO shop_item(store_id,item_id,silver,gold,stock) VALUES('shop','window_float',135,7,10)`)
	db.EnsureProfile(context.Background(), uid)
	db.SQL.Exec(`UPDATE profile SET silver=1000 WHERE uid=?`, uid)
	row := buyReturnTestItem(t, s, uid, "shop", "window_float", false)

	purchaseKey := storePurchaseKey(uid, row.GUID)
	var raw string
	if err := db.SQL.QueryRow(`SELECT v FROM kv WHERE k=?`, purchaseKey).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var purchase storePurchase
	if err := json.Unmarshal([]byte(raw), &purchase); err != nil {
		t.Fatal(err)
	}
	now := worldTotalGameMin(time.Now())
	purchase.BoughtMinute = now - storeReturnWindowGameMin
	data, err := json.Marshal(purchase)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE kv SET v=? WHERE k=?`, string(data), purchaseKey); err != nil {
		t.Fatal(err)
	}

	c := storeReturnTestCtx(t, s, uid, "shop", row.GUID, 60)
	hStoreReturn(c)
	failureBody, _, _, failure := c.replyCapture.snapshot()
	if !failure {
		t.Fatal("expired return accepted")
	}
	if want := proto.BuildFailureEnvelope(c.CallID, "RF4.API.APIError", strconv.Itoa(149)); !bytes.Equal(failureBody, want) {
		t.Fatalf("expired return must report 149, got %x", failureBody)
	}
	if _, err := db.GetItemByGUID(context.Background(), row.GUID); err != nil {
		t.Fatal("expired return lost item")
	}

	purchase.BoughtMinute = now - storeReturnWindowGameMin + 100
	data, err = json.Marshal(purchase)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE kv SET v=? WHERE k=?`, string(data), purchaseKey); err != nil {
		t.Fatal(err)
	}
	if body, ok := hStoreReturn(storeReturnTestCtx(t, s, uid, "shop", row.GUID, 61)); !ok || len(body) == 0 {
		t.Fatal("return inside 48h window failed")
	}
	var silver int64
	db.SQL.QueryRow(`SELECT silver FROM profile WHERE uid=?`, uid).Scan(&silver)
	if silver != 1000 {
		t.Fatalf("window refund=%d", silver)
	}
}

func TestStoreReturnRejectsNonReturnableCategories(t *testing.T) {
	for _, category := range []int64{18, 19, 27} {
		t.Run(fmt.Sprintf("category_%d", category), func(t *testing.T) {
			s, db := newEconomyTxTestServer(t)
			const uid = int64(4507)
			itemID := fmt.Sprintf("bad_category_%d", category)
			seedEconomyItemDef(t, db, 992000+category, itemID, category)
			db.SQL.Exec(`INSERT INTO shop_item(store_id,item_id,silver,gold,stock) VALUES('shop',?,135,7,10)`, itemID)
			db.EnsureProfile(context.Background(), uid)
			db.SQL.Exec(`UPDATE profile SET silver=1000,gold=100 WHERE uid=?`, uid)
			row := buyReturnTestItem(t, s, uid, "shop", itemID, false)

			c := storeReturnTestCtx(t, s, uid, "shop", row.GUID, 62)
			hStoreReturn(c)
			failureBody, _, _, failure := c.replyCapture.snapshot()
			if !failure {
				t.Fatal("non-returnable category accepted")
			}
			if want := proto.BuildFailureEnvelope(c.CallID, "RF4.API.APIError", strconv.Itoa(196)); !bytes.Equal(failureBody, want) {
				t.Fatalf("non-returnable category must report 196, got %x", failureBody)
			}
			if _, err := db.GetItemByGUID(context.Background(), row.GUID); err != nil {
				t.Fatal("rejected category lost item")
			}
			var silver int64
			db.SQL.QueryRow(`SELECT silver FROM profile WHERE uid=?`, uid).Scan(&silver)
			if silver != 865 {
				t.Fatal("rejected category changed wallet")
			}
		})
	}
}

func TestStoreReturnRodAllowsFactoryProjectionAndCleansRig(t *testing.T) {
	s, db := newEconomyTxTestServer(t)
	const uid = int64(4508)
	row := buyReturnTestRod(t, s, db, uid)
	if n, err := s.BackfillRigProjections(context.Background()); err != nil || n == 0 {
		t.Fatalf("backfill rods=%d err=%v", n, err)
	}
	comps, err := db.ListRigComponents(context.Background(), row.GUID)
	if err != nil || len(comps) == 0 {
		t.Fatalf("factory rig projection missing: %d %v", len(comps), err)
	}
	if _, err := db.SQL.Exec(`INSERT OR REPLACE INTO rig(guid,uid,rig_type,name,fields_json) VALUES(?,?,?,?,?)`, row.GUID, uid, 0, "rod", "{}"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT OR REPLACE INTO rod_setting(guid,key,value,updated_ts) VALUES(?,?,?,?)`, row.GUID, "test", "1", 1.0); err != nil {
		t.Fatal(err)
	}
	if body, ok := hStoreReturn(storeReturnTestCtx(t, s, uid, "shop", row.GUID, 63)); !ok || len(body) == 0 {
		t.Fatal("factory rod return failed")
	}
	if current, err := db.GetItemByGUID(context.Background(), row.GUID); err == nil && current != nil {
		t.Fatal("returned rod still exists")
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM rig_component WHERE rig_guid=?`,
		`SELECT COUNT(*) FROM rig WHERE guid=? AND uid=?`,
		`SELECT COUNT(*) FROM rod_setting WHERE guid=?`,
	} {
		var n int
		args := []any{row.GUID}
		if q == `SELECT COUNT(*) FROM rig WHERE guid=? AND uid=?` {
			args = append(args, uid)
		}
		if err := db.SQL.QueryRow(q, args...).Scan(&n); err != nil || n != 0 {
			t.Fatalf("return left rig residue: query=%s n=%d err=%v", q, n, err)
		}
	}
	var silver int64
	db.SQL.QueryRow(`SELECT silver FROM profile WHERE uid=?`, uid).Scan(&silver)
	if silver != 200000 {
		t.Fatalf("rod refund=%d", silver)
	}
}

func TestStoreReturnRodRejectsPlayerAddedProjection(t *testing.T) {
	s, db := newEconomyTxTestServer(t)
	const uid = int64(4509)
	row := buyReturnTestRod(t, s, db, uid)
	if _, err := s.BackfillRigProjections(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO rig_component(rig_guid,slot,ordinal,guid,extra) VALUES(?,?,?,?,?)`,
		row.GUID, 99, 0, "aa000000000000000000000000000099", 0); err != nil {
		t.Fatal(err)
	}
	c := storeReturnTestCtx(t, s, uid, "shop", row.GUID, 64)
	hStoreReturn(c)
	failureBody, _, _, failure := c.replyCapture.snapshot()
	if !failure {
		t.Fatal("rod with added projection accepted")
	}
	if want := proto.BuildFailureEnvelope(c.CallID, "RF4.API.APIError", strconv.Itoa(151)); !bytes.Equal(failureBody, want) {
		t.Fatalf("added projection must report 151, got %x", failureBody)
	}
	if current, err := db.GetItemByGUID(context.Background(), row.GUID); err != nil || current == nil {
		t.Fatal("rejected rod return lost item")
	}
}
