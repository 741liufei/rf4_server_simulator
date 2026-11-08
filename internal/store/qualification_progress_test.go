package store

import (
	"context"
	"testing"
)

// 00047 必须可从旧库升级、失败不留半表、重复执行幂等。
func TestQualificationProgressUpgradeAndRollback(t *testing.T) {
	db := openCatchHistoryDBAtVersion(t, 46)
	if _, err := db.SQL.Exec(`CREATE TRIGGER fail_qual BEFORE INSERT ON schema_migrations
		WHEN NEW.name='migrations/00047_qualification_progress.sql'
		BEGIN SELECT RAISE(ABORT,'test migration failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err == nil {
		t.Fatal("失败的迁移被报告为成功")
	}
	assertCount(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE name='player_qualifications'`, 0)
	if _, err := db.SQL.Exec(`DROP TRIGGER fail_qual`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.Migrate(); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	if err := db.UpsertQualificationState(ctx, 7, QualificationState{
		RuleID: 1001, Counted: []QualificationFish{{GUID: "aa", Species: "perch"}, {GUID: "bb", Species: "roach"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertQualificationState(ctx, 7, QualificationState{
		RuleID: 1001, Counted: []QualificationFish{
			{GUID: "aa", Species: "perch", WeightG: 642, LengthM: .28, Grade: 2, RigType: 1},
			{GUID: "bb", Species: "roach"}, {GUID: "cc", Species: "bream"},
		}, CompletedTS: 123.5,
	}); err != nil {
		t.Fatal(err)
	}
	states, err := db.QualificationStates(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	st := states[1001]
	if len(st.Counted) != 3 || st.CompletedTS != 123.5 {
		t.Fatalf("进度往返不符: %+v", st)
	}
	if st.Counted[0].Species != "perch" || st.Counted[0].WeightG != 642 || st.Counted[0].RigType != 1 {
		t.Fatalf("鱼记录往返不符: %+v", st.Counted[0])
	}
	if empty, err := db.QualificationStates(ctx, 8); err != nil || len(empty) != 0 {
		t.Fatalf("其他玩家应为空: %v %v", empty, err)
	}
}
