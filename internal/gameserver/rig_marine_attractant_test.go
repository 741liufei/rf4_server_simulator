package gameserver

// 海钓拟饵族双件回归：柳叶亮片（pilker）钓组装完亮片后，再装「基本吸引成分」
// （color_bead_* / fire_tube_*，item_def category=17，与拟饵同一品类）时，
// 客户端要求两件各占一个独立槽位索引。
//
// 事故现场（实机库 data/rf4server.db）：
//   - 杆 90e95cf8 rig_MarinePilker(50) 的 f5 只有 cat=17 extra=300 一条；
//   - 杆 3c0d7f44 rig_MarineGiganticLureJig(53) 的 f5 有两条 cat=17
//     （monojig_2418 + fire_tube_004），extra 都是 300；
//   - 服务端自己打了告警：钓组模板提示：该类型槽位表里没有此品类 rig_type=53 cat=17。
//
// 两条组件落在同一规范索引上时，客户端按索引渲染钓组：第二次装配覆盖第一次，
// 装吸引成分这一步表现为整条钓组变灰、判定未完成，已装上的亮片被退回背包。
//
// 本文件锁定服务端侧的真源行为：真源 f5 必须让亮片与吸引成分落在不同索引，
// 且钓组类型不得因为多了一件 cat=17 而被自动切换。

import (
	"context"
	"testing"
)

// marineAttractantRig 建一根海钓竿并固定的钓组类型，装上前导与亮片。
// 返回杆 GUID、亮片 GUID。
func marineAttractantRig(t *testing.T, rodItemID string, rigType int64) (*Server, context.Context, string, string) {
	t.Helper()
	srv, _ := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, rodItemID, "backpack", 1)
	if err != nil {
		t.Fatalf("造竿失败: %v", err)
	}
	if !srv.rigSetType(ctx, uid, rod.GUID, rigType) {
		t.Fatalf("rigSetType(%d) 失败", rigType)
	}
	leader, err := srv.CreateItem(ctx, uid, "sh_leader_40818", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if r := srv.rigSetComponent(ctx, uid, rod.GUID, catLead, leader.GUID, 175,
		rigSetOpts{ParentType: familyRod, EnforceCategory: true}); r.Rejected {
		t.Fatalf("装前导被拒: %+v", r)
	}
	pilker, err := srv.CreateItem(ctx, uid, "pilkerlures_2630", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	res := srv.rigSetComponent(ctx, uid, rod.GUID, catLure, pilker.GUID, 300,
		rigSetOpts{ParentType: familyRod, EnforceCategory: true})
	if res.Rejected || res.MountedGUID == "" {
		t.Fatalf("装亮片被拒: %+v", res)
	}
	return srv, ctx, rod.GUID, res.MountedGUID
}

// lureIndexByGUID 返回组件在 f5 中的 extra（规范索引）。
func lureIndexByGUID(t *testing.T, srv *Server, ctx context.Context, rodGUID, guid string) int64 {
	t.Helper()
	comps := srv.storedRigComponents(ctx, rodGUID)
	for _, comp := range comps {
		if comp.GUID == guid {
			return comp.Extra
		}
	}
	t.Fatalf("f5 中找不到组件 %s", shortGUID(guid))
	return 0
}

// lureCompCount 统计 f5 中 cat=17 组件数量。
func lureCompCount(t *testing.T, srv *Server, ctx context.Context, rodGUID string) int {
	t.Helper()
	return len(rigCompKeysOfCategory(srv.storedRigComponents(ctx, rodGUID), catLure))
}

// TestMarineRigAttractantGetsOwnLureIndex 锁定根因：
// 海钓拟饵族（50–54）必须为「亮片」和「基本吸引成分」各留一个拟饵族索引。
// 修复前 slotIndicesFor 对 50–54 的 cat=17 返回空，两件组件都被
// canonicalIndex 兜底成 canonicalRigIndex[17]=300，索引冲突。
func TestMarineRigAttractantGetsOwnLureIndex(t *testing.T) {
	for _, rigType := range []int64{50, 51, 52, 53, 54} {
		t.Run(rigNameForType(rigType), func(t *testing.T) {
			if got := len(slotIndicesFor(rigType, catLure)); got < 2 {
				t.Fatalf("钓组 %d 的拟饵族槽位 = %d 个，至少需要 2 个（亮片 + 基本吸引成分）",
					rigType, got)
			}
		})
	}
}

// TestMarineRigAttractantMountKeepsDistinctIndices 是用户复现路径的服务端版本：
// 组装亮片杆 → 依次装入各成分 → 最后装基本吸引成分。
// 断言：亮片仍在 f5、吸引成分独立成槽、两者索引不同、钓组类型未被自动切换。
func TestMarineRigAttractantMountKeepsDistinctIndices(t *testing.T) {
	cases := []struct {
		name     string
		rodItem  string
		rigType  int64
		wantName string
	}{
		{"Pilker", "pilkr_10212_90340", 50, "rig_MarinePilker"},
		{"GiganticLureJig", "boatr_10222_SBH80_100", 53, "rig_MarineGiganticLureJig"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, ctx, rod, pilker := marineAttractantRig(t, tc.rodItem, tc.rigType)
			const uid = int64(1000001)

			bead, err := srv.CreateItem(ctx, uid, "color_bead_009", "backpack", 1)
			if err != nil {
				t.Fatal(err)
			}
			// 客户端对基本吸引成分不带槽位，走「按品类找空闲槽」。
			res := srv.rigSetComponent(ctx, uid, rod, catLure, bead.GUID, 0,
				rigSetOpts{ParentType: familyRod, EnforceCategory: true})
			if res.Rejected || res.MountedGUID == "" {
				t.Fatalf("装基本吸引成分被拒: %+v", res)
			}

			if n := lureCompCount(t, srv, ctx, rod); n != 2 {
				t.Fatalf("f5 拟饵族组件 = %d 个，期望 2 个（亮片 + 吸引成分）", n)
			}
			pilkerIdx := lureIndexByGUID(t, srv, ctx, rod, pilker)
			beadIdx := lureIndexByGUID(t, srv, ctx, rod, res.MountedGUID)
			if pilkerIdx == beadIdx {
				t.Fatalf("亮片索引 %d 与吸引成分索引相同，客户端会把两者叠在同一槽位",
					pilkerIdx)
			}

			row, err := srv.DB.GetItemByGUID(ctx, rod)
			if err != nil || row == nil {
				t.Fatal(err)
			}
			f5 := rigStoredF5(t, row)
			if got := f5.Fields[0].StrOr(); got != tc.wantName {
				t.Fatalf("钓组名被改成 %q，期望保持 %q", got, tc.wantName)
			}
			if got := int64(u64Of(f5.Fields[1].Num)); got != tc.rigType {
				t.Fatalf("钓组类型被改成 %d，期望保持 %d", got, tc.rigType)
			}
		})
	}
}

// TestMarineRigAttractantDeclaredInTemplate 验证模板一致性：
// 装了拟饵族双件的钓组，其模板必须能容纳这个数量，
// 否则 rigTypeFitsInstalledComps 会判定不兼容并触发自动换钓组类型。
func TestMarineRigAttractantDeclaredInTemplate(t *testing.T) {
	srv, ctx, rod, pilker := marineAttractantRig(t, "pilkr_10212_90340", 50)
	const uid = int64(1000001)
	bead, err := srv.CreateItem(ctx, uid, "fire_tube_006", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if res := srv.rigSetComponent(ctx, uid, rod, catLure, bead.GUID, 0,
		rigSetOpts{ParentType: familyRod, EnforceCategory: true}); res.Rejected {
		t.Fatalf("装 fire_tube 被拒: %+v", res)
	}
	_ = pilker

	comps := srv.storedRigComponents(ctx, rod)
	if !rigTypeFitsInstalledComps(50, comps) {
		t.Fatalf("装了两件拟饵族后与钓组 50 不兼容，会被自动换类型")
	}
}

// TestPilkerRigKeepsSingleHookPosition 守住连带风险：
// 「钩位数」决定挂饵分组（多钩才按钩拆组）。铁板竿（50-54）只有一个钩位，
// 吸引成分席位（325/326/327）不是钩位——把它们算进钩位数会让单钩竿
// 被当成多钩，饵料分组碎成多组、判鱼时各选一组。
func TestPilkerRigKeepsSingleHookPosition(t *testing.T) {
	srv, ctx, rod, lure := marineAttractantRig(t, "pilkr_10212_90340", 50)
	const uid = int64(1000001)
	hook := mountHookForBaitTest(t, srv, rod, 0)
	bead, err := srv.CreateItem(ctx, uid, "color_bead_009", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if res := srv.rigSetComponent(ctx, uid, rod, catLure, bead.GUID, 0,
		rigSetOpts{ParentType: familyRod, EnforceCategory: true}); res.Rejected {
		t.Fatalf("装吸引成分被拒: %+v", res)
	}
	if _, bait := baitTestMount(t, srv, rod, "worm_01", 3, 350); bait == "" {
		t.Fatal("装饵失败")
	}

	groups := srv.baitedHookSets(ctx, rod)
	if len(groups) != 1 {
		t.Fatalf("单钩铁板竿的挂饵分组 = %d 组，期望 1 组（一个钩位）", len(groups))
	}
	if groups[0].hookGUID != hook {
		t.Fatalf("分组钩位 = %s，期望 %s", shortGUID(groups[0].hookGUID), shortGUID(hook))
	}
	if hpc := hookPositionsFor(50); hpc != 1 {
		t.Fatalf("钓组 50 钩位数 = %d，期望 1", hpc)
	}
	_ = lure
}

// TestBrokenMarineRigLureCollisionIsRepaired 守住存量脏数据：
// 修复前装上去的杆，f5 里有两条 cat=17 都写着 extra=300（亮片 + 吸引成分
// 撞在同一索引上）——这正是用户截图里「装完吸引成分整组变灰」的落库形态。
// 投影重建必须把多出来的那条退回背包（不是删除、不是静默丢弃），
// 让整组回到「一个索引一件」的合法形态，玩家可以再把它装到 325。
func TestBrokenMarineRigLureCollisionIsRepaired(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "boatr_10222_SBH80_100", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !srv.rigSetType(ctx, uid, rod.GUID, 53) {
		t.Fatal("rigSetType 失败")
	}
	pilker, err := srv.CreateItem(ctx, uid, "monojig_2418", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	bead, err := srv.CreateItem(ctx, uid, "fire_tube_004", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}

	// 直接构造事故形态：两条 cat=17 都落在 extra=300。
	row, err := db.GetItemByGUID(ctx, rod.GUID)
	if err != nil || row == nil {
		t.Fatal(err)
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	idx := fieldIdxByCS(row.WireClass, csRigObject)
	stored := fields[idx].Val
	name, rigType, _ := rigIdentity(stored)
	comps := rigCompsOf(stored)
	comps[rigCompKey(catLure, 0)] = rigComp{Category: catLure, Ordinal: 0, GUID: pilker.GUID, Extra: 300}
	comps[rigCompKey(catLure, 1)] = rigComp{Category: catLure, Ordinal: 1, GUID: bead.GUID, Extra: 300}
	fields[idx].Val = buildRig153Like(stored, name, rigType, comps, 100, 0)
	srv.saveItemFields(ctx, row, fields)
	for _, g := range []string{pilker.GUID, bead.GUID} {
		if err := db.SetItemRigLink(ctx, g, rod.GUID, catLure); err != nil {
			t.Fatal(err)
		}
	}

	if err := srv.reconcileRigProjection(ctx, rod.GUID); err != nil {
		t.Fatalf("投影重建失败: %v", err)
	}

	comps = srv.storedRigComponents(ctx, rod.GUID)
	lureKeys := rigCompKeysOfCategory(comps, catLure)
	if len(lureKeys) != 1 {
		t.Fatalf("修复后拟饵族组件 = %d 个，期望收敛成 1 个", len(lureKeys))
	}
	// 先装的亮片保留，多出来的吸引成分退回背包。
	if comps[lureKeys[0]].GUID != pilker.GUID {
		t.Fatalf("保留的是 %s，期望保留先装的亮片 %s",
			shortGUID(comps[lureKeys[0]].GUID), shortGUID(pilker.GUID))
	}
	back, err := db.GetItemByGUID(ctx, bead.GUID)
	if err != nil || back == nil {
		t.Fatal(err)
	}
	if back.Container != "backpack" || back.RigGUID != "" {
		t.Fatalf("被拆下的吸引成分应在背包：container=%q rig=%q", back.Container, back.RigGUID)
	}
}

// TestMarineRigBothAttractantsGetDistinctIndices 复现用户实机路径（2026-11-08 18:30 日志）：
//
//	柳叶亮片 → 鱼片 → 钓钩吸引成分
//
// 客户端对柳叶亮片发 extra=300、对钓钩吸引成分发 extra=326，
// 说明拟饵族至少有 300/325/326 三个位置：亮片、基本吸引成分、钓钩吸引成分。
// 只要少声明一个，第二次装吸引成分就会退回兜底索引 300，与亮片撞位后整组变灰。
func TestMarineRigBothAttractantsGetDistinctIndices(t *testing.T) {
	for _, rigType := range []int64{50, 51, 52, 53, 54} {
		t.Run(rigNameForType(rigType), func(t *testing.T) {
			if got := len(slotIndicesFor(rigType, catLure)); got < 3 {
				t.Fatalf("钓组 %d 的拟饵族槽位 = %d 个，至少需要 3 个（亮片 + 基本吸引成分 + 钓钩吸引成分）",
					rigType, got)
			}
		})
	}
}

// TestMarineRigTwoAttractantsKeepDistinctIndices 端到端：
// 亮片 → 基本吸引成分 → 钓钩吸引成分，三件必须各占一个索引。
func TestMarineRigTwoAttractantsKeepDistinctIndices(t *testing.T) {
	srv, ctx, rod, pilker := marineAttractantRig(t, "pilkr_10212_90340", 50)
	const uid = int64(1000001)

	mount := func(itemID string) string {
		it, err := srv.CreateItem(ctx, uid, itemID, "backpack", 1)
		if err != nil {
			t.Fatal(err)
		}
		res := srv.rigSetComponent(ctx, uid, rod, catLure, it.GUID, 0,
			rigSetOpts{ParentType: familyRod, EnforceCategory: true})
		if res.Rejected || res.MountedGUID == "" {
			t.Fatalf("装 %s 被拒: %+v", itemID, res)
		}
		return res.MountedGUID
	}

	// 基本吸引成分 → 钓钩吸引成分
	bead := mount("color_bead_009")
	tube := mount("fire_tube_003")

	if n := lureCompCount(t, srv, ctx, rod); n != 3 {
		t.Fatalf("f5 拟饵族组件 = %d 个，期望 3 个（亮片 + 两只吸引成分）", n)
	}
	seen := map[int64]string{}
	for _, comp := range srv.storedRigComponents(ctx, rod) {
		if comp.Category != catLure {
			continue
		}
		if prev, dup := seen[comp.Extra]; dup {
			t.Fatalf("索引 %d 被 %s 与 %s 同时占用", comp.Extra, prev, shortGUID(comp.GUID))
		}
		seen[comp.Extra] = shortGUID(comp.GUID)
	}
	if len(seen) != 3 {
		t.Fatalf("不同的拟饵族索引 = %d 个，期望 3 个", len(seen))
	}
	_ = pilker
	_ = bead
	_ = tube
}

// TestMarineRigAttractantsDoNotDisplaceEachOther 复现 2026-11-08 18:52 实机日志：
//
//	柳叶亮片(300) → 钓钩吸引成分 fire_tube(326) → 基本吸引成分 color_bead(325)
//
// 事故形态：第三步把火管顶掉了（日志里 325 写入用了 ordinal=1，与读回给 326
// 的序号撞上，被当成“同槽换件”）。
// 断言：三件共存、各占一个索引、火管仍挂在杆上且未被退回背包。
func TestMarineRigAttractantsDoNotDisplaceEachOther(t *testing.T) {
	srv, ctx, rod, pilker := marineAttractantRig(t, "pilkr_10212_90340", 50)
	const uid = int64(1000001)

	mountAt := func(itemID string, idx int64) string {
		it, err := srv.CreateItem(ctx, uid, itemID, "backpack", 1)
		if err != nil {
			t.Fatal(err)
		}
		res := srv.rigSetComponent(ctx, uid, rod, catLure, it.GUID, idx,
			rigSetOpts{ParentType: familyRod, EnforceCategory: true})
		if res.Rejected || res.MountedGUID == "" {
			t.Fatalf("装 %s(extra=%d) 被拒: %+v", itemID, idx, res)
		}
		return res.MountedGUID
	}

	tube := mountAt("fire_tube_006", 326)  // 钓钩吸引成分
	bead := mountAt("color_bead_009", 325) // 基本吸引成分

	comps := srv.storedRigComponents(ctx, rod)
	byGUID := map[string]rigComp{}
	for _, c := range comps {
		byGUID[c.GUID] = c
	}
	for name, guid := range map[string]string{"柳叶亮片": pilker, "钓钩吸引成分": tube, "基本吸引成分": bead} {
		c, ok := byGUID[guid]
		if !ok {
			t.Fatalf("%s 被顶掉了，f5 中已不存在", name)
		}
		if c.Category != catLure {
			t.Fatalf("%s 品类 = %d，期望 %d", name, c.Category, catLure)
		}
	}
	// 三件各自的规范索引必须互不相同。
	seen := map[int64]string{}
	for name, guid := range map[string]string{"柳叶亮片": pilker, "钓钩吸引成分": tube, "基本吸引成分": bead} {
		c := byGUID[guid]
		idx := canonicalIndexForOrdinal(c.Category, c.Extra, 50, c.Ordinal)
		if prev, dup := seen[idx]; dup {
			t.Fatalf("索引 %d 被 %s 与 %s 同时占用", idx, prev, name)
		}
		seen[idx] = name
	}
	if len(seen) != 3 {
		t.Fatalf("不同索引 = %d 个，期望 3 个：%v", len(seen), seen)
	}
	// 火管不能退回背包。
	if row, err := srv.DB.GetItemByGUID(ctx, tube); err != nil || row == nil {
		t.Fatal(err)
	} else if row.Container != "rig" {
		t.Fatalf("钓钩吸引成分被退回 %q，应仍在 rig 上", row.Container)
	}
}
