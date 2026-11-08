package gameserver

// 按挂饵方式数量拆分配饵回归（2026-11-05，官方模型反编译证伪整包装填）：
// 饵料挂载拆「挂饵方式」对应的一钩份数（默认一份），来源堆保留在背包——
// 同种饵可挂到其他竿；咬口逐份扣减，用尽后 rebait + 抛竿前自动补挂；
// 整堆上钩时删除来源堆并清引用。发丝族按子实例数量渲染并计重，
// 多余份数上钩会超重，所以数量必须跟着挂饵方式走。

import (
	"testing"
)

// mountBaitForCountTest 在 rod 上挂 id 数量的饵，返回 (来源堆 guid, 挂载 guid)。
func mountBaitForCountTest(t *testing.T, s *Server, rod, id string, qty int64) (string, string) {
	t.Helper()
	item, err := s.CreateItem(t.Context(), 1000001, id, "backpack", qty)
	if err != nil {
		t.Fatal(err)
	}
	if s.stackableItem(t.Context(), item) {
		s.setStackCount(t.Context(), item, qty)
	}
	r := s.rigSetComponent(t.Context(), 1000001, rod, 0, item.GUID, 350,
		rigSetOpts{ParentType: familyRod, EnforceCategory: true, SplitStack: true})
	if r.Rejected || r.MountedGUID == "" {
		t.Fatalf("挂载 %s 被拒: %+v", id, r)
	}
	return item.GUID, r.MountedGUID
}

// saveMountTypeForTest 直接写入玩家在某竿选过的挂饵方式。
func saveMountTypeForTest(t *testing.T, s *Server, rod string, mountType int64) {
	t.Helper()
	if err := s.DB.UpsertRodSetting(t.Context(), rod, baitMountSettingKey, itoa64(mountType)); err != nil {
		t.Fatal(err)
	}
}

func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func TestBaitMountSplitsSingleUnitByDefault(t *testing.T) {
	t.Parallel()
	s, _ := newRigTestServer(t)
	rod, err := s.CreateItem(t.Context(), 1000001, "tele_10112_5", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	src, child := mountBaitForCountTest(t, s, rod.GUID, "bread", 15)

	// 未选挂饵方式（f8=0）：官方行为一钩一份，来源堆留在背包。
	srcRow, _ := s.DB.GetItemByGUID(t.Context(), src)
	if srcRow == nil || srcRow.Container != "backpack" || baitQuantity(srcRow) != 14 {
		t.Fatalf("默认挂载后来源堆应留在背包 14 份: %+v", srcRow)
	}
	mounted, _ := s.DB.GetItemByGUID(t.Context(), child)
	if mounted == nil || mounted.Container != "rig" {
		t.Fatalf("挂载实例应落 rig 容器: %+v", mounted)
	}
	if got := baitQuantity(mounted); got != 1 {
		t.Fatalf("未选挂饵方式时应一钩一份，实得 %d", got)
	}
	if splitSourceGUID(mounted) != src {
		t.Fatal("拆分子实例应携带来源堆引用")
	}
	if baits := s.currentBaits(t.Context(), rod.GUID); len(baits) != 1 || baits[0] != "bread" {
		t.Fatalf("currentBaits=%v，期望 [bread]", baits)
	}
}

func TestBaitMountRodBSharesSourceStack(t *testing.T) {
	t.Parallel()
	s, _ := newRigTestServer(t)
	rodA, err := s.CreateItem(t.Context(), 1000001, "tele_10112_5", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	rodB, err := s.CreateItem(t.Context(), 1000001, "feeder_10126_FD390", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	src, childA := mountBaitForCountTest(t, s, rodA.GUID, "bloodworm", 15)

	// 用户场景回归：杆 A 挂过红虫后，杆 B 仍能从同一来源堆挂同种饵。
	r := s.rigSetComponent(t.Context(), 1000001, rodB.GUID, 0, src, 350,
		rigSetOpts{ParentType: familyRod, EnforceCategory: true, SplitStack: true})
	if r.Rejected || r.MountedGUID == "" {
		t.Fatalf("杆 B 挂同种饵被拒: %+v", r)
	}
	childB := r.MountedGUID
	if childB == childA {
		t.Fatal("杆 B 应拆出新子实例，而不是复用杆 A 的挂载")
	}
	srcRow, _ := s.DB.GetItemByGUID(t.Context(), src)
	if srcRow == nil || baitQuantity(srcRow) != 13 {
		t.Fatalf("两次挂载后来源堆应剩 13 份: %+v", srcRow)
	}
	for _, guid := range []string{childA, childB} {
		row, _ := s.DB.GetItemByGUID(t.Context(), guid)
		if row == nil || row.Container != "rig" || baitQuantity(row) != 1 {
			t.Fatalf("每杆应各挂一份: guid=%s row=%+v", guid, row)
		}
	}
}

func TestBaitMountCountFollowsMountType(t *testing.T) {
	t.Parallel()
	s, _ := newRigTestServer(t)
	rod, err := s.CreateItem(t.Context(), 1000001, "feeder_10126_FD390", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	// 蛆组合挂法只属于底蛆钓组（29）：先换钓组再选挂法（钓组↔挂法联动）。
	if !s.rigSetType(t.Context(), 1000001, rod.GUID, 29) {
		t.Fatal("rigSetType 失败")
	}
	// 玉米浮+蛆15（262144）：蛆一钩 15 份。
	saveMountTypeForTest(t, s, rod.GUID, 262144)
	src, child := mountBaitForCountTest(t, s, rod.GUID, "maggot", 48)
	mounted, _ := s.DB.GetItemByGUID(t.Context(), child)
	if mounted == nil || baitQuantity(mounted) != 15 {
		t.Fatalf("玉米浮+蛆15 应拆 15 份蛆上钩: %+v", mounted)
	}
	srcRow, _ := s.DB.GetItemByGUID(t.Context(), src)
	if srcRow == nil || baitQuantity(srcRow) != 33 {
		t.Fatalf("拆出 15 份后来源堆应剩 33: %+v", srcRow)
	}

	// 同一挂法下玉米浮仍是一颗（组合里的另一族数量不进表）。
	_, cornChild := mountBaitForCountTest(t, s, rod.GUID, "crn_cb_bfc", 6)
	cornRow, _ := s.DB.GetItemByGUID(t.Context(), cornChild)
	if cornRow == nil || baitQuantity(cornRow) != 1 {
		t.Fatalf("玉米浮在组合挂法下应一颗: %+v", cornRow)
	}
}

func TestBaitMountFullTakeClearsSourceRef(t *testing.T) {
	t.Parallel()
	s, _ := newRigTestServer(t)
	rod, err := s.CreateItem(t.Context(), 1000001, "feeder_10126_FD390", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	// 玉米浮族属于 pop-up 钓组（26）：先换钓组再选挂法。
	if !s.rigSetType(t.Context(), 1000001, rod.GUID, 26) {
		t.Fatal("rigSetType 失败")
	}
	// 玉米浮×2（1024），背包恰好 2 颗：整堆上钩，来源堆删除且不携带引用。
	saveMountTypeForTest(t, s, rod.GUID, 1024)
	src, child := mountBaitForCountTest(t, s, rod.GUID, "crn_cb_bfc", 2)
	if row, _ := s.DB.GetItemByGUID(t.Context(), src); row != nil {
		t.Fatalf("整堆上钩后来源堆必须删除: %+v", row)
	}
	mounted, _ := s.DB.GetItemByGUID(t.Context(), child)
	if mounted == nil || baitQuantity(mounted) != 2 {
		t.Fatalf("玉米浮×2 应整堆 2 颗上钩: %+v", mounted)
	}
	if splitSourceGUID(mounted) != "" {
		t.Fatal("整堆上钩实例不应携带来源堆引用")
	}
}

func TestBottomSimpleFullMaskPersistsMountSelection(t *testing.T) {
	t.Parallel()
	s, _ := newRigTestServer(t)
	rod, err := s.CreateItem(t.Context(), 1000001, "feeder_10126_FD390", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !s.rigSetType(t.Context(), 1000001, rod.GUID, 12) {
		t.Fatal("rigSetType 失败")
	}
	saveMountTypeForTest(t, s, rod.GUID, 1024) // 玉米浮×2，超出旧 126 掩码
	if got, ok := s.savedBaitMount(t.Context(), rod.GUID); !ok || got != 1024 {
		t.Fatalf("BottomSimple 全开掩码下挂饵方式未持久化: got=%d ok=%v", got, ok)
	}
	corn, err := s.CreateItem(t.Context(), 1000001, "crn_cb_bfc", "backpack", 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.baitHookCount(t.Context(), rod.GUID, corn); got != 2 {
		t.Fatalf("玉米浮×2 应按挂饵方式拆 2 份，实得 %d", got)
	}
}

func TestBaitMountBiteConsumesSingleUnitKeepsHookBaited(t *testing.T) {
	t.Parallel()
	s, db := newRigTestServer(t)
	rod, err := s.CreateItem(t.Context(), 1000001, "tele_10112_5", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	saveMountTypeForTest(t, s, rod.GUID, 131072) // 玉米浮+蛆10
	_, child := mountBaitForCountTest(t, s, rod.GUID, "bloodworm", 30)

	for i := 1; i <= 3; i++ {
		if _, err := s.consumeBaitEvent(t.Context(), 1000001, rod.GUID,
			"bite-mc-"+string(rune('0'+i)), []string{child}, false); err != nil {
			t.Fatal(err)
		}
	}
	mounted, _ := s.DB.GetItemByGUID(t.Context(), child)
	if mounted == nil {
		t.Fatal("三次咬口后钩上饵不应消失")
	}
	if got := baitQuantity(mounted); got != 7 {
		t.Fatalf("10 份上钩三次咬口后应剩 7，实得 %d", got)
	}
	var n int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM rod_setting WHERE guid=? AND key LIKE 'rebait:%'`,
		rod.GUID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("钩上还有饵时不应写补饵记录，实得 %d 条", n)
	}
}

func TestBaitMountExhaustWritesRebaitAndAutoRemountsSameCount(t *testing.T) {
	t.Parallel()
	s, _ := newRigTestServer(t)
	rod, err := s.CreateItem(t.Context(), 1000001, "feeder_10126_FD390", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	// 蛆组合挂法属于底蛆钓组（29）：先换钓组再选挂法。
	if !s.rigSetType(t.Context(), 1000001, rod.GUID, 29) {
		t.Fatal("rigSetType 失败")
	}
	// 补饵以钓组仍有钩为前提；无钩钓组的补饵请求按无效处理。
	mountHookForBaitTest(t, s, rod.GUID, 0)
	saveMountTypeForTest(t, s, rod.GUID, 65536) // 玉米浮+蛆5
	_, child := mountBaitForCountTest(t, s, rod.GUID, "bloodworm", 5)
	for i := 1; i <= 5; i++ {
		if _, err := s.consumeBaitEvent(t.Context(), 1000001, rod.GUID,
			"bite-ex-"+string(rune('0'+i)), []string{child}, false); err != nil {
			t.Fatal(err)
		}
	}
	if row, _ := s.DB.GetItemByGUID(t.Context(), child); row != nil {
		t.Fatalf("5 份用完后挂载实例应删除: %+v", row)
	}
	val, err := s.DB.GetRodSetting(t.Context(), rod.GUID, "rebait:0")
	if err != nil || val == "" {
		t.Fatalf("用完后应写 rebait:0 记录: %q %v", val, err)
	}

	// 背包再放 7 份，下次抛竿前自动补挂仍按挂饵方式拆 5 份。
	stack, err := s.CreateItem(t.Context(), 1000001, "bloodworm", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	s.setStackCount(t.Context(), stack, 7)
	if _, err := s.reloadBaitForCast(t.Context(), 1000001, rod.GUID); err != nil {
		t.Fatal(err)
	}
	var mountedQty int64 = -1
	rows, err := s.DB.ListRigComponents(t.Context(), rod.GUID)
	if err != nil {
		t.Fatal(err)
	}
	for _, rc := range rows {
		if row, _ := s.DB.GetItemByGUID(t.Context(), rc.GUID); row != nil && row.ItemID == "bloodworm" {
			mountedQty = baitQuantity(row)
		}
	}
	if mountedQty != 5 {
		t.Fatalf("自动补挂应按挂饵方式拆 5 份，实得 %d", mountedQty)
	}
}

func TestBaitMountUnmountRefundsRemainder(t *testing.T) {
	t.Parallel()
	s, _ := newRigTestServer(t)
	rod, err := s.CreateItem(t.Context(), 1000001, "feeder_10126_FD390", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	// 蛆组合挂法属于底蛆钓组（29）：先换钓组再选挂法。
	if !s.rigSetType(t.Context(), 1000001, rod.GUID, 29) {
		t.Fatal("rigSetType 失败")
	}
	saveMountTypeForTest(t, s, rod.GUID, 262144) // 玉米浮+蛆15
	src, child := mountBaitForCountTest(t, s, rod.GUID, "maggot", 20)
	for i := 1; i <= 3; i++ {
		if _, err := s.consumeBaitEvent(t.Context(), 1000001, rod.GUID,
			"bite-um-"+string(rune('0'+i)), []string{child}, false); err != nil {
			t.Fatal(err)
		}
	}
	res := s.rigRemoveComponentInstance(t.Context(), 1000001, rod.GUID, catBait, child, 350)
	if res == nil || res.CompGUID == "" {
		t.Fatalf("拆卸失败: %+v", res)
	}
	if row, _ := s.DB.GetItemByGUID(t.Context(), child); row != nil {
		t.Fatalf("拆卸后拆分子实例应删除: %+v", row)
	}
	back, _ := s.DB.GetItemByGUID(t.Context(), src)
	// 拆出 15（源堆剩 5）+ 咬口吃掉 3（钩上剩 12）→ 退回后源堆 5+12=17。
	if back == nil || back.Container != "backpack" || baitQuantity(back) != 17 {
		t.Fatalf("拆卸应把钩上剩余 12 份退回来源堆（5+12=17），实得: %+v", back)
	}
	if baits := s.currentBaits(t.Context(), rod.GUID); len(baits) != 0 {
		t.Fatalf("拆卸后钩上饵=%v，期望为空", baits)
	}
}

func TestBaitMountFullTakeUnmountReturnsWholeStack(t *testing.T) {
	t.Parallel()
	s, _ := newRigTestServer(t)
	rod, err := s.CreateItem(t.Context(), 1000001, "tele_10112_5", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	saveMountTypeForTest(t, s, rod.GUID, 2048) // 玉米浮×4
	_, child := mountBaitForCountTest(t, s, rod.GUID, "crn_cb_bfc", 4)

	res := s.rigRemoveComponentInstance(t.Context(), 1000001, rod.GUID, catBait, child, 350)
	if res == nil || res.CompGUID == "" {
		t.Fatalf("拆卸失败: %+v", res)
	}
	back, _ := s.DB.GetItemByGUID(t.Context(), child)
	if back == nil || back.Container != "backpack" || baitQuantity(back) != 4 {
		t.Fatalf("整堆上钩拆卸后应整体回到背包 4 份: %+v", back)
	}
}

func TestBaitMountKindOfScopesToBaitCategory(t *testing.T) {
	t.Parallel()
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	// fopbhlmkcpe 是共用子类型字段（竿型的字段值恰好也常落在 1..10），
	// 非饵品类必须判 Other，否则挂玉米浮×2 时竿/钩会被误拆多份。
	row, err := s.CreateItem(ctx, 1000001, "tele_10112_5", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.baitMountKindOf(ctx, row); got != baitKindOther {
		t.Fatalf("竿实例不应判成组件族，实得 %d", got)
	}
	// 泡沫按前缀判别，不受品类限制。
	foamRow, err := s.CreateItem(ctx, 1000001, "hm_foamr_80black_hq", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.baitMountKindOf(ctx, foamRow); got != baitKindFoam {
		t.Fatalf("泡沫应判 foam 族，实得 %d", got)
	}
}

func TestFoamMountX2UsesTwoLureComponents(t *testing.T) {
	t.Parallel()
	s, db := newRigTestServer(t)
	rod, err := s.CreateItem(t.Context(), 1000001, "feeder_10126_FD390", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	// 泡沫饵属于 Zig 钓组（34）。
	if !s.rigSetType(t.Context(), 1000001, rod.GUID, 34) {
		t.Fatal("rigSetType 失败")
	}
	saveMountTypeForTest(t, s, rod.GUID, 1048576) // 泡沫×2
	hook, err := s.CreateItem(t.Context(), 1000001, "c_hook_20819", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if r := s.rigSetComponent(t.Context(), 1000001, rod.GUID, 0, hook.GUID, 0,
		rigSetOpts{ParentType: familyRod, EnforceCategory: true, SplitStack: true}); r.Rejected {
		t.Fatalf("挂钩失败: %+v", r)
	}
	first, err := s.CreateItem(t.Context(), 1000001, "hm_foamr_80black_hq", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateItem(t.Context(), 1000001, "hm_foamr_80black_hq", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, guid := range []string{first.GUID, second.GUID} {
		r := s.rigSetComponent(t.Context(), 1000001, rod.GUID, 0, guid, 0,
			rigSetOpts{ParentType: familyRod, EnforceCategory: true, SplitStack: true})
		if r.Rejected || r.MountedGUID != guid {
			t.Fatalf("泡沫挂载失败 guid=%s result=%+v", shortGUID(guid), r)
		}
	}
	fresh, err := db.GetItemByGUID(t.Context(), rod.GUID)
	if err != nil {
		t.Fatal(err)
	}
	comps := rigTestCategoryComps(t, rigStoredF5(t, fresh), catLure)
	if len(comps) != 2 || comps[0].GUID != first.GUID || comps[1].GUID != second.GUID {
		t.Fatalf("泡沫×2 应保留两个 lure 组件，实得 %+v", comps)
	}
	sets := s.baitedHookSets(t.Context(), rod.GUID)
	if len(sets) != 1 || len(sets[0].baits) != 2 {
		t.Fatalf("客户端应收到同一钩上的两枚泡沫: %+v", sets)
	}
}

func TestFoamSingleMountStillReplaces(t *testing.T) {
	t.Parallel()
	s, db := newRigTestServer(t)
	rod, err := s.CreateItem(t.Context(), 1000001, "feeder_10126_FD390", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !s.rigSetType(t.Context(), 1000001, rod.GUID, 34) {
		t.Fatal("rigSetType 失败")
	}
	saveMountTypeForTest(t, s, rod.GUID, 524288) // 泡沫×1
	first, err := s.CreateItem(t.Context(), 1000001, "hm_foamr_80black_hq", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateItem(t.Context(), 1000001, "hm_foamr_80red_hq", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, guid := range []string{first.GUID, second.GUID} {
		r := s.rigSetComponent(t.Context(), 1000001, rod.GUID, 0, guid, 0,
			rigSetOpts{ParentType: familyRod, EnforceCategory: true, SplitStack: true})
		if r.Rejected {
			t.Fatalf("泡沫挂载失败: %+v", r)
		}
	}
	fresh, err := db.GetItemByGUID(t.Context(), rod.GUID)
	if err != nil {
		t.Fatal(err)
	}
	comps := rigTestCategoryComps(t, rigStoredF5(t, fresh), catLure)
	if len(comps) != 1 || comps[0].GUID != second.GUID {
		t.Fatalf("泡沫×1 应替换为第二枚，实得 %+v", comps)
	}
	back, err := db.GetItemByGUID(t.Context(), first.GUID)
	if err != nil || back == nil || back.Container != "backpack" || back.RigGUID != "" {
		t.Fatalf("被替换泡沫未退回背包: %+v err=%v", back, err)
	}
}
