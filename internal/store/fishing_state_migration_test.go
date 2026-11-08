package store

import (
	"testing"
)

// 00048 必须可从 47 版旧库升级、失败不留半列、重复执行幂等，并保留旧记录。
func TestFishingState48UpgradeAndRollback(t *testing.T) {
	db := openCatchHistoryDBAtVersion(t, 47)
	ctx := t.Context()
	if _, err := db.SQL.Exec(`INSERT INTO fishing_state(
			uid, gear_guid, rig_type, loc, spot, depth_cm, cast_active, cast_ts_ns,
			updated_ts, rod_guid
		) VALUES(7,'rod-a',5,'level_001_lake',22,37,1,123456789,42,'rod-a')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`CREATE TRIGGER fail_fishing_state_cast_m BEFORE INSERT ON schema_migrations
		WHEN NEW.name='migrations/00048_fishing_state_cast_m.sql'
		BEGIN SELECT RAISE(ABORT,'test migration failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err == nil {
		t.Fatal("失败的迁移被报告为成功")
	}
	assertCount(t, db, `SELECT COUNT(*) FROM pragma_table_info('fishing_state') WHERE name='cast_m'`, 0)
	assertCount(t, db, `SELECT COUNT(*) FROM fishing_state WHERE uid=7`, 1)
	if _, err := db.SQL.Exec(`DROP TRIGGER fail_fishing_state_cast_m`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.Migrate(); err != nil {
			t.Fatalf("迁移重试失败: %v", err)
		}
	}
	assertCount(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=48 AND name='migrations/00048_fishing_state_cast_m.sql'`, 1)
	assertCount(t, db, `SELECT COUNT(*) FROM pragma_table_info('fishing_state') WHERE name='cast_m'`, 1)
	states, err := db.LoadFishingStates(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 {
		t.Fatalf("旧记录数量=%d，期望 1", len(states))
	}
	got := states[0]
	if got.RodGUID != "rod-a" || got.DepthCM != 37 || !got.CastActive || got.CastM != 0 {
		t.Fatalf("旧记录迁移结果不符: %+v", got)
	}
}
