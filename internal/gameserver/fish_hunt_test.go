package gameserver

import (
	"math"
	"testing"
	"time"
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
		w1, w2, w3, w4, force := fishHuntWeights(tc.stage, &fishHuntState{}, 0, false)
		if force || math.Abs(w1-tc.w1) > 1e-9 || math.Abs(w2-tc.w2) > 1e-9 ||
			math.Abs(w3-tc.w3) > 1e-9 || math.Abs(w4-tc.w4) > 1e-9 {
			t.Fatalf("stage=%d weights=(%.1f,%.1f,%.1f,%.1f) force=%v", tc.stage, w1, w2, w3, w4, force)
		}
	}
}

func TestFishHuntPityForcesTrophy(t *testing.T) {
	_, _, _, _, force := fishHuntWeights(fishHuntActive, &fishHuntState{Pity: 60}, 0, false)
	if !force {
		t.Fatal("60 尾未出奖杯时必须触发奖杯硬保底")
	}
	_, _, w3, _, force := fishHuntWeights(fishHuntActive, &fishHuntState{Pity: 30}, 0, false)
	if force || w3 <= 6.8 {
		t.Fatalf("30 尾软保底应提高奖杯权重，w3=%.2f force=%v", w3, force)
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
