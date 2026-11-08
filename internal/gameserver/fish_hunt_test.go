package gameserver

import (
	"fmt"
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

func TestHuntBaitMultiplierStaysInsideSelectedBand(t *testing.T) {
	lo, hi, _, ok := huntBandBounds(100, 200, 400, 3)
	if !ok {
		t.Fatal("奖杯档位边界缺失")
	}
	low, high := lo*2, hi*2
	for _, weight := range []float64{low, (low + high) / 2, high} {
		got := clampHuntBandWeight(weight, low, high, 2, 0)
		if got < low || got > high {
			t.Fatalf("饵效倍率越过奖杯档: weight=%.2f got=%.2f range=[%.2f,%.2f]",
				weight, got, low, high)
		}
	}
	if got := clampHuntBandWeight(high, low, high, 2, 0); got >= 400*2 {
		t.Fatalf("奖杯档被饵效倍率推入蓝冠档: %.2f", got)
	}
}

func TestHuntBaitMultiplierKeepsSelectedGrade(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	c := fishRollContext{cache: newFishingRollCache()}
	lines := s.cachedSettleStatics(ctx, c, "perch")
	if lines[0] <= 0 || lines[1] <= lines[0] || lines[2] <= lines[1] {
		t.Fatalf("perch 档位线不可用: %v", lines)
	}
	sampled := 0
	for i := 0; i < 1000; i++ {
		weight := s.sampleHuntBand(ctx, "perch", 3, 2, 1, 0, c)
		if weight <= 0 {
			continue
		}
		sampled++
		if grade := s.rollGradeForRoll(ctx, "perch", weight, c); grade != 3 {
			t.Fatalf("饵效倍率把奖杯档推到 grade=%d: weight=%d lines=%v", grade, weight, lines)
		}
	}
	if sampled == 0 {
		t.Fatal("没有采到奖杯档样本")
	}
}

func TestFishHuntBlueProbabilityIsHookIndependent(t *testing.T) {
	blueProbability := func(w1, w2, w3, w4 float64) float64 {
		total := w1 + w2 + w3 + w4
		if total <= 0 {
			return 0
		}
		return w4 / total
	}
	tests := []struct {
		name string
		h    *fishHuntState
		comp bool
	}{
		{name: "active", h: &fishHuntState{}},
		{name: "softPity", h: &fishHuntState{Pity: 55}},
		{name: "hardPity", h: &fishHuntState{Pity: 60}},
		{name: "blueComp", h: &fishHuntState{CompFish: 4, CompGrade: 4}, comp: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			small1, small2, small3, small4, _ := fishHuntWeights(fishHuntActive, tc.h, 0, .1, tc.comp)
			large1, large2, large3, large4, _ := fishHuntWeights(fishHuntActive, tc.h, 0, .9, tc.comp)
			smallP := blueProbability(small1, small2, small3, small4)
			largeP := blueProbability(large1, large2, large3, large4)
			if math.Abs(smallP-largeP) > 1e-12 {
				t.Fatalf("归一化后的蓝冠概率不能受钩型影响: small=%.12f large=%.12f", smallP, largeP)
			}
		})
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
		HookConfirmed: true, HuntStage: fishHuntActive, HuntStageKnown: true,
	}
	got := rolledFishFromStore(rolledFishToStore(want))
	if got == nil || got.BiteQuality != want.BiteQuality || got.HookHoldP != want.HookHoldP ||
		got.LuckProtected != want.LuckProtected || got.HookConfirmed != want.HookConfirmed ||
		got.HuntStage != want.HuntStage || got.HuntStageKnown != want.HuntStageKnown {
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

func TestLuckProtectedBlueDoesNotRetryTerminalFrame(t *testing.T) {
	s, _ := newRigTestServer(t)
	sess, _ := newTunnelProbe(t, s)
	st := s.stateFor(sess.Userid)
	fish := &RolledFish{Species: "crucian", Grade: 4, WeightG: 3200,
		BiteQuality: "solid", HookHoldP: 1, LuckProtected: true}
	st.mu.Lock()
	st.CastActive, st.InWater, st.Hooked, st.Pending = true, true, true, fish
	st.mu.Unlock()

	tree := proto.Args524(proto.Obj("lhicjpmglbo", 0,
		proto.Field{Op: "U32", Num: ptrU64(7)}))
	if _, ok := hFishingLeft(miscCtx(sess, 14, 12, tree)); !ok {
		t.Fatal("14/12 f0=7 未处理")
	}
	st.mu.Lock()
	terminal := st.Pending == nil && !st.Hooked && st.Resolved && st.pendingTimer == nil
	st.cancelBiteTimerLocked()
	st.mu.Unlock()
	if !terminal {
		t.Fatalf("f0=7 是终态，不得因蓝冠保护重推: pending=%v hooked=%v resolved=%v timer=%v",
			st.Pending != nil, st.Hooked, st.Resolved, st.pendingTimer != nil)
	}
}

func TestUnhookedTrophyUnknownFrameDoesNotGrantEscapeCompensation(t *testing.T) {
	s, _ := newRigTestServer(t)
	sess, _ := newTunnelProbe(t, s)
	setWeatherTestTunable(t, s, "fish_hunt_enabled", "1")
	st := s.stateFor(sess.Userid)
	fish := &RolledFish{FishGUID: "unhooked-trophy", Species: "carp", Grade: 3, WeightG: 12_000}
	st.mu.Lock()
	st.Pending, st.CastActive, st.InWater, st.Hooked = fish, true, true, false
	st.Loc, st.Spot = "unhooked-14-12", 7
	st.mu.Unlock()

	tree := proto.Args524(
		proto.Guid(st.GearGuid),
		proto.Guid(fish.FishGUID),
		proto.Obj("lhicjpmglbo", 0, proto.Field{Op: "U32", Num: ptrU64(4)}),
	)
	if _, ok := hFishingLeft(miscCtx(sess, 14, 12, tree)); !ok {
		t.Fatal("14/12 f0=4 未处理")
	}
	h := s.loadFishHuntState(t.Context(), sess.Userid, "unhooked-14-12", 7)
	if h.BigMiss != 0 || h.CompFish != 0 || h.CompUntil != 0 {
		t.Fatalf("未中钩鱼不得获得脱钩补偿: %+v", h)
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

func TestDuplicateHookedConfirmationIsIdempotent(t *testing.T) {
	s, _ := newRigTestServer(t)
	sess, _ := newTunnelProbe(t, s)
	st := s.stateFor(sess.Userid)
	fish := &RolledFish{Species: "carp", Grade: 2, WeightG: 3200,
		BiteQuality: "solid", HookHoldP: 1}
	st.mu.Lock()
	st.CastActive, st.InWater, st.Pending = true, true, fish
	st.mu.Unlock()

	if _, ok := hFishingHooked(miscCtx(sess, 14, 11, nil)); !ok {
		t.Fatal("首次 14/11 未处理")
	}
	st.mu.Lock()
	if !st.Hooked || st.Pending != fish {
		st.mu.Unlock()
		t.Fatal("首次 14/11 没有稳定中钩")
	}
	// 重复确认不得再次抽样，也不得因为当前状态被误改而丢鱼。
	fish.BiteQuality = "nibble"
	st.mu.Unlock()
	if _, ok := hFishingHooked(miscCtx(sess, 14, 11, nil)); !ok {
		t.Fatal("重复 14/11 未处理")
	}
	st.mu.Lock()
	kept := st.Hooked && st.Pending == fish
	st.cancelBiteTimerLocked()
	st.cancelPendingTimerLocked()
	st.mu.Unlock()
	if !kept {
		t.Fatal("重复 14/11 不得重抽保持率或清掉已中钩鱼")
	}
}

func TestMarineNibbleDoesNotEnterHookedState(t *testing.T) {
	s, _ := newRigTestServer(t)
	sess, _ := newTunnelProbe(t, s)
	st := s.stateFor(sess.Userid)
	fish := &RolledFish{Species: "atl_cod", Grade: 2, WeightG: 4200,
		BiteQuality: "nibble", HookHoldP: .4}
	st.mu.Lock()
	st.RigType, st.CastActive, st.InWater, st.Pending = 50, true, true, fish
	st.mu.Unlock()

	if _, ok := hFishingPull(miscCtx(sess, 14, 9, nil)); !ok {
		t.Fatal("14/9 海钓嘬饵处理失败")
	}
	st.mu.Lock()
	cleared := st.Pending == nil && !st.Hooked && st.biteTimer != nil
	st.cancelBiteTimerLocked()
	st.mu.Unlock()
	if !cleared {
		t.Fatalf("海钓嘬饵不应进入 Hooked 状态: pending=%v hooked=%v timer=%v",
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
	f := &RolledFish{FishGUID: "fish-trophy", Grade: 3, HuntStageKnown: true, HuntStage: fishHuntActive}
	s.recordHuntEscape(ctx, uid, loc, spot, f)
	h := s.loadFishHuntState(ctx, uid, loc, spot)
	if h.BigMiss != 1 || h.CompFish != 3 || h.CompUntil <= 0 {
		t.Fatalf("奖杯脱钩应留下 3 尾回访补偿，实得 %+v", h)
	}

	for i := 1; i <= 3; i++ {
		f = &RolledFish{FishGUID: fmt.Sprintf("fish-comp-%d", i), Grade: 1,
			HuntStageKnown: true, HuntStage: fishHuntGather}
		s.recordHuntCatch(ctx, uid, loc, spot, f)
		h = s.loadFishHuntState(ctx, uid, loc, spot)
		if want := int64(3 - i); h.CompFish != want {
			t.Fatalf("第 %d 尾有效鱼后补偿剩余=%d，期望 %d: %+v", i, h.CompFish, want, h)
		}
		s.recordHuntCatch(ctx, uid, loc, spot, f)
		if replay := s.loadFishHuntState(ctx, uid, loc, spot); replay.CompFish != h.CompFish {
			t.Fatalf("同一尾 FishGUID 重放不得再次消耗补偿: before=%d after=%d", h.CompFish, replay.CompFish)
		}
		if i == 3 && (h.CompGrade != 0 || h.CompUntil != 0) {
			t.Fatalf("补偿耗尽后应清空档位和截止时间: %+v", h)
		}
	}
	if h.BigMiss != 1 || h.Pity != 3 {
		t.Fatalf("三尾普通鱼应逐尾消耗补偿并只累计聚鱼阶段保底: %+v", h)
	}
}

func TestFishHuntDailyCatchDoesNotAccumulatePity(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	const (
		uid  = int64(1000006)
		loc  = "hunt-daily-test"
		spot = uint64(7)
	)
	daily := &RolledFish{FishGUID: "fish-daily", Grade: 1,
		HuntStageKnown: true, HuntStage: fishHuntDaily}
	s.recordHuntCatch(ctx, uid, loc, spot, daily)
	h := s.loadFishHuntState(ctx, uid, loc, spot)
	if h.Pity != 0 {
		t.Fatalf("日常鱼不得累计奖杯保底: %+v", h)
	}
	unknown := &RolledFish{FishGUID: "fish-unknown-stage", Grade: 1}
	s.recordHuntCatch(ctx, uid, loc, spot, unknown)
	h = s.loadFishHuntState(ctx, uid, loc, spot)
	if h.Pity != 0 {
		t.Fatalf("旧快照阶段未知时不得猜测并累计保底: %+v", h)
	}
	gather := &RolledFish{FishGUID: "fish-gather", Grade: 1,
		HuntStageKnown: true, HuntStage: fishHuntGather}
	s.recordHuntCatch(ctx, uid, loc, spot, gather)
	h = s.loadFishHuntState(ctx, uid, loc, spot)
	if h.Pity != 1 {
		t.Fatalf("聚鱼阶段普通鱼应累计奖杯保底: %+v", h)
	}
}

func TestFishHuntCompensationExpires(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	const (
		uid  = int64(1000007)
		loc  = "hunt-expire-test"
		spot = uint64(7)
	)
	h := s.loadFishHuntState(ctx, uid, loc, spot)
	h.CompFish, h.CompGrade, h.CompUntil = 1, 4, float64(time.Now().Add(-time.Second).Unix())
	s.saveFishHuntState(ctx, h)
	s.recordHuntCatch(ctx, uid, loc, spot, &RolledFish{
		FishGUID: "fish-after-expire", Grade: 1, HuntStageKnown: true, HuntStage: fishHuntGather,
	})
	h = s.loadFishHuntState(ctx, uid, loc, spot)
	if h.CompFish != 0 || h.CompGrade != 0 || h.CompUntil != 0 {
		t.Fatalf("超时补偿应直接失效: %+v", h)
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
