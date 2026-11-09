package gameserver

// 海钓钩位索引回归（2026-11-08 实机日志，杆 90e95cf8 / 3c0d7f44）：
//
//	18:58:21 装配 slot=12 ordinal=0 item=071993e7 extra=1001
//	20:44:38 装配 slot=12 ordinal=0 item=071993e7 extra=1001   ← 槽位变灰
//	20:44:47 装配 slot=12 ordinal=0 item=1df1daba extra=250    ← 成功
//	20:57:41 装配 slot=12 ordinal=0 item=8128dd25 extra=1005   ← 倒钓臂第 1 位
//	20:58:01 装配 slot=12 ordinal=0 item=544b8300 extra=1005
//	20:58:04 装配 slot=12 ordinal=0 item=071993e7 extra=1009   ← 倒钓臂第 3 位
//
// 同一个钩，extra=250 能装上，1001/1005/1009 都装不上（客户端整组变灰）。
// 根因：canonicalIndexForOrdinal 只认模板声明的索引（rig50/51 的钩位只有 250），
// 客户端上报的 1001/1005/1009 不属于模板索引时被无条件改写成 250，
// 客户端在自己的槽位上找不到组件，判整组无效。
//
// 注意：20:44 之后「钓组模板提示」出现 0 次，出问题的 rig50/51 都声明了钩位，
// 因此本缺陷与 rig53/54 模板缺声明无关，两个现象同源。

import "testing"

// TestHookSlotKeepsClientReportedIndex 断言客户端明确上报的钩位槽位索引
// 必须原样保留，不能被模板首索引改写。
// 这条在当前实现下必然失败（1001 → 250），是「先红」的那一条。
func TestHookSlotKeepsClientReportedIndex(t *testing.T) {
	for _, tc := range []struct {
		rigType     int64
		clientExtra int64
	}{
		{50, 1001},
		{51, 1001},
		{50, 1005},
		{50, 1009},
	} {
		got := canonicalIndexForOrdinal(catHook, tc.clientExtra, tc.rigType, 0)
		if got != tc.clientExtra {
			t.Fatalf("rig %d：客户端上报钩位索引 %d，被改写成 %d；"+
				"客户端在自己的槽位上找不到组件会整组判无效（槽位变灰、钩装不上）",
				tc.rigType, tc.clientExtra, got)
		}
	}
}

// TestMarineHookHighIndicesCoexist 复现现象 2（倒钓臂三个钩位）：
// 客户端连续给 1001/1005/1009 三个独立槽位装钩，服务端必须让三条组件
// 各自保留自己的索引并同时存在，不能被压成同一个 250 互相顶掉。
func TestMarineHookHighIndicesCoexist(t *testing.T) {
	srv, _ := newRigTestServer(t)
	ctx := t.Context()
	const uid = int64(1000001)
	rod, err := srv.CreateItem(ctx, uid, "pilkr_10212_90340", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !srv.rigSetType(ctx, uid, rod.GUID, 50) {
		t.Fatal("rigSetType 失败")
	}

	mount := func(extra int64) string {
		hook, err := srv.CreateItem(ctx, uid, "mhook_20924", "backpack", 1)
		if err != nil {
			t.Fatal(err)
		}
		res := srv.rigSetComponent(ctx, uid, rod.GUID, catHook, hook.GUID, extra,
			rigSetOpts{ParentType: familyRod, EnforceCategory: true})
		if res.Rejected || res.MountedGUID == "" {
			t.Fatalf("装钩(extra=%d) 被拒: %+v", extra, res)
		}
		return res.MountedGUID
	}
	for _, extra := range []int64{1001, 1005, 1009} {
		mount(extra)
	}

	comps := srv.storedRigComponents(ctx, rod.GUID)
	seen := map[int64]string{}
	for _, c := range comps {
		if c.Category != catHook {
			continue
		}
		if prev, dup := seen[c.Extra]; dup {
			t.Fatalf("钩位索引 %d 被两条组件占用: %s / %s", c.Extra, prev, shortGUID(c.GUID))
		}
		seen[c.Extra] = shortGUID(c.GUID)
	}
	for _, want := range []int64{1001, 1005, 1009} {
		if _, ok := seen[want]; !ok {
			t.Fatalf("钩位索引 %d 未保留，实际落库 = %v", want, seen)
		}
	}
	if len(seen) != 3 {
		t.Fatalf("钩位组件 = %d 个，期望 3 个互不冲突的槽位: %v", len(seen), seen)
	}
}
