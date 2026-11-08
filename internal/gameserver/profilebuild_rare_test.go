package gameserver

import (
	"context"
	"testing"
)

// 主资料页奖杯数（307.f16/trph）应等于 catch_log 里 grade>=3 的条数。
// 客户端把 f16 映射到资料卡奖杯位；该字段此前写死 1，钓到奖杯鱼后数字不动。
func TestProfile307StarCountsRareCatches(t *testing.T) {
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
	insert(uid, "fish_super", 4)     // 超级稀有，也计入稀有
	insert(uid, "fish_rare_b", 3)    // 稀有
	insert(uid+1, "fish_foreign", 4) // 其他玩家，不应计入

	p, err := srv.BuildProfile307(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if got := u32Of(p.Fields[16].Num); got != 3 {
		t.Fatalf("307.f16 奖杯数应为 3，实得 %d", got)
	}
}
