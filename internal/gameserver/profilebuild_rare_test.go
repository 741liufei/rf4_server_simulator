package gameserver

import (
	"context"
	"testing"
)

// 主资料页奖杯数（307.f16/trph）按鱼种去重：钓到过 grade>=3（重量稀有/
// 超级稀有）的鱼种各计一次，同鱼种多条稀有渔获不重复计数。客户端把
// f16 与 24/19.f5 同落 ifbfdldbana.gebeadddmlp（等级旁星号同一字段）；
// 统计页 3/2 的按条数口径不经此路径。该字段此前写死 1，钓到奖杯鱼后数字不动。
func TestProfile307StarCountsRareSpecies(t *testing.T) {
	db := connectTestDB(t)
	srv := NewServerForGearTest(t, db)
	ctx := context.Background()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}

	insert := func(owner int64, fish string, grade int64) {
		t.Helper()
		if err := db.CatchLogInsert(ctx, owner, "level_001_lake", 1, fish, "测试鱼",
			100, 0.2, "keep", "worm_01", grade, 10, 1, "level_001_lake"); err != nil {
			t.Fatalf("插入钓获记录 %s: %v", fish, err)
		}
	}
	insert(uid, "fish_common", 2)    // 普通鱼，不计
	insert(uid, "fish_rare_a", 3)    // 稀有
	insert(uid, "fish_rare_a", 3)    // 同鱼种第二条稀有，去重后不重复计
	insert(uid, "fish_super", 4)     // 超级稀有，也计入
	insert(uid, "fish_rare_b", 3)    // 稀有
	insert(uid+1, "fish_foreign", 4) // 其他玩家，不应计入

	// store 层口径（24/19 f5 与 307.f16 共用）。
	n, err := db.RareSpeciesCount(ctx, uid, 3)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("RareSpeciesCount 应为 3 个鱼种（按条数会是 4），实得 %d", n)
	}

	p, err := srv.BuildProfile307(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if got := u32Of(p.Fields[16].Num); got != 3 {
		t.Fatalf("307.f16 奖杯鱼种数应为 3，实得 %d", got)
	}
}
