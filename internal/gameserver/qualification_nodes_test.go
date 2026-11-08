package gameserver

import (
	"testing"

	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

// 28/1 的 5011.f1 是资格面板唯一的数据来源：必须下发 8 个永久分支 × 7 阶段 × 3 任务，
// 且每个分支节点的 Idx1 为 0（资格面板的过滤谓词）。周年/活动分支严禁混入此树。
func TestEnvironmentEmitsQualificationTree(t *testing.T) {
	body, ok := hEnvironment(&Ctx{})
	if !ok {
		t.Fatal("28/1 未处理")
	}
	root, _, err := proto.DecodeValue(body, 0, wiregen.Table())
	if err != nil {
		t.Fatalf("28/1 解码失败: %v", err)
	}
	if root.TypeID != 5011 {
		t.Fatalf("根对象应为 5011，实得 %d", root.TypeID)
	}
	nodes := root.Fields[1].Val
	if nodes == nil || len(nodes.Items) != qualificationBranches {
		t.Fatalf("分支数应为 %d，实得 %v", qualificationBranches, nodes)
	}
	for bi, node := range nodes.Items {
		branch := uint64(bi + 1)
		if node.TypeID != 5009 {
			t.Fatalf("分支节点应为 5009，实得 %d", node.TypeID)
		}
		if got := *node.Fields[0].Num; got != branch {
			t.Fatalf("Idx0 分支 ID 应为 %d，实得 %d", branch, got)
		}
		// 资格面板只接受 Idx1 == 0 的节点。
		if got := *node.Fields[1].Num; got != 0 {
			t.Fatalf("分支 %d 的 Idx1 必须为 0，实得 %d", branch, got)
		}
		stages := node.Fields[3].Val
		if stages == nil || len(stages.Items) != qualificationStages {
			t.Fatalf("分支 %d 阶段数应为 %d", branch, qualificationStages)
		}
		for si, stage := range stages.Items {
			stageNo := uint64(si + 1)
			if stage.TypeID != 5008 {
				t.Fatalf("阶段对象应为 5008，实得 %d", stage.TypeID)
			}
			if got := *stage.Fields[0].Num; got != stageNo {
				t.Fatalf("阶段号应为 %d，实得 %d", stageNo, got)
			}
			tasks := stage.Fields[1].Val
			if tasks == nil || len(tasks.Items) != qualificationTasks {
				t.Fatalf("分支 %d 阶段 %d 任务数应为 %d", branch, stageNo, qualificationTasks)
			}
			for ti, task := range tasks.Items {
				if task.TypeID != 5019 {
					t.Fatalf("任务对象应为 5019，实得 %d", task.TypeID)
				}
				rules := task.Fields[1].Val
				// 每条任务必须至少带 1 条规则，否则进度 0/0 → NaN，面板永久转圈。
				if rules == nil || len(rules.Items) != 1 {
					t.Fatalf("分支 %d 阶段 %d 任务 %d 必须带 1 条规则", branch, stageNo, ti+1)
				}
				rule := rules.Items[0]
				if rule.TypeID != 5007 {
					t.Fatalf("规则对象应为 5007，实得 %d", rule.TypeID)
				}
				want := qualificationRuleID(branch, stageNo, uint64(ti+1))
				if got := *rule.Fields[0].Num; got != want {
					t.Fatalf("规则类型应为 %d，实得 %d", want, got)
				}
				// 进度分母必须等于静态目标值且 > 0，否则进度除零。
				if got := *rule.Fields[3].Num; got != uint64(qualificationTarget(want)) || got == 0 {
					t.Fatalf("分支 %d 阶段 %d 任务 %d 进度分母应为 %d，实得 %d",
						branch, stageNo, ti+1, qualificationTarget(want), got)
				}
			}
		}
	}
}

// 规则类型编号与客户端本地化键 qlfctn_t_<id> 一一对应，必须落在 1001..8603 的阶梯上。
func TestQualificationRuleIDLadder(t *testing.T) {
	cases := []struct{ branch, stage, task, want uint64 }{
		{1, 1, 1, 1001},
		{1, 1, 3, 1003},
		{1, 2, 1, 1101},
		{1, 7, 3, 1603},
		{2, 1, 1, 2001},
		{8, 7, 3, 8603},
	}
	for _, c := range cases {
		if got := qualificationRuleID(c.branch, c.stage, c.task); got != c.want {
			t.Fatalf("qualificationRuleID(%d,%d,%d)=%d，期望 %d", c.branch, c.stage, c.task, got, c.want)
		}
	}
	seen := make(map[uint64]bool, 168)
	for b := uint64(1); b <= qualificationBranches; b++ {
		for s := uint64(1); s <= qualificationStages; s++ {
			for tk := uint64(1); tk <= qualificationTasks; tk++ {
				id := qualificationRuleID(b, s, tk)
				if seen[id] {
					t.Fatalf("规则类型 %d 重复", id)
				}
				seen[id] = true
			}
		}
	}
	if len(seen) != 168 {
		t.Fatalf("任务总数应为 168，实得 %d", len(seen))
	}
}
