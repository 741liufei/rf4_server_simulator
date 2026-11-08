package gameserver

import "testing"

// lureTestHooks 读落库拟饵的出厂钩 GUID 列表。
func lureTestHooks(t *testing.T, s *Server, lureGUID string) []string {
	t.Helper()
	row, err := s.DB.GetItemByGUID(t.Context(), lureGUID)
	if err != nil || row == nil {
		t.Fatalf("读取拟饵 %s: %v", lureGUID, err)
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatalf("解析拟饵字段: %v", err)
	}
	idx := lureHookFieldIndex(row.WireClass)
	if idx < 0 || idx >= len(fields) || fields[idx].Val == nil {
		t.Fatalf("拟饵缺少钩数组字段")
	}
	return lureHookItemGUIDs(fields[idx].Val.Items)
}

// 15/1 换钩必须改拟饵实例 f0：钩要挂上去、旧钩让位、钩本体离开背包。
// 旧实现把钩写进竿式 f5/rig_component，拟饵 f0 不变，客户端看不到换钩效果。
func TestLureHookSwapRewritesFactoryHookList(t *testing.T) {
	t.Parallel()
	s, _ := newRigTestServer(t)
	lure, err := s.CreateItem(t.Context(), 1000001, "spinnerbaits_1244", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	hook, err := s.CreateItem(t.Context(), 1000001, "c_hook_20712", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}

	before := lureTestHooks(t, s, lure.GUID)
	if len(before) == 0 {
		t.Fatal("测试拟饵没有出厂钩")
	}

	res := s.rigSetComponent(t.Context(), 1000001, lure.GUID, catHook, hook.GUID, 250,
		rigSetOpts{ParentType: familyLure, EnforceCategory: true, SplitStack: true})
	if res.Rejected || res.MountedGUID != hook.GUID {
		t.Fatalf("拟饵换钩被拒: %+v", res)
	}

	after := lureTestHooks(t, s, lure.GUID)
	if len(after) != len(before) {
		t.Fatalf("换钩不应改变钩位数量: before=%d after=%d", len(before), len(after))
	}
	if after[0] != hook.GUID {
		t.Fatalf("钩位 0 应为新钩 %s，实得 %s", hook.GUID, after[0])
	}
	if before[0] == hook.GUID {
		t.Fatal("新钩未替换原钩")
	}

	row, err := s.DB.GetItemByGUID(t.Context(), hook.GUID)
	if err != nil || row == nil {
		t.Fatalf("读取钩实例: %v", err)
	}
	if row.Container != "rig" {
		t.Fatalf("钩应转为已装配(rig)，实得 %q", row.Container)
	}
	if got := s.lureActiveHookGUID(t.Context(), lure.GUID); got != hook.GUID {
		t.Fatalf("活动钩应指向新钩 %s，实得 %s", hook.GUID, got)
	}
	if len(res.UpdatedTrees) == 0 {
		t.Fatal("换钩后应把拟饵/钩的更新树放进 800 快照")
	}
}

// 15/2 拆钩要把钩从 f0 摘掉并退回背包。
func TestLureHookRemoveReturnsHookToBackpack(t *testing.T) {
	t.Parallel()
	s, _ := newRigTestServer(t)
	lure, err := s.CreateItem(t.Context(), 1000001, "spinnerbaits_1244", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	hook, err := s.CreateItem(t.Context(), 1000001, "c_hook_20712", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	before := lureTestHooks(t, s, lure.GUID)
	if res := s.rigSetComponent(t.Context(), 1000001, lure.GUID, catHook, hook.GUID, 250,
		rigSetOpts{ParentType: familyLure, EnforceCategory: true, SplitStack: true}); res.Rejected {
		t.Fatalf("拟饵换钩被拒: %+v", res)
	}

	det := s.rigRemoveComponentInstance(t.Context(), 1000001, lure.GUID, catHook, hook.GUID, 250)
	if det == nil || det.CompGUID != hook.GUID {
		t.Fatalf("拟饵拆钩失败: %+v", det)
	}
	after := lureTestHooks(t, s, lure.GUID)
	if len(after) != len(before)-1 {
		t.Fatalf("拆钩后钩位数量应为 %d，实得 %d", len(before)-1, len(after))
	}
	for _, g := range after {
		if g == hook.GUID {
			t.Fatal("拆下的钩仍在拟饵 f0 里")
		}
	}
	row, err := s.DB.GetItemByGUID(t.Context(), hook.GUID)
	if err != nil || row == nil {
		t.Fatalf("读取钩实例: %v", err)
	}
	if row.Container != "backpack" {
		t.Fatalf("拆下的钩应回到背包，实得容器 %q", row.Container)
	}
	if det.ReturnedTree == nil {
		t.Fatal("拆钩应返回钩的更新树")
	}
}

// 拟饵钩位不应在 rig_component 留下行：旧实现残留的幽灵行必须被清掉。
func TestLureHookSwapLeavesNoRigComponentRow(t *testing.T) {
	t.Parallel()
	s, db := newRigTestServer(t)
	lure, err := s.CreateItem(t.Context(), 1000001, "spinnerbaits_1244", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	hook, err := s.CreateItem(t.Context(), 1000001, "c_hook_20712", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertRigComponentAt(t.Context(), lure.GUID, catHook, 0, NewItemGUID(), 250); err != nil {
		t.Fatal(err)
	}
	if res := s.rigSetComponent(t.Context(), 1000001, lure.GUID, catHook, hook.GUID, 250,
		rigSetOpts{ParentType: familyLure, EnforceCategory: true, SplitStack: true}); res.Rejected {
		t.Fatalf("拟饵换钩被拒: %+v", res)
	}
	rows, err := db.ListRigComponents(t.Context(), lure.GUID)
	if err != nil {
		t.Fatal(err)
	}
	for _, rc := range rows {
		if rc.Slot == catHook {
			t.Fatalf("拟饵钩位仍在 rig_component 留行: ordinal=%d guid=%s", rc.Ordinal, rc.GUID)
		}
	}
}

// 拟饵与鱼钩都必须属于当前玩家；否则可直接操作别人的物品。
func TestLureHookRejectsForeignItems(t *testing.T) {
	t.Parallel()
	s, _ := newRigTestServer(t)
	const owner, stranger = int64(1000001), int64(1000002)

	lure, err := s.CreateItem(t.Context(), owner, "spinnerbaits_1244", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	foreignHook, err := s.CreateItem(t.Context(), stranger, "c_hook_20712", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	res := s.rigSetComponent(t.Context(), owner, lure.GUID, catHook, foreignHook.GUID, 250,
		rigSetOpts{ParentType: familyLure, EnforceCategory: true, SplitStack: true})
	if !res.Rejected {
		t.Fatalf("不应允许装配其他玩家的鱼钩: %+v", res)
	}

	foreignLure, err := s.CreateItem(t.Context(), stranger, "spinnerbaits_1244", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	ownHook, err := s.CreateItem(t.Context(), owner, "c_hook_20712", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	res = s.rigSetComponent(t.Context(), owner, foreignLure.GUID, catHook, ownHook.GUID, 250,
		rigSetOpts{ParentType: familyLure, EnforceCategory: true, SplitStack: true})
	if !res.Rejected {
		t.Fatalf("不应允许修改其他玩家的拟饵: %+v", res)
	}
	if det := s.rigRemoveComponentInstance(t.Context(), owner, foreignLure.GUID, catHook, "", 250); det != nil {
		t.Fatalf("不应允许拆其他玩家拟饵的钩: %+v", det)
	}
}

// 竿上拟饵的 bait_hook 关联以竿 GUID 为行、key=bait_hook:<拟饵GUID>；
// 换钩要改成新钩，拆钩要删除旧关联。
func TestLureHookSelectionTracksSwapAndRemove(t *testing.T) {
	t.Parallel()
	s, db := newRigTestServer(t)
	const uid = int64(1000001)
	lure, err := s.CreateItem(t.Context(), uid, "spinnerbaits_1244", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	before := lureTestHooks(t, s, lure.GUID)
	if len(before) == 0 {
		t.Fatal("测试拟饵没有出厂钩")
	}
	const rodGUID = "00112233445566778899aabbccddeeff"
	key := "bait_hook:" + lure.GUID
	if err := db.UpsertRodSetting(t.Context(), rodGUID, key, before[0]); err != nil {
		t.Fatal(err)
	}

	hook, err := s.CreateItem(t.Context(), uid, "c_hook_20712", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if res := s.rigSetComponent(t.Context(), uid, lure.GUID, catHook, hook.GUID, 250,
		rigSetOpts{ParentType: familyLure, EnforceCategory: true, SplitStack: true}); res.Rejected {
		t.Fatalf("拟饵换钩被拒: %+v", res)
	}
	var selected string
	if err := db.SQL.QueryRow(`SELECT value FROM rod_setting WHERE guid=? AND key=?`, rodGUID, key).Scan(&selected); err != nil {
		t.Fatal(err)
	}
	if selected != hook.GUID {
		t.Fatalf("换钩后 bait_hook 应指向新钩 %s，实得 %s", hook.GUID, selected)
	}

	if det := s.rigRemoveComponentInstance(t.Context(), uid, lure.GUID, catHook, hook.GUID, 250); det == nil {
		t.Fatal("拟饵拆钩失败")
	}
	var n int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM rod_setting WHERE guid=? AND key=?`, rodGUID, key).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("拆钩后 bait_hook 关联应删除，实存 %d 行", n)
	}
}
