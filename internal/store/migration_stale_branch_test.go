package store

import (
	"testing"
)

// TestMigrateRetiresStaleBranchRowBlockingTripState 复刻分支库升级主线库的真实状态：
// 渔轮部件磨损分支曾以 00035 登记自己的迁移，而主线 00035 现在是 trip_state。
// 旧登记行不让位的话，主线 00035 插登记会撞 schema_migrations.version 的 UNIQUE 约束，
// gameserver/gateway 直接起不来（2026-11-06 实机遇到）。
func TestMigrateRetiresStaleBranchRowBlockingTripState(t *testing.T) {
	db := openCatchHistoryDBAtVersion(t, 34)
	if _, err := db.SQL.Exec(`INSERT INTO schema_migrations VALUES(35,'migrations/00035_reel_component_wear.sql',0)`); err != nil {
		t.Fatal(err)
	}

	if err := db.Migrate(); err != nil {
		t.Fatalf("旧分支登记行挡住了主线迁移: %v", err)
	}

	assertCount(t, db,
		`SELECT COUNT(*) FROM schema_migrations WHERE version=35 AND name='migrations/00035_trip_state.sql'`, 1)
	assertCount(t, db,
		`SELECT COUNT(*) FROM schema_migrations WHERE version=39 AND name='migrations/00039_trip_grant.sql'`, 1)
	assertCount(t, db,
		`SELECT COUNT(*) FROM schema_migrations WHERE name='migrations/00035_reel_component_wear.sql'`, 0)
	assertCount(t, db,
		`SELECT COUNT(*) FROM schema_migrations WHERE version=9000035 AND name='retired/migrations/00035_reel_component_wear.sql'`, 1)
	assertCount(t, db, `SELECT COUNT(*) FROM pragma_table_info('trip_state') WHERE name='remaining_sec'`, 1)

	// 再跑一次必须幂等：退役行不能被反复挪动编号。
	if err := db.Migrate(); err != nil {
		t.Fatalf("二次迁移应幂等: %v", err)
	}
	assertCount(t, db,
		`SELECT COUNT(*) FROM schema_migrations WHERE version=9000035 AND name='retired/migrations/00035_reel_component_wear.sql'`, 1)
}

// TestMigrateKeepsUnrelatedBranchRowInPlace 验证只有真的挡住待应用编号的旧登记行才退役：
// 没有冲突的编号必须原地保留，不然等于篡改别人的迁移历史。
func TestMigrateKeepsUnrelatedBranchRowInPlace(t *testing.T) {
	db := openCatchHistoryDBAtVersion(t, 33)
	if _, err := db.SQL.Exec(`INSERT INTO schema_migrations VALUES(120,'migrations/00120_some_branch_feature.sql',0)`); err != nil {
		t.Fatal(err)
	}

	if err := db.Migrate(); err != nil {
		t.Fatalf("无冲突的旧分支登记不该影响迁移: %v", err)
	}
	assertCount(t, db,
		`SELECT COUNT(*) FROM schema_migrations WHERE version=120 AND name='migrations/00120_some_branch_feature.sql'`, 1)
	assertCount(t, db,
		`SELECT COUNT(*) FROM schema_migrations WHERE name LIKE 'retired/%'`, 0)
}

func TestMigrateRenumbersFishHuntBranchMigration(t *testing.T) {
	db := openCatchHistoryDBAtVersion(t, 45)
	createLegacyFishHuntState(t, db)
	if _, err := db.SQL.Exec(`INSERT INTO schema_migrations VALUES(46,'migrations/00046_fish_hunt.sql',0)`); err != nil {
		t.Fatal(err)
	}

	if err := db.Migrate(); err != nil {
		t.Fatalf("旧鱼猎迁移号挡住了主线 00046: %v", err)
	}

	assertCount(t, db,
		`SELECT COUNT(*) FROM schema_migrations WHERE version=46 AND name='migrations/00046_player_condition_comfort_luck.sql'`, 1)
	assertCount(t, db,
		`SELECT COUNT(*) FROM schema_migrations WHERE version=49 AND name='migrations/00049_fish_hunt.sql'`, 1)
	assertCount(t, db,
		`SELECT COUNT(*) FROM schema_migrations WHERE name='migrations/00046_fish_hunt.sql'`, 0)
}

func TestMigrateRenumbersMarineHuntBranchMigration(t *testing.T) {
	db := openCatchHistoryDBAtVersion(t, 45)
	createLegacyFishHuntState(t, db)
	if _, err := db.SQL.Exec(`
		ALTER TABLE fish_hunt_state ADD COLUMN hunt_mode INTEGER NOT NULL DEFAULT 0;
		ALTER TABLE fish_hunt_state ADD COLUMN sea_zone INTEGER NOT NULL DEFAULT 0;
		ALTER TABLE fish_hunt_state ADD COLUMN sea_zone_known INTEGER NOT NULL DEFAULT 0;
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`
		INSERT INTO schema_migrations VALUES(46,'migrations/00046_fish_hunt.sql',0);
		INSERT INTO schema_migrations VALUES(47,'migrations/00047_marine_hunt.sql',0);
	`); err != nil {
		t.Fatal(err)
	}

	if err := db.Migrate(); err != nil {
		t.Fatalf("旧海图狩猎迁移号挡住了主线 00047: %v", err)
	}

	assertCount(t, db,
		`SELECT COUNT(*) FROM schema_migrations WHERE version=49 AND name='migrations/00049_fish_hunt.sql'`, 1)
	assertCount(t, db,
		`SELECT COUNT(*) FROM schema_migrations WHERE version=50 AND name='migrations/00050_marine_hunt.sql'`, 1)
	assertCount(t, db,
		`SELECT COUNT(*) FROM schema_migrations WHERE name IN ('migrations/00046_fish_hunt.sql','migrations/00047_marine_hunt.sql')`, 0)
}

func createLegacyFishHuntState(t *testing.T, db *DB) {
	t.Helper()
	if _, err := db.SQL.Exec(`
		CREATE TABLE fish_hunt_state(
			uid             INTEGER NOT NULL,
			loc             TEXT    NOT NULL,
			spot            INTEGER NOT NULL,
			anchor_x        REAL    NOT NULL DEFAULT 0,
			anchor_z        REAL    NOT NULL DEFAULT 0,
			anchor_known    INTEGER NOT NULL DEFAULT 0,
			active_seconds  REAL    NOT NULL DEFAULT 0,
			valid_bites     INTEGER NOT NULL DEFAULT 0,
			pity            INTEGER NOT NULL DEFAULT 0,
			big_miss        INTEGER NOT NULL DEFAULT 0,
			comp_fish       INTEGER NOT NULL DEFAULT 0,
			comp_grade      INTEGER NOT NULL DEFAULT 0,
			comp_until      REAL    NOT NULL DEFAULT 0,
			last_catch_guid TEXT    NOT NULL DEFAULT '',
			last_active_ts  REAL    NOT NULL DEFAULT 0,
			updated_ts      REAL    NOT NULL DEFAULT 0,
			PRIMARY KEY(uid, loc, spot)
		)
	`); err != nil {
		t.Fatal(err)
	}
}
