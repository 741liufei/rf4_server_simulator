package gameserver

import (
	"testing"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

// eventTestServer 直接预置运行参数缓存，避免测试依赖 30 秒刷新窗口。
func eventTestServer(t *testing.T, anniversaryOn bool) *Server {
	return eventTestServerWith(t, anniversaryOn, false)
}

func eventTestServerWith(t *testing.T, anniversaryOn, newYearOn bool) *Server {
	return eventTestServerBranches(t, anniversaryOn, newYearOn, defaultEventBranches)
}

// eventTestServerBranches 额外预置字符串参数缓存，用于验证分支配置解析。
func eventTestServerBranches(t *testing.T, anniversaryOn, newYearOn bool, branches string) *Server {
	t.Helper()
	srv, _ := newRigTestServer(t)
	srv.tunMu.Lock()
	srv.tunCache = map[string]float64{}
	srv.tunText = map[string]string{eventBranchesKey: branches}
	// 显式写 0，避免落到出厂默认（新年开），让"关闭"这一侧可确定。
	srv.tunCache[eventAnniversaryKey] = 0
	srv.tunCache[eventNewYearKey] = 0
	if anniversaryOn {
		srv.tunCache[eventAnniversaryKey] = 1
	}
	if newYearOn {
		srv.tunCache[eventNewYearKey] = 1
	}
	srv.tunAt = time.Now()
	srv.tunMu.Unlock()
	return srv
}

func environmentNodes(t *testing.T, srv *Server) *proto.Value {
	t.Helper()
	body, ok := hEnvironment(dynamicTestCtx(srv, nil))
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
	return root.Fields[1].Val
}

func TestAnniversarySwitchSeasonStages(t *testing.T) {
	off := eventTestServer(t, false).seasonStages()
	if len(off) != 1 || off[0] != 0 {
		t.Fatalf("关闭时应只发阶段 0，实得 %v", off)
	}
	on := eventTestServer(t, true).seasonStages()
	if len(on) != 2 || on[0] != 0 || on[1] != eventStageAnniversary {
		t.Fatalf("开启时应只发 [0 6]（补其它阶段号会把入口切成新年任务），实得 %v", on)
	}
}

func TestBuild5012CarriesStageEntries(t *testing.T) {
	body, err := proto.EncodeValue(build5012([]uint32{0, eventStageAnniversary}, seasonEndFiletime(0)), wiregen.Table())
	if err != nil {
		t.Fatalf("5012 编码失败: %v", err)
	}
	root, _, err := proto.DecodeValue(body, 0, wiregen.Table())
	if err != nil {
		t.Fatalf("5012 解码失败: %v", err)
	}
	entries := root.Fields[0].Val
	if entries == nil || len(entries.Items) != 2 {
		t.Fatalf("5013 条目应为 2 条，实得 %v", entries)
	}
	for i, want := range []uint64{0, uint64(eventStageAnniversary)} {
		entry := entries.Items[i]
		if entry.TypeID != 5013 {
			t.Fatalf("条目应为 5013，实得 %d", entry.TypeID)
		}
		if got := *entry.Fields[0].Num; got != want {
			t.Fatalf("第 %d 条 Idx0 应为 %d，实得 %d", i, want, got)
		}
	}
}

func TestEnvironmentAnniversaryNodesFollowSwitch(t *testing.T) {
	off := environmentNodes(t, eventTestServer(t, false))
	if len(off.Items) != qualificationBranches {
		t.Fatalf("关闭时应只有 8 个资格分支，实得 %d", len(off.Items))
	}
	for i, node := range off.Items {
		if got := *node.Fields[1].Num; got != 0 {
			t.Fatalf("资格节点 %d 的 Idx1 应为 0，实得 %d", i+1, got)
		}
	}

	srv := eventTestServer(t, true)
	on := environmentNodes(t, srv)
	branches := srv.eventBranches()
	if len(on.Items) != qualificationBranches+len(branches) {
		t.Fatalf("开启时应有 %d 个节点，实得 %d", qualificationBranches+len(branches), len(on.Items))
	}
	for i := 0; i < qualificationBranches; i++ {
		if got := *on.Items[i].Fields[1].Num; got != 0 {
			t.Fatalf("前 8 个仍须是 Idx1=0 的资格节点，第 %d 个实得 %d", i+1, got)
		}
	}
	type wantBranch struct {
		branch uint64
		counts []uint64
		total  int
	}
	wantCounts := make([]wantBranch, 0, len(branches))
	for _, branch := range branches {
		counts := eventTaskCounts[branch]
		total := 0
		for _, n := range counts {
			total += int(n)
		}
		wantCounts = append(wantCounts, wantBranch{branch, counts, total})
	}
	for i, want := range wantCounts {
		node := on.Items[qualificationBranches+i]
		if node.TypeID != 5009 {
			t.Fatalf("活动节点应为 5009，实得 %d", node.TypeID)
		}
		if got := *node.Fields[0].Num; got != want.branch {
			t.Fatalf("活动分支 ID 应为 %d，实得 %d", want.branch, got)
		}
		if got := *node.Fields[1].Num; got != uint64(eventStageAnniversary) {
			t.Fatalf("活动节点 Idx1 应为 6，实得 %d", got)
		}
		stages := node.Fields[3].Val
		if stages == nil || len(stages.Items) != len(want.counts) {
			t.Fatalf("分支 %d 应有 %d 阶段，实得 %v", want.branch, len(want.counts), stages)
		}
		total := 0
		for si, stage := range stages.Items {
			tasks := stage.Fields[1].Val
			if tasks == nil || uint64(len(tasks.Items)) != want.counts[si] {
				t.Fatalf("分支 %d 阶段 %d 任务数应为 %d，实得 %v",
					want.branch, si+1, want.counts[si], tasks)
			}
			for ti, task := range tasks.Items {
				rules := task.Fields[1].Val
				if rules == nil || len(rules.Items) != 1 {
					t.Fatalf("活动任务必须带 1 条规则")
				}
				rule := rules.Items[0]
				wantID := qualificationRuleID(want.branch, uint64(si+1), uint64(ti+1))
				if got := *rule.Fields[0].Num; got != wantID {
					t.Fatalf("活动规则类型应为 %d，实得 %d", wantID, got)
				}
				if got := *rule.Fields[3].Num; got == 0 {
					t.Fatalf("活动规则 %d 进度分母必须 > 0", wantID)
				}
			}
			total += len(tasks.Items)
		}
		if total != want.total {
			t.Fatalf("分支 %d 应有 %d 条任务，实得 %d", want.branch, want.total, total)
		}
	}
}

// 阶段数组按开关值追加：只开新年发 [0 5]。后台写入已互斥，这里同时验证
// 纯函数在两个值都为 1 时的行为仍是 [0 5 6]。
func TestNewYearSwitchSeasonStages(t *testing.T) {
	ny := eventTestServerWith(t, false, true).seasonStages()
	if len(ny) != 2 || ny[0] != 0 || ny[1] != eventStageNewYear {
		t.Fatalf("只开新年应发 [0 5]，实得 %v", ny)
	}
	both := eventTestServerWith(t, true, true).seasonStages()
	if len(both) != 3 || both[1] != eventStageNewYear || both[2] != eventStageAnniversary {
		t.Fatalf("两个都开应发 [0 5 6]，实得 %v", both)
	}
}

// 新年节点 Idx1=5，各分支的阶段数、任务数、文案编号与分母都合法。
func TestEnvironmentNewYearNodesFollowSwitch(t *testing.T) {
	srv := eventTestServerWith(t, false, true)
	on := environmentNodes(t, srv)
	branches := srv.eventBranches()
	if len(on.Items) != qualificationBranches+len(branches) {
		t.Fatalf("只开新年应有 %d 个节点，实得 %d", qualificationBranches+len(branches), len(on.Items))
	}
	for i := 0; i < qualificationBranches; i++ {
		if got := *on.Items[i].Fields[1].Num; got != 0 {
			t.Fatalf("前 8 个仍须是 Idx1=0 的资格节点，第 %d 个实得 %d", i+1, got)
		}
	}
	type wantBranch struct {
		branch uint64
		stages int
		total  int
	}
	want := make([]wantBranch, 0, len(branches))
	for _, branch := range branches {
		counts := eventTaskCounts[branch]
		if len(counts) == 0 {
			t.Fatalf("分支 %d 缺少任务数配置", branch)
		}
		total := 0
		for _, n := range counts {
			total += int(n)
		}
		want = append(want, wantBranch{branch, len(counts), total})
	}
	for i, w := range want {
		node := on.Items[qualificationBranches+i]
		if node.TypeID != 5009 {
			t.Fatalf("活动节点应为 5009，实得 %d", node.TypeID)
		}
		if got := *node.Fields[0].Num; got != w.branch {
			t.Fatalf("新年分支 ID 应为 %d，实得 %d", w.branch, got)
		}
		if got := *node.Fields[1].Num; got != uint64(eventStageNewYear) {
			t.Fatalf("新年节点 Idx1 应为 5，实得 %d", got)
		}
		stages := node.Fields[3].Val
		if stages == nil || len(stages.Items) != w.stages {
			t.Fatalf("分支 %d 应有 %d 阶段，实得 %v", w.branch, w.stages, stages)
		}
		total := 0
		for si, stage := range stages.Items {
			tasks := stage.Fields[1].Val
			if tasks == nil {
				t.Fatalf("分支 %d 阶段 %d 无任务列表", w.branch, si+1)
			}
			for ti, task := range tasks.Items {
				rules := task.Fields[1].Val
				if rules == nil || len(rules.Items) != 1 {
					t.Fatalf("新年任务必须带 1 条规则")
				}
				rule := rules.Items[0]
				wantID := qualificationRuleID(w.branch, uint64(si+1), uint64(ti+1))
				if got := *rule.Fields[0].Num; got != wantID {
					t.Fatalf("新年规则类型应为 %d，实得 %d", wantID, got)
				}
				if got := *rule.Fields[3].Num; got == 0 {
					t.Fatalf("新年规则 %d 进度分母必须 > 0", wantID)
				}
			}
			total += len(tasks.Items)
		}
		if total != w.total {
			t.Fatalf("分支 %d 应有 %d 条任务，实得 %d", w.branch, w.total, total)
		}
	}

	// 后台已互斥，但两个值同时为 1 时按阶段 5 优先，只下发一份新年节点。
	both := environmentNodes(t, eventTestServerWith(t, true, true))
	if len(both.Items) != qualificationBranches+len(branches) {
		t.Fatalf("两个都开时应只下发一份节点，共 %d 个，实得 %d",
			qualificationBranches+len(branches), len(both.Items))
	}
	if got := *both.Items[qualificationBranches].Fields[1].Num; got != uint64(eventStageNewYear) {
		t.Fatalf("两个都开时应按阶段 5 优先，实得 %d", got)
	}
}

// 分支配置：逗号分隔、去重、排序、过滤非法 ID、空值不下发。
func TestEventBranchesFromConfig(t *testing.T) {
	cases := []struct {
		raw  string
		want []uint64
	}{
		{"17,18", []uint64{17, 18}},
		{"20,9,15,9", []uint64{9, 15, 20}},
		{" 19 , 20 ", []uint64{19, 20}},
		{"99,7,abc,17", []uint64{17}},
		{"", nil},
		{"abc", nil},
	}
	for _, c := range cases {
		srv := eventTestServerBranches(t, false, true, c.raw)
		got := srv.eventBranches()
		if len(got) != len(c.want) {
			t.Fatalf("%q 应得 %v，实得 %v", c.raw, c.want, got)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("%q 应得 %v，实得 %v", c.raw, c.want, got)
			}
		}
	}
	// 空配置时 28/1 仍应只有 8 个资格节点。
	empty := environmentNodes(t, eventTestServerBranches(t, false, true, ""))
	if len(empty.Items) != qualificationBranches {
		t.Fatalf("空配置应只有 8 个资格节点，实得 %d", len(empty.Items))
	}
	// 周年开关改变的是 Idx1，分支集合不变。
	anniv := eventTestServerBranches(t, true, false, "17,18")
	nodes := environmentNodes(t, anniv)
	if len(nodes.Items) != qualificationBranches+2 {
		t.Fatalf("配置 17,18 应有 10 个节点，实得 %d", len(nodes.Items))
	}
	if got := *nodes.Items[qualificationBranches].Fields[1].Num; got != uint64(eventStageAnniversary) {
		t.Fatalf("周年开关下 Idx1 应为 6，实得 %d", got)
	}
}

// 12 分支同屏实验：确认 28/1 报文能完整编码，并记录实际规模。
func TestEnvironmentAllBranchesEncodes(t *testing.T) {
	srv := eventTestServerWith(t, false, true)
	body, ok := hEnvironment(dynamicTestCtx(srv, nil))
	if !ok || len(body) == 0 {
		t.Fatalf("28/1 编码失败或为空: len=%d ok=%v", len(body), ok)
	}
	t.Logf("当前 %d 个活动分支时 28/1 报文 = %d 字节",
		len(srv.eventBranches()), len(body))
}

func TestPlayerRewards2015EmptyArray(t *testing.T) {
	srv, _ := newRigTestServer(t)
	registerAchievementHandlers(srv.dispatch)
	if srv.dispatch.lookup(3, 16) == nil {
		t.Fatal("3/16 未注册 handler")
	}
	body, ok := hPlayerRewards2015(dynamicTestCtx(srv, nil))
	if !ok {
		t.Fatal("3/16 未处理")
	}
	want := []byte{0x03, 0x03, '1', '8', '5', 0x00, 0x00}
	if len(body) != len(want) {
		t.Fatalf("3/16 应返回空 args 数组，长度 %d，实得 %x", len(want), body)
	}
	for i := range want {
		if body[i] != want[i] {
			t.Fatalf("3/16 应返回 args(\"185\") 空数组 %x，实得 %x", want, body)
		}
	}
}

// 5013.Idx4 是后台配置的结束时间；Idx1/Idx2 仍用 seasonBounds，
// Idx3 是按 ClassOps 生成的 1 字节 OPT 零值。
func TestBuild5013SeasonEndFromConfig(t *testing.T) {
	start, seasonEnd := seasonBounds()
	configured := seasonEndFiletime(1900000000)
	body, err := proto.EncodeValue(build5013(eventStageAnniversary, configured), wiregen.Table())
	if err != nil {
		t.Fatalf("5013 编码失败: %v", err)
	}
	dec, _, err := proto.DecodeValue(body, 0, wiregen.Table())
	if err != nil {
		t.Fatalf("5013 解码失败: %v", err)
	}
	if len(dec.Fields) != 6 {
		t.Fatalf("5013 应有 6 个字段，实得 %d", len(dec.Fields))
	}
	if got := *dec.Fields[0].Num; got != uint64(eventStageAnniversary) {
		t.Fatalf("Idx0 阶段应为 %d，实得 %d", eventStageAnniversary, got)
	}
	if got := *dec.Fields[1].Num; got != start {
		t.Fatalf("Idx1 起点应保持 seasonBounds: got=%d want=%d", got, start)
	}
	if got := *dec.Fields[2].Num; got != seasonEnd {
		t.Fatalf("Idx2 终点应保持 seasonBounds: got=%d want=%d", got, seasonEnd)
	}
	if dec.Fields[3].Op != "OPT" || dec.Fields[3].Num == nil || *dec.Fields[3].Num != 0 {
		t.Fatalf("Idx3 应为 1 字节 OPT 零值，实得 %+v", dec.Fields[3])
	}
	if got := *dec.Fields[4].Num; got != configured {
		t.Fatalf("Idx4 应为配置结束时间 %d，实得 %d", configured, got)
	}
}

// 缺省用固定锚点（倒计时会逐日递减），配置了就用配置值。
func TestSeasonEndReadsTunable(t *testing.T) {
	wantDefault := uint64(proto.UnixToFiletime(defaultEventEndUnix))
	if got := seasonEndFiletime(0); got != wantDefault {
		t.Fatalf("缺省结束时间应为固定锚点 %d，实得 %d", wantDefault, got)
	}
	srv := eventTestServer(t, false)
	if got := srv.seasonEnd(); got != wantDefault {
		t.Fatalf("未配置时 seasonEnd 应为锚点 %d，实得 %d", wantDefault, got)
	}
	const custom = 1900000000
	srv.tunCache[eventEndKey] = custom
	if got, want := srv.seasonEnd(), uint64(proto.UnixToFiletime(custom)); got != want {
		t.Fatalf("配置结束时间应为 %d，实得 %d", want, got)
	}
}

// TestEventFactoryDefaults 锁死出厂默认：新年活动开、周年关、12 个分支全挂。
// 两个默认值必须互斥，否则缺配置时两个面板会同时点亮。
func TestEventFactoryDefaults(t *testing.T) {
	srv, _ := newRigTestServer(t)
	srv.tunMu.Lock()
	srv.tunCache = map[string]float64{}
	srv.tunText = map[string]string{}
	srv.tunAt = time.Now()
	srv.tunMu.Unlock()
	if !srv.newYearEnabled() || srv.anniversaryEnabled() {
		t.Fatalf("出厂默认应为新年开、周年关，实得 新年=%v 周年=%v",
			srv.newYearEnabled(), srv.anniversaryEnabled())
	}
	if got := srv.seasonStages(); len(got) != 2 || got[0] != 0 || got[1] != eventStageNewYear {
		t.Fatalf("出厂默认阶段应为 [0,5]，实得 %v", got)
	}
	if got := len(srv.eventBranches()); got != 12 {
		t.Fatalf("出厂默认应挂 12 个分支，实得 %d", got)
	}
}
