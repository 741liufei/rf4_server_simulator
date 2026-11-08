package gameserver

import (
	"bytes"
	"context"
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
			seedEconomyItemDef(t, db, 991002, "return_hook", 27)
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
	seedEconomyItemDef(t, db, 991003, "stack_hook", 27)
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
	s, db := newBundleTestServer(t)
	const uid = int64(4505)
	db.EnsureProfile(context.Background(), uid)
	db.SQL.Exec(`UPDATE profile SET silver=1000 WHERE uid=?`, uid)
	db.SQL.Exec(`INSERT INTO shop_item(store_id,item_id,silver,gold,stock) VALUES('shop',?,135,0,10)`, testBundleID)
	original := economyTestItem(t, s, uid, testBundleContentsID, "backpack", 3)
	c := economyTestCtx(s, uid, proto.Args524(proto.StrVal("shop"), proto.StrVal(testBundleID), proto.U64(0)))
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
