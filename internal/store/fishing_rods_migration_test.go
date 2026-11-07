package store

import "testing"

func TestFishingRodsUpgradeAndRollback(t *testing.T) {
	db := openCatchHistoryDBAtVersion(t, 40)
	if _, err := db.SQL.Exec(`INSERT INTO fishing_state(uid,gear_guid,pending_json,cast_active)
		VALUES(1000001,'rod-a','{"fish_guid":"fish-a","weight_g":321}',1);
		CREATE TRIGGER fail_rods_upgrade BEFORE INSERT ON schema_migrations
		WHEN NEW.name='migrations/00041_fishing_rods.sql'
		BEGIN SELECT RAISE(ABORT,'test migration failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err == nil {
		t.Fatal("failed migration was reported as success")
	}
	assertCount(t, db, `SELECT COUNT(*) FROM pragma_table_info('fishing_state') WHERE name='rod_guid'`, 0)
	assertCount(t, db, `SELECT COUNT(*) FROM fishing_state WHERE uid=1000001 AND json_extract(pending_json,'$.weight_g')=321`, 1)
	if _, err := db.SQL.Exec(`DROP TRIGGER fail_rods_upgrade`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.Migrate(); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := db.LoadFishingState(t.Context(), 1000001)
	if err != nil || saved == nil || saved.RodGUID != "rod-a" || saved.Pending == nil || saved.Pending.WeightG != 321 {
		t.Fatalf("old fish not preserved: %+v %v", saved, err)
	}
	if err := db.SaveFishingState(t.Context(), &FishingState{UID: 1000001, GearGUID: "rod-b", Pending: &FishingFish{FishGUID: "fish-b"}}); err != nil {
		t.Fatal(err)
	}
	assertCount(t, db, `SELECT COUNT(*) FROM fishing_state WHERE uid=1000001`, 2)
}
