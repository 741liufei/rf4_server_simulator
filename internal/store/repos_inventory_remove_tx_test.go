package store

import (
	"context"
	"reflect"
	"testing"
)

func TestRemoveOwnedItemsTxLeavesCommitAndRollbackToCaller(t *testing.T) {
	db := openIntegrityTestDB(t)
	ctx := context.Background()
	const uid int64 = 7357
	fish := seedRemoveTestFish(t, db, uid, 7301, true)
	foreign := seedRemoveTestFish(t, db, uid+1, 7302, true)
	const ordinary = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := db.InsertItem(ctx, &Item{GUID: ordinary, UID: uid, Container: "backpack", Qty: 1, FieldsJSON: "[]"}); err != nil {
		t.Fatal(err)
	}
	for _, commit := range []bool{false, true} {
		tx, err := db.SQL.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		removed, released, err := db.RemoveOwnedItemsTx(ctx, tx, uid, []string{ordinary, fish, ordinary, fish, foreign})
		if err != nil || !reflect.DeepEqual(removed, []string{ordinary, fish}) || !reflect.DeepEqual(released, []string{fish}) {
			t.Fatalf("removed=%v fish=%v err=%v", removed, released, err)
		}
		var items int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM item WHERE uid=?`, uid).Scan(&items); err != nil || items != 0 {
			t.Fatalf("caller transaction did not see removals: items=%d err=%v", items, err)
		}
		if commit {
			err = tx.Commit()
		} else {
			err = tx.Rollback()
		}
		if err != nil {
			t.Fatal(err)
		}
		wantItems, wantFish := 2, 1
		if commit {
			wantItems, wantFish = 0, 0
		}
		assertCount(t, db, `SELECT COUNT(*) FROM item WHERE uid=7357`, wantItems)
		assertCount(t, db, `SELECT COUNT(*) FROM keepnet_items WHERE uid=7357`, wantFish)
		assertCount(t, db, `SELECT COUNT(*) FROM item WHERE uid=7358`, 1)
		assertCount(t, db, `SELECT COUNT(*) FROM keepnet_items WHERE uid=7358`, 1)
	}
	removed, fishRemoved, err := db.RemoveOwnedItems(ctx, uid, []string{ordinary, fish, foreign})
	if err != nil || len(removed) != 0 || len(fishRemoved) != 0 {
		t.Fatalf("wrapper repeat was not idempotent: removed=%v fish=%v err=%v", removed, fishRemoved, err)
	}
}
