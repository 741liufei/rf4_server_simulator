package store

// player_condition 扩列（00046）迁移测试：旧两列库升级后旧值保留、
// 新列回填默认；迁移可重复执行；Upsert 比例钳制生效。

import "testing"

func TestPlayerCondition46MigratesFromTwoColumns(t *testing.T) {
	db := openCatchHistoryDBAtVersion(t, 45)
	ctx := t.Context()
	if _, err := db.SQL.Exec(`INSERT INTO player_condition(uid, primary_value, secondary_value, updated_ts)
		VALUES(7, 0.62, 0.71, 42)`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.Migrate(); err != nil {
			t.Fatal(err)
		}
	}
	assertCount(t, db, `SELECT COUNT(*) FROM schema_migrations
		WHERE version=46 AND name='migrations/00046_player_condition_comfort_luck.sql'`, 1)
	row, err := db.EnsurePlayerCondition(ctx, 7)
	if err != nil || row == nil {
		t.Fatalf("旧档读取失败: %+v %v", row, err)
	}
	if row.Primary != 0.62 || row.Secondary != 0.71 {
		t.Fatalf("旧档数值被迁移改动: %+v", row)
	}
	if row.Comfort != DefaultPlayerConditionComfort || row.LuckPenalty != DefaultPlayerConditionLuckPenalty {
		t.Fatalf("新列回填默认值错误: %+v", row)
	}
	row.LuckPenalty = 1.5
	if err := db.UpsertPlayerCondition(ctx, *row); err != nil {
		t.Fatal(err)
	}
	row2, err := db.EnsurePlayerCondition(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if row2.LuckPenalty != 1 {
		t.Fatalf("luck_penalty 应钳到 [0,1]: %v", row2.LuckPenalty)
	}
	if row2.Comfort != row.Comfort || row2.Primary != row.Primary || row2.Secondary != row.Secondary {
		t.Fatalf("扩列写入不应影响既有列: %+v -> %+v", row, row2)
	}
}
