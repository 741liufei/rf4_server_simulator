package gameserver

import (
	"math"
	"testing"
	"time"

	"rf4server/internal/proto"
)

func TestFishHuntStageWeights(t *testing.T) {
	tests := []struct {
		stage          int
		w1, w2, w3, w4 float64
	}{
		{fishHuntDaily, 74.7, 24, 1, .3},
		{fishHuntGather, 54.5, 43, 2, .5},
		{fishHuntActive, 30, 61.2, 6.8, 2},
		{fishHuntGiant, 10, 64, 20, 6},
	}
	for _, tc := range tests {
		w1, w2, w3, w4, force := fishHuntWeights(tc.stage, &fishHuntState{}, 0, .5, false)
		if force || math.Abs(w1-tc.w1) > 1e-9 || math.Abs(w2-tc.w2) > 1e-9 ||
			math.Abs(w3-tc.w3) > 1e-9 || math.Abs(w4-tc.w4) > 1e-9 {
			t.Fatalf("stage=%d weights=(%.1f,%.1f,%.1f,%.1f) force=%v", tc.stage, w1, w2, w3, w4, force)
		}
	}
}

func TestFishHuntPityForcesTrophy(t *testing.T) {
	_, _, _, _, force := fishHuntWeights(fishHuntActive, &fishHuntState{Pity: 60}, 0, .5, false)
	if !force {
		t.Fatal("60 尾未出奖杯时必须触发奖杯硬保底")
	}
	_, _, w3, _, force := fishHuntWeights(fishHuntActive, &fishHuntState{Pity: 30}, 0, .5, false)
	if force || w3 <= 6.8 {
		t.Fatalf("30 尾软保底应提高奖杯权重，w3=%.2f force=%v", w3, force)
	}
}

func TestFishHuntBlueProbabilityIsHookIndependent(t *testing.T) {
	_, _, _, smallBlue, _ := fishHuntWeights(fishHuntActive, &fishHuntState{}, 0, .1, false)
	_, _, _, largeBlue, _ := fishHuntWeights(fishHuntActive, &fishHuntState{}, 0, .9, false)
	if math.Abs(smallBlue-largeBlue) > 1e-9 {
		t.Fatalf("蓝冠概率不能受钩型影响: small=%.3f large=%.3f", smallBlue, largeBlue)
	}
}

func TestLuckProtectedBlueBypassesHookQuality(t *testing.T) {
	tiny := gearPhysicsSpec{HookPresent: true, HookSize: 0, HookLengthMM: 1.8}
	f := &RolledFish{Grade: 4, WeightG: 500_000}
	applyHookBiteQuality(f, tiny)
	if !f.LuckProtected || f.BiteQuality != "solid" || f.HookHoldP != 1 {
		t.Fatalf("蓝冠天命鱼必须绕过钩型判定: %+v", f)
	}
}

func TestHookBiteStatePersistsWithPendingFish(t *testing.T) {
	want := &RolledFish{
		FishGUID: "fish-nibble", BiteQuality: "nibble", HookHoldP: .42, LuckProtected: false,
	}
	got := rolledFishFromStore(rolledFishToStore(want))
	if got == nil || got.BiteQuality != want.BiteQuality || got.HookHoldP != want.HookHoldP ||
		got.LuckProtected != want.LuckProtected {
		t.Fatalf("钩型咬口状态未持久化: %+v", got)
	}
}

func TestLuckProtectedBlueRejectsEscapeFrame(t *testing.T) {
	s, _ := newRigTestServer(t)
	sess, _ := newTunnelProbe(t, s)
	st := s.stateFor(sess.Userid)
	fish := &RolledFish{Species: "crucian", Grade: 4, WeightG: 3200,
		BiteQuality: "solid", HookHoldP: 1, LuckProtected: true}
	st.mu.Lock()
	st.CastActive, st.InWater, st.Hooked, st.Pending = true, true, true, fish
	st.mu.Unlock()

	tree := proto.Args524(proto.Obj("lhicjpmglbo", 0,
		proto.Field{Op: "U32", Num: ptrU64(2)}))
	if _, ok := hFishingLeft(miscCtx(sess, 14, 12, tree)); !ok {
		t.Fatal("14/12 未处理")
	}
	st.mu.Lock()
	kept := st.Pending == fish && st.LuckRepush == 1 && st.pendingTimer != nil && !st.Hooked
	st.cancelPendingTimerLocked()
	st.mu.Unlock()
	if !kept {
		t.Fatalf("天命蓝冠不应被钩型脱钩清掉: pending=%v retry=%d timer=%v hooked=%v",
			st.Pending != nil, st.LuckRepush, st.pendingTimer != nil, st.Hooked)
	}
}

func TestNibbleDoesNotEnterHookedState(t *testing.T) {
	s, _ := newRigTestServer(t)
	sess, _ := newTunnelProbe(t, s)
	st := s.stateFor(sess.Userid)
	fish := &RolledFish{Species: "crucian", Grade: 1, WeightG: 120,
		BiteQuality: "nibble", HookHoldP: .4}
	st.mu.Lock()
	st.CastActive, st.InWater, st.Resolved, st.Pending = true, true, false, fish
	st.mu.Unlock()

	if _, ok := hFishingHooked(miscCtx(sess, 14, 11, nil)); !ok {
		t.Fatal("14/11 嘬饵处理失败")
	}
	st.mu.Lock()
	cleared := st.Pending == nil && !st.Hooked && st.biteTimer != nil
	st.cancelBiteTimerLocked()
	st.mu.Unlock()
	if !cleared {
		t.Fatalf("嘬饵不应进入 Hooked 状态: pending=%v hooked=%v timer=%v",
			st.Pending != nil, st.Hooked, st.biteTimer != nil)
	}
}

func TestHookQualityPrefersDifferentFishSizes(t *testing.T) {
	tiny := gearPhysicsSpec{HookPresent: true, HookSize: 4, HookLengthMM: 2.5}
	large := gearPhysicsSpec{HookPresent: true, HookSize: 28, HookLengthMM: 24}
	if hookScore(tiny) >= hookScore(large) {
		t.Fatalf("大钩评分应高于小钩: tiny=%.2f large=%.2f", hookScore(tiny), hookScore(large))
	}
	if solid, _ := hookSolidHold(100, large); solid >= .9 {
		t.Fatalf("大钩遇 100g 小鱼不应有稳定入口，solid=%.2f", solid)
	}
	if solid, _ := hookSolidHold(20_000, tiny); solid >= .9 {
		t.Fatalf("小钩遇 20kg 大鱼不应有稳定入口，solid=%.2f", solid)
	}
}

func TestFishHuntCircularGroundbaitRange(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	setWeatherTestTunable(t, s, "groundbait_enabled", "1")
	const uid = int64(1000001)
	s.groundbaitDeposit(ctx, uid, "range-test", 100, 100, true, 7,
		"feed_karp", 10, "catapult", "range:1", "")

	unitsAt := func(x, z float64) float64 {
		return s.groundbaitSpotAt(ctx, uid, "range-test", x, z, true, 7).units
	}
	if got := unitsAt(100, 100); got < 9.99 || got > 10.01 {
		t.Fatalf("核心圈应为全额 10，实得 %.3f", got)
	}
	if got := unitsAt(105, 100); got < 8.6 || got > 8.75 {
		t.Fatalf("5 米距离应约 86.7%%，实得 %.3f", got)
	}
	if got := unitsAt(109, 100); got < 1.9 || got > 2.1 {
		t.Fatalf("9 米距离应约 20%%，实得 %.3f", got)
	}
	if got := unitsAt(111, 100); got != 0 {
		t.Fatalf("10 米外应为 0，实得 %.3f", got)
	}
}

func TestFishHuntReadinessRequiresFishMouthMatch(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	setWeatherTestTunable(t, s, "groundbait_enabled", "1")
	setWeatherTestTunable(t, s, "fish_ecology_enabled", "1")
	setWeatherTestTunable(t, s, "fish_weather_enabled", "1")
	const (
		uid  = int64(1000003)
		loc  = "hunt-stage-test"
		spot = uint64(7)
	)
	s.groundbaitDeposit(ctx, uid, loc, 300, 300, true, spot,
		"feed_dry", 8, "feeder", "stage:1", "")
	h := s.loadFishHuntState(ctx, uid, loc, spot)
	h.ActiveSeconds, h.ValidBites, h.AnchorKnown = 1800, 30, true
	h.AnchorX, h.AnchorZ = 300, 300
	c := fishRollContext{
		uid: uid, loc: loc, x: 300, z: 300, hasPos: true, spot: spot,
		baits: []string{"muckworm"}, rigType: 1, depthCM: 400,
		now: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC), cache: newFishingRollCache(),
		weather: fishingWeather{known: true, kind: fishingWeatherClear, airTemp: 20, previousAirTemp: 20, wind: 2},
		skill: fishingSkillBonus{BiteSpeed: 1, EcologyInterest: .22, RareFish: .18,
			LayerAccuracy: .18, WeatherAdaptation: .2, LureTolerance: .2},
	}
	r := s.fishHuntReadiness(ctx, "crucian", c, h)
	if r.A < .80 || r.G < .50 || r.P < .80 {
		t.Fatalf("窝料与持续度应已充分积累，实得 A=%.2f E=%.2f G=%.2f P=%.2f Q=%.2f",
			r.A, r.E, r.G, r.P, r.Q)
	}
	if r.E < .45 && r.Stage != fishHuntDaily {
		t.Fatalf("鱼口匹配不足时必须守住日常阶段，实得 E=%.2f stage=%s", r.E, fishHuntStageName(r.Stage))
	}
}

func TestFishHuntEscapeCompensation(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	const (
		uid  = int64(1000002)
		loc  = "hunt-test"
		spot = uint64(7)
	)
	f := &RolledFish{FishGUID: "fish-trophy", Grade: 3}
	s.recordHuntEscape(ctx, uid, loc, spot, f)
	h := s.loadFishHuntState(ctx, uid, loc, spot)
	if h.BigMiss != 1 || h.CompFish != 3 || h.CompUntil <= 0 {
		t.Fatalf("奖杯脱钩应留下 3 尾回访补偿，实得 %+v", h)
	}

	s.recordHuntCatch(ctx, uid, loc, spot, f)
	h = s.loadFishHuntState(ctx, uid, loc, spot)
	if h.BigMiss != 0 || h.CompFish != 0 || h.LastCatchGUID != f.FishGUID {
		t.Fatalf("成功入护后应清空脱钩补偿，实得 %+v", h)
	}
}

func TestFishHuntNewSpotResetsAfterTwentyMeters(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	setWeatherTestTunable(t, s, "groundbait_enabled", "1")
	const (
		uid  = int64(1000004)
		loc  = "hunt-anchor-test"
		spot = uint64(7)
	)
	s.groundbaitDeposit(ctx, uid, loc, 100, 100, true, spot,
		"feed_dry", 1, "catapult", "anchor:1", "")
	h := s.loadFishHuntState(ctx, uid, loc, spot)
	h.ValidBites = 7
	s.saveFishHuntState(ctx, h)

	s.groundbaitDeposit(ctx, uid, loc, 115, 100, true, spot,
		"feed_dry", 1, "catapult", "anchor:2", "")
	if got := s.loadFishHuntState(ctx, uid, loc, spot).ValidBites; got != 7 {
		t.Fatalf("20 米内的补窝不应重置发窝进度，实得 %d", got)
	}

	s.groundbaitDeposit(ctx, uid, loc, 125, 100, true, spot,
		"feed_dry", 1, "catapult", "anchor:3", "")
	if got := s.loadFishHuntState(ctx, uid, loc, spot).ValidBites; got != 0 {
		t.Fatalf("超过 20 米的新窝应重置发窝进度，实得 %d", got)
	}
}
