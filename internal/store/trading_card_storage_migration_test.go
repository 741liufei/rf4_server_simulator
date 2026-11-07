package store

import "testing"

func TestTradingCardStorageUpgradeAndRollback(t *testing.T) {
	db := openCatchHistoryDBAtVersion(t, 40)
	const payload = `[{"op":"VAL","v":{"fish_guid":"original","species":"perch","weight_g":2100}}]`
	for _, row := range []struct{ guid, class, container, parent string }{
		{"card", "dapkdcghfmg", "backpack", ""},
		{"deck", "ckfmndabaei", "backpack", ""},
		{"nested", "dapkdcghfmg", "card_deck", "deck"},
		{"bait", "kbnbcppjkaf", "backpack", ""},
	} {
		if _, err := db.SQL.Exec(`INSERT INTO item(guid,uid,wire_class,container,rig_guid,fields_json)
			VALUES(?,7,?,?,?,?)`, row.guid, row.class, row.container, row.parent, payload); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.SQL.Exec(`INSERT INTO container_slot(uid,container,slot,guid) VALUES(7,0,1,'card'),(7,0,2,'bait');
		CREATE TRIGGER fail_card_storage BEFORE INSERT ON schema_migrations
		WHEN NEW.name='migrations/00042_trading_card_storage.sql'
		BEGIN SELECT RAISE(ABORT,'test migration failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err == nil {
		t.Fatal("failed migration was reported as success")
	}
	assertCount(t, db, `SELECT COUNT(*) FROM item WHERE container='backpack'`, 3)
	assertCount(t, db, `SELECT COUNT(*) FROM container_slot WHERE guid='card'`, 1)
	if _, err := db.SQL.Exec(`DROP TRIGGER fail_card_storage`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.Migrate(); err != nil {
			t.Fatal(err)
		}
	}
	assertCount(t, db, `SELECT COUNT(*) FROM item WHERE container='native:18'`, 2)
	assertCount(t, db, `SELECT COUNT(*) FROM item WHERE guid='bait' AND container='backpack'`, 1)
	assertCount(t, db, `SELECT COUNT(*) FROM item WHERE guid='nested' AND container='card_deck' AND rig_guid='deck'`, 1)
	assertCount(t, db, `SELECT COUNT(*) FROM container_slot WHERE guid='card'`, 0)
	assertCount(t, db, `SELECT COUNT(*) FROM container_slot WHERE guid='bait'`, 1)
	rows, err := db.SQL.Query(`SELECT fields_json FROM item`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var got string
		if err := rows.Scan(&got); err != nil || got != payload {
			t.Fatalf("migration changed original card data: %q %v", got, err)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
