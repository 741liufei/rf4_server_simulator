package gameserver

import (
	"context"
	"encoding/json"
	"testing"
)

// 断钩/断竿/断轮后的残留清理回归：
//   - 钩断时挂在钩上的饵必须一起离组；
//   - 自动补饵请求和饵-钩映射不能留在已清空的钓组上；
//   - 拆分子饵离组时并回来源堆，不能在背包里变成独立一堆。

func seedBaitHookResidue(t *testing.T, srv *Server, rodGUID, baitGUID, hookGUID string) {
	t.Helper()
	ctx := context.Background()
	if err := srv.DB.UpsertRodSetting(ctx, rodGUID, "bait_hook:"+baitGUID, hookGUID); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(rebaitRequest{ItemID: "worm_01", HookGUID: hookGUID})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.DB.UpsertRodSetting(ctx, rodGUID, "rebait:0", string(encoded)); err != nil {
		t.Fatal(err)
	}
}

func rodSettingCount(t *testing.T, srv *Server, rodGUID, pattern string) int64 {
	t.Helper()
	var n int64
	if err := srv.DB.SQL.QueryRow(
		`SELECT COUNT(*) FROM rod_setting WHERE guid=? AND key LIKE ?`, rodGUID, pattern).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func stackQtyForTest(t *testing.T, srv *Server, guid string) int64 {
	t.Helper()
	row, err := srv.DB.GetItemByGUID(context.Background(), guid)
	if err != nil || row == nil {
		t.Fatalf("读取背包堆失败: %v", err)
	}
	return srv.stackCountOf(context.Background(), row)
}

// TestHookBreakDetachesBaitAndRefundsStack 锁定 P1-2：钩断后挂在钩上的饵
// 不能再留在钓组上，拆分出的子饵要并回来源堆，并清掉补饵记录。
func TestHookBreakDetachesBaitAndRefundsStack(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "tele_10180_4", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_15095", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	hook, err := srv.CreateItem(ctx, uid, "clas_20163", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	bait, err := srv.CreateItem(ctx, uid, "worm_01", "backpack", 3)
	if err != nil {
		t.Fatal(err)
	}
	srv.setStackCount(ctx, bait, 3)
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, hook.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, bait.GUID)

	comps := rigSlotGUIDs(t, srv, rod.GUID)
	mountedBait := comps[catBait]
	if mountedBait == "" || mountedBait == bait.GUID {
		t.Fatalf("挂饵应拆出独立子实例，实得 %s", mountedBait)
	}
	seedBaitHookResidue(t, srv, rod.GUID, mountedBait, hook.GUID)

	outcome, err := srv.breakLineOrRod(ctx, uid, rod.GUID, hook.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.ComponentBroken || outcome.BrokenGUID != hook.GUID {
		t.Fatalf("应判钩断: %+v", outcome)
	}
	if row, _ := db.GetItemByGUID(ctx, mountedBait); row != nil {
		t.Fatalf("断钩后挂载子饵应删除（并回来源堆），实得 %+v", row)
	}
	if got := stackQtyForTest(t, srv, bait.GUID); got != 3 {
		t.Fatalf("来源饵堆应恢复到 3，实得 %d", got)
	}
	if !containsGUID(outcome.StackRefreshGUIDs, bait.GUID) {
		t.Fatalf("结果应刷新来源饵堆 %s: %+v", bait.GUID, outcome.StackRefreshGUIDs)
	}
	if got := rodSettingCount(t, srv, rod.GUID, "bait_hook:%"); got != 0 {
		t.Fatalf("断钩后不应残留饵-钩映射，实得 %d 条", got)
	}
	if got := rodSettingCount(t, srv, rod.GUID, "rebait:%"); got != 0 {
		t.Fatalf("断钩后不应残留补饵请求，实得 %d 条", got)
	}
	if mounts := rigSlotGUIDs(t, srv, rod.GUID); mounts[catBait] != "" {
		t.Fatalf("断钩后饵位应清空，实得 %q", mounts[catBait])
	}
}

// TestRodBreakClearsBaitSettingsAndRefundsStack 锁定 P1-3：断竿清空钓组后
// 不能再留下 rebait/bait_hook；拆分子饵并回来源堆。
func TestRodBreakClearsBaitSettingsAndRefundsStack(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "tele_10180_4", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_15095", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	hook, err := srv.CreateItem(ctx, uid, "clas_20163", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	bait, err := srv.CreateItem(ctx, uid, "worm_01", "backpack", 3)
	if err != nil {
		t.Fatal(err)
	}
	srv.setStackCount(ctx, bait, 3)
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, hook.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, bait.GUID)
	if err := db.UpsertRodSetting(ctx, rod.GUID, reelDragSettingKey, "29"); err != nil {
		t.Fatal(err)
	}

	comps := rigSlotGUIDs(t, srv, rod.GUID)
	mountedBait := comps[catBait]
	seedBaitHookResidue(t, srv, rod.GUID, mountedBait, hook.GUID)

	outcome, err := srv.breakLineOrRod(ctx, uid, rod.GUID, rod.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.BreakPart != "rod" {
		t.Fatalf("应判断竿: %+v", outcome)
	}
	if row, _ := db.GetItemByGUID(ctx, mountedBait); row != nil {
		t.Fatalf("断竿后挂载子饵应删除（并回来源堆），实得 %+v", row)
	}
	if got := stackQtyForTest(t, srv, bait.GUID); got != 3 {
		t.Fatalf("来源饵堆应恢复到 3，实得 %d", got)
	}
	if !containsGUID(outcome.StackRefreshGUIDs, bait.GUID) {
		t.Fatalf("结果应刷新来源饵堆 %s: %+v", bait.GUID, outcome.StackRefreshGUIDs)
	}
	if got := rodSettingCount(t, srv, rod.GUID, "bait_hook:%"); got != 0 {
		t.Fatalf("断竿后不应残留饵-钩映射，实得 %d 条", got)
	}
	if got := rodSettingCount(t, srv, rod.GUID, "rebait:%"); got != 0 {
		t.Fatalf("断竿后不应残留补饵请求，实得 %d 条", got)
	}
	if got, err := db.GetRodSetting(ctx, rod.GUID, reelDragSettingKey); err != nil || got != "29" {
		t.Fatalf("断竿不应清掉摩擦档位: %q %v", got, err)
	}
}

// TestReelBreakClearsBaitSettingsAndRefundsStack 锁定 P1-3：断轮同样要清
// rebait/bait_hook，并把拆分子饵并回来源堆。
func TestReelBreakClearsBaitSettingsAndRefundsStack(t *testing.T) {
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
	bait, err := srv.CreateItem(ctx, uid, "worm_01", "backpack", 3)
	if err != nil {
		t.Fatal(err)
	}
	srv.setStackCount(ctx, bait, 3)
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, reel.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, hook.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, bait.GUID)

	comps := rigSlotGUIDs(t, srv, rod.GUID)
	mountedBait := comps[catBait]
	seedBaitHookResidue(t, srv, rod.GUID, mountedBait, hook.GUID)

	outcome, err := srv.breakLineOrRod(ctx, uid, rod.GUID, reel.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.ReelBroken || outcome.BreakPart != "reel" {
		t.Fatalf("应判断轮: %+v", outcome)
	}
	if row, _ := db.GetItemByGUID(ctx, reel.GUID); row == nil || row.Durability != 0 || row.RigGUID != rod.GUID {
		t.Fatalf("断轮后轮子应留竿上且耐久为 0: %+v", row)
	}
	if row, _ := db.GetItemByGUID(ctx, mountedBait); row != nil {
		t.Fatalf("断轮后挂载子饵应删除（并回来源堆），实得 %+v", row)
	}
	if got := stackQtyForTest(t, srv, bait.GUID); got != 3 {
		t.Fatalf("来源饵堆应恢复到 3，实得 %d", got)
	}
	if !containsGUID(outcome.StackRefreshGUIDs, bait.GUID) {
		t.Fatalf("结果应刷新来源饵堆 %s: %+v", bait.GUID, outcome.StackRefreshGUIDs)
	}
	if got := rodSettingCount(t, srv, rod.GUID, "bait_hook:%"); got != 0 {
		t.Fatalf("断轮后不应残留饵-钩映射，实得 %d 条", got)
	}
	if got := rodSettingCount(t, srv, rod.GUID, "rebait:%"); got != 0 {
		t.Fatalf("断轮后不应残留补饵请求，实得 %d 条", got)
	}
}

// TestBaitBreakClearsItsBaitSettings 锁定 P1-2 的另一半：饵自身断裂时，
// bait_hook 与 rebait 不能继续把这枚饵补回同一钩。
func TestBaitBreakClearsItsBaitSettings(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "tele_10180_4", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_15095", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	hook, err := srv.CreateItem(ctx, uid, "clas_20163", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	bait, err := srv.CreateItem(ctx, uid, "worm_01", "backpack", 3)
	if err != nil {
		t.Fatal(err)
	}
	srv.setStackCount(ctx, bait, 3)
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, hook.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, bait.GUID)

	comps := rigSlotGUIDs(t, srv, rod.GUID)
	mountedBait := comps[catBait]
	seedBaitHookResidue(t, srv, rod.GUID, mountedBait, hook.GUID)

	outcome, err := srv.breakLineOrRod(ctx, uid, rod.GUID, mountedBait)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.ComponentBroken || outcome.BrokenGUID != mountedBait {
		t.Fatalf("应判饵断: %+v", outcome)
	}
	if row, _ := db.GetItemByGUID(ctx, mountedBait); row == nil || row.Durability != 0 {
		t.Fatalf("断饵应保留为耐久 0 的背包物品: %+v", row)
	}
	if got := rodSettingCount(t, srv, rod.GUID, "bait_hook:%"); got != 0 {
		t.Fatalf("断饵后不应残留饵-钩映射，实得 %d 条", got)
	}
	if got := rodSettingCount(t, srv, rod.GUID, "rebait:%"); got != 0 {
		t.Fatalf("断饵后不应残留补饵请求，实得 %d 条", got)
	}
}
