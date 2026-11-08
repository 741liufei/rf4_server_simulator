package gameserver

import (
	"context"
	"math"
	"testing"
)

// TestHookStrengthBreaksHookNotRod 是大鱼吃小钩的正常结果：
// 4 号钩拉力约 3.15kg，主线 10kg 以上，超载时最弱环是钩——
// 17/9 应判"钩被拉直"，摘掉钩，竿和线都不动。
func TestHookStrengthBreaksHookNotRod(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "spin_10001_S60L", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	reel, err := srv.CreateItem(ctx, uid, "spin_5820", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_15249", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	hook, err := srv.CreateItem(ctx, uid, "orig_20008_18", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, reel.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, hook.GUID)

	spec := srv.loadGearSpecForRig(ctx, srv.rigGearIDs(ctx, uid, rod.GUID), rod.GUID)
	if !spec.HookPresent || spec.HookStrengthKg <= 0 {
		t.Fatalf("钩强度没读出来: present=%v kg=%f", spec.HookPresent, spec.HookStrengthKg)
	}
	if spec.HookStrengthKg >= spec.LineStrengthKg || spec.HookStrengthKg >= spec.RodPowerKg {
		t.Fatalf("测试钓组应满足 钩 < 竿/线: hook=%f rod=%f line=%f",
			spec.HookStrengthKg, spec.RodPowerKg, spec.LineStrengthKg)
	}
	if got := gearWeakestPart(spec); got != "hook" {
		t.Fatalf("最弱环=%q，期望 hook", got)
	}

	decision, err := srv.resolveGearBreak(ctx, uid, rod.GUID, hook.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Part != "hook" || decision.GUID != hook.GUID {
		t.Fatalf("超载应判断钩: %+v", decision)
	}

	outcome, err := srv.breakLineOrRod(ctx, uid, rod.GUID, hook.GUID)
	if err != nil {
		t.Fatal(err)
	}
	// 终端件断裂统一按 component 处理（和客户端上报断钩走同一条路），
	// 关键是断的是钩的 GUID，竿和线不动。
	if !outcome.ComponentBroken || outcome.BrokenGUID != hook.GUID {
		t.Fatalf("断件结果不是钩: %+v", outcome)
	}
	if row, _ := db.GetItemByGUID(ctx, hook.GUID); row != nil {
		t.Fatalf("断钩后钩应被销毁（官服口径），实得 %+v", row)
	}
	if row, _ := db.GetItemByGUID(ctx, rod.GUID); row == nil || row.Durability != 1 {
		t.Fatalf("断钩不应伤到竿: %+v", row)
	}
	if row, _ := db.GetItemByGUID(ctx, line.GUID); row == nil || row.Durability != 1 {
		t.Fatalf("断钩不应伤到主线: %+v", row)
	}
	if mounts := rigSlotGUIDs(t, srv, rod.GUID); mounts[catHook] != "" {
		t.Fatalf("断钩后钩位应清空，实得 %q", mounts[catHook])
	}
}

// TestStrongHookLeavesLineAsWeakestLink 换成 33 号钩（拉力 660kg）后，
// 最弱环回到主线，超载仍然断线，不会因为"有钩"就把钩当替罪羊。
func TestStrongHookLeavesLineAsWeakestLink(t *testing.T) {
	srv, _ := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "spin_10001_S60L", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_15249", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	hook, err := srv.CreateItem(ctx, uid, "triple_21070", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, hook.GUID)

	spec := srv.loadGearSpecForRig(ctx, srv.rigGearIDs(ctx, uid, rod.GUID), rod.GUID)
	if spec.HookStrengthKg <= spec.LineStrengthKg {
		t.Fatalf("33 号钩应强于主线: hook=%f line=%f", spec.HookStrengthKg, spec.LineStrengthKg)
	}
	if got := gearWeakestPart(spec); got == "hook" {
		t.Fatalf("强钩不应成为最弱环: %q", got)
	}
	if limit := gearBreakLimitKg(spec); math.Abs(limit-spec.LineStrengthKg) > 1e-9 &&
		math.Abs(limit-spec.RodPowerKg) > 1e-9 {
		t.Fatalf("最弱环应落在竿或线: limit=%f rod=%f line=%f", limit, spec.RodPowerKg, spec.LineStrengthKg)
	}
}
