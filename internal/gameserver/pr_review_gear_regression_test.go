package gameserver

// 断件链/渔轮模型 PR #185 的复审回归：
//   - 摩擦片磨穿状态、csDragForce 持久化与存量零值迁移；
//   - 铅芯前导可上报、钩交由最弱环裁决、下游丢失集合按断件位置计算。
//
// 用例使用仓库自带的真实 item_def（lc_40246 / c_hook_20748 等），
// 不用伪造定义，以免把"数据覆盖不全"误判成"逻辑正确"。
// 抛投校准部分的回归在 #188 的 pr_review_cast_regression_test.go，
// 两个文件分开命名，避免两个 PR 合并时互相覆盖。

import (
	"context"
	"math"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

// frictionDragFromFields 从物品树（落库 fields 或 ItemToTree 下发的树）里读出
// 当前摩擦片的制动力，字段缺失直接失败。
func frictionDragFromFields(t *testing.T, srv *Server, ctx context.Context, fields []proto.Field) float64 {
	t.Helper()
	child, _, _, ok := srv.selectedReelPart(ctx, fields, 9)
	if !ok || child == nil {
		t.Fatal("轮子缺少摩擦片(cat9)子实例")
	}
	v, present, _ := frictionWasherDrag(child)
	if !present {
		t.Fatal("摩擦片制动力字段缺失")
	}
	return v
}

func storedFieldsOf(t *testing.T, srv *Server, ctx context.Context, guid string) []proto.Field {
	t.Helper()
	row, err := srv.DB.GetItemByGUID(ctx, guid)
	if err != nil || row == nil {
		t.Fatalf("读取物品 %s 失败: %v", guid, err)
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatalf("解析 %s 字段失败: %v", guid, err)
	}
	return fields
}

// slipUntilWorn 连续泄力打滑直到摩擦片磨穿，返回打滑次数。
func slipUntilWorn(t *testing.T, srv *Server, ctx context.Context, uid int64, rodGUID, reelGUID string) int {
	t.Helper()
	seedFrictionWasherNearWorn(t, srv, ctx, reelGUID)
	for i := 1; i <= 2; i++ {
		out, err := srv.damageReelSlip(ctx, uid, rodGUID, reelGUID)
		if err != nil {
			t.Fatalf("第 %d 次打滑失败: %v", i, err)
		}
		if out.FrictionWorn {
			return i
		}
	}
	t.Fatal("预置两次磨损后摩擦片仍未磨穿")
	return 0
}

func newSlipRig(t *testing.T, srv *Server, ctx context.Context, uid int64) (rodGUID, reelGUID string) {
	t.Helper()
	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatalf("造竿失败: %v", err)
	}
	reel, err := srv.CreateItem(ctx, uid, "bclp_5004", "backpack", 1)
	if err != nil {
		t.Fatalf("造轮失败: %v", err)
	}
	mountReelOnRod(t, srv, rod.GUID, reel.GUID)
	return rod.GUID, reel.GUID
}

// seedFrictionWasherNearWorn 把摩擦片预置到只差两次打滑就磨穿，
// 避免每个用例真实循环约 50 次写库。后续仍走 damageReelSlip 的真实磨损链。
func seedFrictionWasherNearWorn(t *testing.T, srv *Server, ctx context.Context, reelGUID string) {
	t.Helper()
	row, err := srv.DB.GetItemByGUID(ctx, reelGUID)
	if err != nil || row == nil {
		t.Fatalf("读取轮子失败: %+v %v", row, err)
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	washer, def, _, ok := srv.selectedReelPart(ctx, fields, 9)
	if !ok || washer == nil || def == nil {
		t.Fatal("轮子缺少摩擦片子实例")
	}
	maxDrag := itemDefF32(def, gearAttrDragMax) * row.Workmanship
	if maxDrag <= 0 {
		t.Fatal("摩擦片额定制动力无效")
	}
	step := maxDrag * reelSlipWear
	setWireFloat(washer.Fields, washer.Class, fieldIdxByCS(washer.Class, csDragForce), step*1.5)
	row.FieldsJSON, err = FieldsToJSON(fields)
	if err != nil {
		t.Fatal(err)
	}
	srv.canonicalizeReelStrength(ctx, row, fields)
	row.FieldsJSON, err = FieldsToJSON(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.DB.UpdateItemFields(ctx, row.GUID, row.FieldsJSON, row.Qty, row.Durability, row.Workmanship, row.RigGUID); err != nil {
		t.Fatal(err)
	}
}

// P1：磨穿后再打滑一次，制动力不能从 0 复活到额定值。
func TestFrictionWasherWornStateSurvivesSlip(t *testing.T) {
	t.Parallel()
	srv, db := newStaticRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	rodGUID, reelGUID := newSlipRig(t, srv, ctx, uid)

	initial, err := srv.syncReelPerformance(ctx, mustItem(t, db, reelGUID))
	if err != nil || initial.BaseDragKg <= 0 {
		t.Fatalf("新轮应有制动力: %+v err=%v", initial, err)
	}
	slipUntilWorn(t, srv, ctx, uid, rodGUID, reelGUID)

	// 关键回归点：磨穿后再打滑，旧实现会 cur<=0 → 回退 maxDrag 重新满血。
	out, err := srv.damageReelSlip(ctx, uid, rodGUID, reelGUID)
	if err != nil {
		t.Fatalf("磨穿后再次打滑失败: %v", err)
	}
	if !out.FrictionWorn {
		t.Fatal("摩擦片已磨穿，后续打滑必须仍报 FrictionWorn")
	}
	if got := frictionDragFromFields(t, srv, ctx, storedFieldsOf(t, srv, ctx, reelGUID)); got != 0 {
		t.Fatalf("磨穿后制动力被复活: %.4fkg（期望 0）", got)
	}
	perf, err := srv.syncReelPerformance(ctx, mustItem(t, db, reelGUID))
	if err != nil || perf.BaseDragKg > 1e-9 || perf.LockedDragKg > 1e-9 {
		t.Fatalf("磨穿后泄力应仍失效: %+v err=%v", perf, err)
	}
}

// P1：单次磨损必须穿过 ItemToTree 下发，并且经启动修复/重启不回满值。
func TestFrictionWearSurvivesItemTreeAndRestart(t *testing.T) {
	t.Parallel()
	srv, db := newStaticRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	rodGUID, reelGUID := newSlipRig(t, srv, ctx, uid)
	if _, err := srv.damageReelSlip(ctx, uid, rodGUID, reelGUID); err != nil {
		t.Fatalf("单次打滑失败: %v", err)
	}

	row := mustItem(t, db, reelGUID)
	fields := storedFieldsOf(t, srv, ctx, reelGUID)
	child, def, _, ok := srv.selectedReelPart(ctx, fields, 9)
	if !ok || child == nil || def == nil {
		t.Fatal("轮子缺少摩擦片子实例或定义")
	}
	rated := itemDefF32(def, gearAttrDragMax) * row.Workmanship
	worn := frictionDragFromFields(t, srv, ctx, fields)
	if worn <= 0 || !(worn < rated) {
		t.Fatalf("单次打滑应让制动力略低于额定值: worn=%.4f rated=%.4f", worn, rated)
	}

	// ItemToTree 下发给客户端的值必须就是落库磨损值，不能重算成定义值。
	tree, err := srv.ItemToTree(ctx, row)
	if err != nil {
		t.Fatalf("ItemToTree 失败: %v", err)
	}
	if got := frictionDragFromFields(t, srv, ctx, tree.Fields); math.Abs(got-worn) > 1e-6 {
		t.Fatalf("物品树把磨损刷回定义值: 下发=%.4f 落库=%.4f 额定=%.4f", got, worn, rated)
	}

	// 模拟服务重启：启动修复会把 ItemToTree 的结果写回数据库。
	if err := srv.RepairItemPhysics(ctx); err != nil {
		t.Fatalf("RepairItemPhysics 失败: %v", err)
	}
	if got := frictionDragFromFields(t, srv, ctx, storedFieldsOf(t, srv, ctx, reelGUID)); math.Abs(got-worn) > 1e-6 {
		t.Fatalf("启动修复把磨损写回满值: 现在=%.4f 打滑后=%.4f 额定=%.4f", got, worn, rated)
	}
	perf, err := srv.syncReelPerformance(ctx, mustItem(t, db, reelGUID))
	if err != nil || math.Abs(perf.BaseDragKg-worn) > 1e-6 {
		t.Fatalf("重启后物理表制动力应等于磨损值: %+v 期望 %.4f err=%v", perf, worn, err)
	}
}

// P1：轮子零件槽重建（启动修复/工坊兼容修复）不能把磨损刷回满值。
func TestFrictionWearSurvivesReelComponentRepair(t *testing.T) {
	t.Parallel()
	srv, _ := newStaticRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	rodGUID, reelGUID := newSlipRig(t, srv, ctx, uid)
	if _, err := srv.damageReelSlip(ctx, uid, rodGUID, reelGUID); err != nil {
		t.Fatalf("单次打滑失败: %v", err)
	}
	worn := frictionDragFromFields(t, srv, ctx, storedFieldsOf(t, srv, ctx, reelGUID))
	if worn <= 0 {
		t.Fatalf("打滑后制动力异常: %f", worn)
	}
	if _, err := srv.RepairReelComponents(ctx); err != nil {
		t.Fatalf("重建轮子零件失败: %v", err)
	}
	if got := frictionDragFromFields(t, srv, ctx, storedFieldsOf(t, srv, ctx, reelGUID)); math.Abs(got-worn) > 1e-6 {
		t.Fatalf("重建轮子零件把摩擦片磨损重置了: 现在=%.4f 打滑后=%.4f", got, worn)
	}
}

// P1：存量零值只迁移一次；迁移后新语义里的 0 是权威的磨穿状态。
func TestLegacyZeroDragForceMigrationRunsOnce(t *testing.T) {
	t.Parallel()
	srv, db := newStaticRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	rodGUID, reelGUID := newSlipRig(t, srv, ctx, uid)

	// 构造升级前的旧档：字段存在但值为 0（旧实现把它当"未初始化"）。
	row := mustItem(t, db, reelGUID)
	fields := storedFieldsOf(t, srv, ctx, reelGUID)
	child, def, _, ok := srv.selectedReelPart(ctx, fields, 9)
	if !ok || child == nil || def == nil {
		t.Fatal("轮子缺少摩擦片子实例或定义")
	}
	idx := fieldIdxByCS(child.Class, csDragForce)
	if idx < 0 || idx >= len(child.Fields) {
		t.Fatal("摩擦片缺少制动力字段")
	}
	setWireFloat(child.Fields, child.Class, idx, 0)
	fj, err := FieldsToJSON(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateItemFields(ctx, row.GUID, fj, row.Qty, row.Durability, row.Workmanship, row.RigGUID); err != nil {
		t.Fatal(err)
	}

	// 第一次启动：迁移把存量零值补回额定值。
	if err := srv.RepairItemPhysics(ctx); err != nil {
		t.Fatalf("首次启动修复失败: %v", err)
	}
	row = mustItem(t, db, reelGUID)
	rated := itemDefF32(def, gearAttrDragMax) * row.Workmanship
	if got := frictionDragFromFields(t, srv, ctx, storedFieldsOf(t, srv, ctx, reelGUID)); math.Abs(got-rated) > 1e-6 {
		t.Fatalf("存量零值未迁移到额定值: %.4f 期望 %.4f", got, rated)
	}

	// 磨穿后第二次启动：迁移不能重复执行，0 必须保留。
	slipUntilWorn(t, srv, ctx, uid, rodGUID, reelGUID)
	if err := srv.RepairItemPhysics(ctx); err != nil {
		t.Fatalf("二次启动修复失败: %v", err)
	}
	if got := frictionDragFromFields(t, srv, ctx, storedFieldsOf(t, srv, ctx, reelGUID)); got != 0 {
		t.Fatalf("重复迁移把磨穿的摩擦片复活了: %.4f", got)
	}
}

// P1：真实 lc_40246 铅芯前导装配后，上报 17/9 不能被"断件类型不支持"拒绝。
func TestLeadcoreBreakReportAcceptedOnRealRig(t *testing.T) {
	t.Parallel()
	srv, _ := newStaticRigTestServer(t)
	ctx := context.Background()
	sess := newProbeSess(srv)
	uid := sess.Userid

	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_40853", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	leadcore, err := srv.CreateItem(ctx, uid, "lc_40246", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, leadcore.GUID)

	report, err := srv.validateBreakReport(ctx, uid, rod.GUID, leadcore.GUID)
	if err != nil {
		t.Fatalf("铅芯前导上报被拒绝: %v", err)
	}
	if report.terminalCategory != 0 {
		t.Fatalf("铅芯前导是承力件，不应走终端件直接采信: %+v", report)
	}

	// 端到端走一次 17/9，确认协议层不回 nativeFailure。
	tree := rigRequestTree(t, proto.Guid(rod.GUID), proto.Guid(leadcore.GUID), proto.U32(1), proto.Null())
	body, ok := hDynamicRodBreak(&Ctx{Sess: sess, Main: 17, Sub: 9, Tree: tree,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard()})
	if !ok || len(body) == 0 {
		t.Fatal("17/9 拒绝了真实装配的铅芯前导")
	}
	if _, _, derr := proto.DecodeValue(body, 0, srv.Table()); derr != nil {
		t.Fatalf("17/9 响应无法解码: %v", derr)
	}
}

// 客户端上报钩断时，按上报件摘钩；即使钩强于主线，也不改判成断线。
func TestReportedHookBreakDetachesHookEvenIfLineWeaker(t *testing.T) {
	t.Parallel()
	srv, db := newStaticRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "tele_10180_4", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	// mono_15095 线 5.0kg，c_hook_20748 钩 48.72kg：钩远强于线。
	line, err := srv.CreateItem(ctx, uid, "mono_15095", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	hook, err := srv.CreateItem(ctx, uid, "c_hook_20748", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, hook.GUID)

	before := lineLengthOfTestItem(t, srv, line.GUID)
	out, err := srv.breakLineOrRod(ctx, uid, rod.GUID, hook.GUID)
	if err != nil {
		t.Fatalf("上报钩断应被受理: %v", err)
	}
	if !out.ComponentBroken || out.BreakPart != "component" || out.BrokenGUID != hook.GUID {
		t.Fatalf("应按上报件摘钩: %+v", out)
	}
	if out.LineBroken {
		t.Fatalf("上报钩断不应改判成断线: %+v", out)
	}
	if after := lineLengthOfTestItem(t, srv, line.GUID); after != before {
		t.Fatalf("摘钩不应扣主线长度: %d -> %d", before, after)
	}
	logs, err := db.ListItemDamageLogs(ctx, uid, 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range logs {
		if entry.ItemGUID == hook.GUID && entry.Event == damageEventComponentBreak {
			found = true
		}
	}
	if !found {
		t.Fatal("上报钩断应记录 component_break")
	}
}

// P2：按断件位置取下游集合，子线/连接件断裂不能删掉上游铅芯。
func TestDownstreamLossKeepsUpstreamLeadcore(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		rigType  int64
		broken   int64
		wantLead bool
		wantHook bool
	}{
		{"鲤族钓组断子线", 24, catLead, false, true},
		{"鲤族钓组断连接件", 24, catLeaderLock, false, true},
		{"鲤族钓组断铅芯自身", 24, gearCatLeadcore, true, true},
		{"路亚钓组断子线", 5, catLead, false, true},
		{"未知钓组类型", 0, catLead, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lost := downstreamLossCategories(tc.rigType, tc.broken)
			if lost[gearCatLeadcore] != tc.wantLead {
				t.Fatalf("铅芯丢失判定错误: lost=%v", lost)
			}
			if lost[catHook] != tc.wantHook {
				t.Fatalf("下游钩应随断件丢失: lost=%v", lost)
			}
			if !lost[tc.broken] {
				t.Fatalf("断件自身必须进丢失集合: lost=%v", lost)
			}
		})
	}
}

// P2 集成：真实装配铅芯 + 子线，断子线后铅芯仍留在主线上。
func TestLeaderBreakKeepsMountedLeadcore(t *testing.T) {
	t.Parallel()
	srv, db := newStaticRigTestServer(t)
	ctx := context.Background()
	sess := newProbeSess(srv)
	uid := sess.Userid

	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_40853", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	leader, err := srv.CreateItem(ctx, uid, "fc_leader_40048", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	leadcore, err := srv.CreateItem(ctx, uid, "lc_40246", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, guid := range []string{line.GUID, leader.GUID, leadcore.GUID} {
		mountGearForTest(t, srv, sess, rod.GUID, guid)
	}
	if _, err := srv.breakLineTx(ctx, uid, rod.GUID, leader.GUID, catLead); err != nil {
		t.Fatalf("断子线失败: %v", err)
	}
	if row, _ := db.GetItemByGUID(ctx, leadcore.GUID); row == nil {
		t.Fatal("断子线误删了上游仍在主线上的铅芯前导")
	}
	if row, _ := db.GetItemByGUID(ctx, leader.GUID); row != nil {
		t.Fatal("断件自身应被移除")
	}
	if got := rigSlotGUIDs(t, srv, rod.GUID)[gearCatLeadcore]; got != leadcore.GUID {
		t.Fatalf("竿上铅芯槽位应保留: %q 期望 %q", got, leadcore.GUID)
	}
}
