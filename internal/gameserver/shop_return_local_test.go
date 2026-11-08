package gameserver

import (
	"context"
	"encoding/json"
	"os"
	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/store"
	"testing"
)

// Explicit opt-in: this test only mutates a caller-provided disposable save copy.
func TestLocalStoreReturnSaveCopy(t *testing.T) {
	path := os.Getenv("RF4_RETURN_SAVE_COPY")
	if path == "" {
		t.Skip("disposable save copy not provided")
	}
	db, err := store.OpenWithStatic(path, os.Getenv("RF4_RETURN_STATIC"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewServer(config.Game{}, logging.Discard(), nil)
	s.SetDB(db)
	n, err := s.RecoverStorePurchases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("recovered=%d", n)
	rows, err := db.SQL.Query(`SELECT k,v FROM kv WHERE k LIKE 'store-purchase:%'`)
	if err != nil {
		t.Fatal(err)
	}
	var purchases []storePurchase
	for rows.Next() {
		var k, v string
		rows.Scan(&k, &v)
		var p storePurchase
		if err := json.Unmarshal([]byte(v), &p); err != nil {
			t.Fatal(err)
		}
		purchases = append(purchases, p)
	}
	rows.Close()
	if len(purchases) == 0 {
		t.Fatal("no legacy purchase recovered")
	}
	for i, p := range purchases {
		row := p.Items[0]
		var before int64
		if err := db.SQL.QueryRow(`SELECT silver FROM profile WHERE uid=?`, row.UID).Scan(&before); err != nil {
			t.Fatal(err)
		}
		if body, ok := hStoreReturn(storeReturnTestCtx(t, s, row.UID, p.StoreID, row.GUID, uint32(700+i))); !ok || len(body) == 0 {
			t.Fatal("save-copy return failed", p.ItemID)
		}
		var after int64
		db.SQL.QueryRow(`SELECT silver FROM profile WHERE uid=?`, row.UID).Scan(&after)
		if after-before != int64(p.Price) {
			t.Fatal("save-copy refund amount", p.ItemID, after-before, p.Price)
		}
		t.Logf("refunded %s in %s: %d", p.ItemID, p.StoreID, p.Price)
	}
}
