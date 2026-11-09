package gameserver

import (
	"context"
	"math"
	"testing"

	"rf4server/internal/store"
)

func TestHookFamilyForRealModelIDs(t *testing.T) {
	tests := map[string]string{
		"triple_20686":  "treble",
		"jhead_20001":   "jighead",
		"offset_20007":  "offset",
		"woffset_20001": "offset",
		"mhook_20870":   "marine",
		"mdhook_20932":  "marine",
		"gmhook_21205":  "marine",
		"c_hook_20712":  "carp",
		"clas_20042":    "classic",
		"orig_20008_1":  "classic",
		"hook_20001":    "other",
	}
	for itemID, want := range tests {
		if got := hookFamilyFor(itemID); got != want {
			t.Fatalf("hookFamilyFor(%q)=%q，期望 %q", itemID, got, want)
		}
	}
}

func TestSyncHookBiteProfilesUsesRealStaticData(t *testing.T) {
	s, db := newStaticRigTestServer(t)
	ctx := context.Background()
	defs, err := db.ListItemDefsByCategory(ctx, catHook)
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.SyncHookBiteProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(defs) || n < 1000 {
		t.Fatalf("同步型号数=%d，静态定义=%d", n, len(defs))
	}

	p, ok := s.hookBiteProfileFor("triple_20686")
	if !ok {
		t.Fatal("真实型号 triple_20686 未进入运行时缓存")
	}
	if p.Size != 25 || math.Abs(p.LengthMM-18) > 1e-6 ||
		math.Abs(p.StrengthKg-98) > 1e-6 || p.Quality != 100 ||
		p.Family != "treble" || p.Brand != "brsrk_ultra_tr_f" {
		t.Fatalf("真实型号物理字段错误: %+v", p)
	}
	if p.SolidScale != 1 || p.HoldScale != 1 ||
		p.SmallFishFloor != 0 || p.OverCapacityHoldFloor != 0 {
		t.Fatalf("默认调参值不应改变现有手感: %+v", p)
	}
}

func TestApplyHookBiteProfileDefaultsAndOverrides(t *testing.T) {
	spec := gearPhysicsSpec{
		HookPresent: true, HookItemID: "triple_test",
		HookSize: 28, HookLengthMM: 24,
	}
	baseSolid, baseHold := hookSolidHold(100, spec)
	gotSolid, gotHold := applyHookBiteProfile(100, spec, store.HookBiteProfile{
		SolidScale: 1, HoldScale: 1,
	}, baseSolid, baseHold)
	if gotSolid != baseSolid || gotHold != baseHold {
		t.Fatalf("默认倍率改变了旧手感: base=(%f,%f) got=(%f,%f)",
			baseSolid, baseHold, gotSolid, gotHold)
	}

	gotSolid, _ = applyHookBiteProfile(100, spec, store.HookBiteProfile{
		SolidScale: 0.1, SmallFishFloor: 0.42,
	}, baseSolid, baseHold)
	if math.Abs(gotSolid-0.42) > 1e-9 {
		t.Fatalf("小鱼咬口下限未生效: %f", gotSolid)
	}

	overSolid, overHold := hookSolidHold(200_000, spec)
	_, gotHold = applyHookBiteProfile(200_000, spec, store.HookBiteProfile{
		HoldScale: 0.5, OverCapacityHoldFloor: 0.65,
	}, overSolid, overHold)
	if math.Abs(gotHold-0.65) > 1e-9 {
		t.Fatalf("超载保持下限未生效: %f", gotHold)
	}

	gotSolid, gotHold = applyHookBiteProfile(100, spec, store.HookBiteProfile{
		SolidScale: 3, HoldScale: 3,
	}, 0.96, 0.98)
	if gotSolid != 1 || gotHold != 1 {
		t.Fatalf("概率必须限幅到 1: (%f,%f)", gotSolid, gotHold)
	}
	gotSolid, gotHold = applyHookBiteProfile(100, spec, store.HookBiteProfile{
		SolidScale: 0, HoldScale: 0,
	}, 0.96, 0.98)
	if gotSolid != 0 || gotHold != 0 {
		t.Fatalf("后台设置为 0 必须真正关闭概率: (%f,%f)", gotSolid, gotHold)
	}
}

func TestMountedHookCarriesItemIDIntoPhysicsSpec(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	rod, err := s.CreateItem(ctx, uid, "spin_10001_S60L", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	hook, err := s.CreateItem(ctx, uid, "triple_20686", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(s)
	mountGearForTest(t, s, sess, rod.GUID, hook.GUID)

	spec := s.loadGearSpecForRig(ctx, s.rigGearIDs(ctx, uid, rod.GUID), rod.GUID)
	if !spec.HookPresent || spec.HookItemID != "triple_20686" {
		t.Fatalf("装配钩型号未进入物理裁决: %+v", spec)
	}
}
