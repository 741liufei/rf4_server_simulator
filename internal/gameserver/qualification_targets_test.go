package gameserver

import "testing"

// 目标表必须覆盖全部 168 条永久资格规则且值 > 0；抽样核对文案推导结果
// （数量型取 N，重量/深度/经验阈值型取 1）。周年活动分支不在资格树内，不在此表。
func TestQualificationTargetsCoverLadder(t *testing.T) {
	qualificationTarget(0) // 触发 sync.Once 加载，随后直接查表校验覆盖
	for b := uint64(1); b <= qualificationBranches; b++ {
		for s := uint64(1); s <= qualificationStages; s++ {
			for tk := uint64(1); tk <= qualificationTasks; tk++ {
				id := qualificationRuleID(b, s, tk)
				if v, ok := qualificationTargetsMap[id]; !ok || v == 0 {
					t.Fatalf("规则 %d 缺少目标值（ok=%v v=%d）", id, ok, v)
				}
			}
		}
	}
	cases := []struct {
		id   uint64
		want uint32
	}{
		{1001, 3},  // 钓获3种鱼
		{1002, 1},  // 钓获3公斤以上（含）的鱼：3 是重量阈值
		{1003, 7},  // 用7种不同的拟饵
		{1302, 4},  // 每种拟饵：列举 4 种
		{1401, 25}, // 25种鱼类
		{3601, 50}, // 50种鱼类
		{4402, 1},  // 至少7000点经验值：经验阈值
		{4602, 75}, // 75种鱼
		{6501, 1},  // 30公斤以上：重量阈值
		{7603, 5},  // 列举 5 种稀有鲤鱼
		{8303, 20}, // 20种鱼类
		{8401, 5},  // 120米深度钓获五种鱼类：120 是深度阈值
		{8502, 3},  // 三条稀有鱼
	}
	for _, c := range cases {
		if got := qualificationTarget(c.id); got != c.want {
			t.Fatalf("规则 %d 目标值应为 %d，实得 %d", c.id, c.want, got)
		}
	}
	if got := qualificationTarget(9999); got != 1 {
		t.Fatalf("未收录规则应回退 1，实得 %d", got)
	}
}
