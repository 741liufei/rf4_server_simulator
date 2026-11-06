package gameserver

import (
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFishEcologyAssetCoversAllSpeciesAndActions(t *testing.T) {
	var profiles []fishEcologyProfile
	if err := json.Unmarshal(fishEcologyDefsJSON, &profiles); err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 252 {
		t.Fatalf("生态资产=%d，期望 252", len(profiles))
	}
	seen := map[string]bool{}
	for _, profile := range profiles {
		if profile.FishKey == "" || seen[profile.FishKey] {
			t.Fatalf("鱼种 key 为空或重复: %q", profile.FishKey)
		}
		seen[profile.FishKey] = true
		if profile.ProfileVersion != fishEcologyProfileVersion {
			t.Errorf("%s profile_version=%d", profile.FishKey, profile.ProfileVersion)
		}
		if profile.RarityTier < 0 || profile.RarityTier > 3 {
			t.Errorf("%s rarity_tier=%d", profile.FishKey, profile.RarityTier)
		}
		if !(0 <= profile.LayerMin && profile.LayerMin < profile.LayerPref &&
			profile.LayerPref < profile.LayerMax && profile.LayerMax <= 1) {
			t.Errorf("%s 水层范围非法: %+v", profile.FishKey, profile)
		}
		if !(profile.TempMinC < profile.TempPrefC && profile.TempPrefC < profile.TempMaxC) {
			t.Errorf("%s 温度范围非法: %+v", profile.FishKey, profile)
		}
		for name, raw := range map[string]string{
			"diet": profile.DietJSON, "weather": profile.WeatherJSON,
			"action": profile.ActionJSON, "substrate": profile.SubstrateJSON,
			"color": profile.ColorJSON,
		} {
			if len(fishEcologyJSONMap(raw)) == 0 {
				t.Errorf("%s %s 矩阵为空", profile.FishKey, name)
			}
		}
		actions := fishEcologyJSONMap(profile.ActionJSON)
		if len(actions) != len(actionNamesForTest()) {
			t.Errorf("%s 动作数=%d，期望 %d", profile.FishKey, len(actions), len(actionNamesForTest()))
		}
	}
}

func TestEveryLureMotionBitHasDistinctAction(t *testing.T) {
	seen := map[string]uint32{}
	unknown := map[uint]bool{15: true, 18: true, 20: true}
	for bit := uint(0); bit <= 22; bit++ {
		mask := uint32(1) << bit
		action := classifyLureMotion(mask)
		if unknown[bit] {
			if action != "" {
				t.Errorf("未证实动作位 0x%X 不应映射为 %q", mask, action)
			}
			continue
		}
		if action == "" {
			t.Errorf("动作位 0x%X 未识别", mask)
			continue
		}
		if previous, ok := seen[action]; ok {
			t.Errorf("动作 %q 同时对应 0x%X 和 0x%X", action, previous, mask)
		}
		seen[action] = mask
	}
	if len(seen) != 20 {
		t.Fatalf("已识别动作=%d，期望 20", len(seen))
	}
}

func TestWaterTemperatureUsesSeasonDepthAndLag(t *testing.T) {
	profile := defaultFishEcology("carp")
	summer := time.Unix(worldDayEpochUnix+120*24*realSecPerGameHour+12*realSecPerGameHour, 0)
	winter := time.Unix(worldDayEpochUnix+300*24*realSecPerGameHour+12*realSecPerGameHour, 0)
	weather := fishingWeather{known: true, airTemp: 18, previousAirTemp: 18, kind: fishingWeatherCloudy, wind: 3}
	shallow := fishRollContext{rigType: 1, depthCM: 50, now: summer, weather: weather}
	deep := shallow
	deep.now = winter
	deep.depthCM = 500
	summerShallow := fishWaterConditionsFor(profile, shallow)
	winterDeep := fishWaterConditionsFor(profile, deep)
	if summerShallow.WaterTempC <= winterDeep.WaterTempC {
		t.Fatalf("夏季浅水 %.2f 应高于冬季深水 %.2f", summerShallow.WaterTempC, winterDeep.WaterTempC)
	}
	if summerShallow.Clarity <= 0 || summerShallow.Light <= 0 || summerShallow.Turbulence < 0 {
		t.Fatalf("水环境字段非法: %+v", summerShallow)
	}
}

func TestLureLightColorAndSkillAffectBiteWeight(t *testing.T) {
	profile := fishEcologyProfile{
		ColorJSON:  `{"silver":1.45,"dark":0.55}`,
		Aggression: 1.2,
	}
	c := fishRollContext{
		rigType: 5, lureLayer: .45, lureAction: "twitch",
		runtime: &fishRollRuntime{
			lureKnown: true,
			lure: lureAttributes{
				Type: "jerkbait", Color: "silver", ColorKnown: true, Reflectivity: 1,
				Vibration: .8, WeightG: 18, DiveDepth: .45,
			},
		},
	}
	brightWater := fishWaterConditions{Light: .85, Clarity: .85, Turbulence: .35}
	darkWater := fishWaterConditions{Light: .12, Clarity: .35, Turbulence: .35}
	bright := (&Server{}).fishLureWeight(profile, c, brightWater)
	c.runtime.lure.Color = "dark"
	c.runtime.lure.Reflectivity = .25
	dark := (&Server{}).fishLureWeight(profile, c, darkWater)
	if math.Abs(bright-dark) < .05 {
		t.Fatalf("银亮清水与暗色浑水权重没有差异: bright=%f dark=%f", bright, dark)
	}

	neutral := fishSkillEnvironmentWeight("n.pike", fishRollContext{rigType: 5})
	skilled := fishSkillEnvironmentWeight("n.pike", fishRollContext{
		rigType: 5,
		skill: fishingSkillBonus{
			BiteSpeed: .7, EcologyInterest: .12, LureTolerance: .14,
			WeatherAdaptation: .1, LayerAccuracy: .1, PredatorBoost: .16,
		},
	})
	if skilled <= neutral {
		t.Fatalf("专项技能未提高鱼口: neutral=%f skilled=%f", neutral, skilled)
	}
}

func TestSpotEnvironmentUsesStableSubstrateCurrentAndDepth(t *testing.T) {
	first := fishRollContext{loc: "level_012_north_sea", spot: 80, rigType: 50}
	firstEnv := fishSpotEnvironmentFor(&first)
	secondEnv := fishSpotEnvironmentFor(&first)
	if firstEnv != secondEnv {
		t.Fatalf("同一钓点环境不稳定: %+v != %+v", firstEnv, secondEnv)
	}
	if !firstEnv.Known || firstEnv.DepthM <= 0 || firstEnv.Current <= 0 ||
		firstEnv.Structure <= 0 || firstEnv.Substrate == "" {
		t.Fatalf("钓点环境不完整: %+v", firstEnv)
	}
	profile := fishEcologyProfile{
		SubstrateJSON: `{"mud":.5,"sand":1,"gravel":1,"rock":1,"weed":1}`,
		CurrentPref:   .5, Aggression: 1.2, LayerPref: .8,
	}
	if got := fishSpotBiteWeight(profile, firstEnv); got < .3 || got > 1.6 {
		t.Fatalf("钓点鱼口倍率越界: %f", got)
	}
}

func TestLureAttributesUseConfirmedTypeAndDerivedFields(t *testing.T) {
	srv, db := newRigTestServer(t)
	staticPath := filepath.Join("..", "..", "server_data", "static.sqlite3")
	if _, err := db.SQL.Exec(`ATTACH DATABASE ? AS src`, staticPath); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT OR IGNORE INTO item_def SELECT * FROM src.item_def WHERE item_id='crank_2603'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT OR IGNORE INTO item_catalog SELECT * FROM src.item_catalog WHERE item_id='crank_2603'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`DETACH DATABASE src`); err != nil {
		t.Fatal(err)
	}
	lure, ok := srv.loadLureAttributes(t.Context(), "crank_2603")
	if !ok {
		t.Fatal("crank_2603 未加载")
	}
	if lure.Type != "crank" || lure.SizeCM <= 0 || lure.WeightG <= 0 ||
		lure.DiveDepth <= 0 || lure.Reflectivity <= 0 || lure.SinkRate <= 0 {
		t.Fatalf("拟饵属性不完整: %+v", lure)
	}
	if got := lureActionTypeWeight(lure.Type, "trolling"); got <= 1 {
		t.Fatalf("crank 拖钓动作未加分: %f", got)
	}
}

func TestSkillFishEffectsAreTableDrivenAndSharedGroupsCountOnce(t *testing.T) {
	srv, db := newRigTestServer(t)
	staticPath := filepath.Join("..", "..", "server_data", "static.sqlite3")
	if _, err := db.SQL.Exec(`ATTACH DATABASE ? AS src`, staticPath); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT OR IGNORE INTO skill_def SELECT * FROM src.skill_def`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`DETACH DATABASE src`); err != nil {
		t.Fatal(err)
	}
	inserted, err := srv.ensureFishSkillEffects(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if inserted < 70 {
		t.Fatalf("鱼口技能效果只生成 %d 条", inserted)
	}

	for _, nodes := range [][]int64{
		{514, 701, 313, 1002},
		{515, 516},
		{718, 1008},
		{721, 1020},
	} {
		var signatures int
		query := `SELECT COUNT(DISTINCT effect_key||'/'||per_level||'/'||cap)
			FROM skill_fish_effect_def WHERE node_id IN (` +
			strings.TrimSuffix(strings.Repeat("?,", len(nodes)), ",") + `)`
		args := make([]any, len(nodes))
		for i, node := range nodes {
			args[i] = node
		}
		if err := db.SQL.QueryRow(query, args...).Scan(&signatures); err != nil {
			t.Fatal(err)
		}
		if signatures != 1 {
			t.Fatalf("共享技能 %v 效果签名不一致: %d", nodes, signatures)
		}
	}
	defs := []skillNodeDef{
		{NodeID: 701, CatID: 2, MaxLevel: 5, Enabled: true},
	}
	effects := effectiveFishingEffects{
		Learned:   map[int64]int64{514: 2, 701: 2, 313: 2, 1002: 2},
		GearNodes: map[int64]int64{701: 1},
	}
	bonus := neutralFishingSkillBonus()
	srv.applyFishingNodeBonuses(t.Context(), 2, &bonus, effects, defs)
	total := bonus.EcologyInterest + bonus.RareFish + bonus.LureTolerance +
		bonus.WeatherAdaptation + bonus.LayerAccuracy + bonus.WormBoost +
		bonus.PredatorBoost + bonus.SalmonidBoost + bonus.BottomBoost + bonus.FloatBoost
	var perLevel, cap float64
	if err := db.SQL.QueryRow(`SELECT per_level,cap FROM skill_fish_effect_def
		WHERE cat_id=2 AND node_id=701`).Scan(&perLevel, &cap); err != nil {
		t.Fatal(err)
	}
	want := math.Min(cap, 3*perLevel) // 学习 2 级 + 鱼竿赠 1 级
	if math.Abs(total-want) > 1e-9 {
		t.Fatalf("共享技能赠级结算错误 total=%f want=%f bonus=%+v", total, want, bonus)
	}
}

func actionNamesForTest() []string {
	return []string{
		"stir", "speed_up", "jerk", "bottom_contact", "twitch", "splash",
		"drift", "jig", "walk_dog", "water_column_motionless", "steady",
		"bottom_motionless", "surface_motionless", "bottom_move", "trolling",
		"motion_15", "stop_go", "hard_twitch", "motion_18", "vertical_dive",
		"motion_20", "vertical_ascend", "bottom_layer_move",
	}
}
