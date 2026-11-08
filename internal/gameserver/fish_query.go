package gameserver

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"
)

type FishQueryRequest struct {
	UID     int64    `json:"uid"`
	Fish    string   `json:"fish"`
	Map     string   `json:"map"`
	Bait    string   `json:"bait"`
	Gear    string   `json:"gear"`
	Grade   int64    `json:"grade"`
	RigType int64    `json:"rig_type"`
	DepthCM int64    `json:"depth_cm"`
	X       *float64 `json:"x"`
	Z       *float64 `json:"z"`
}

type FishQueryReference struct {
	Coordinate string `json:"coordinate"`
	Catches    int64  `json:"catches"`
}

type FishQueryPlan struct {
	Map             string               `json:"map"`
	MapName         string               `json:"map_name"`
	Scene           string               `json:"scene"`
	Bait            string               `json:"bait"`
	BaitName        string               `json:"bait_name"`
	EquivalentBaits int                  `json:"equivalent_baits"`
	RigType         int64                `json:"rig_type"`
	RigName         string               `json:"rig_name"`
	DepthCM         int64                `json:"depth_cm"`
	LureLayer       float64              `json:"lure_layer"`
	Action          string               `json:"action"`
	OnBoat          bool                 `json:"on_boat"`
	At              string               `json:"at"`
	GameHour        int64                `json:"game_hour"`
	GameMinute      int64                `json:"game_minute"`
	WaitSeconds     int64                `json:"wait_seconds"`
	X               *float64             `json:"x"`
	Z               *float64             `json:"z"`
	Zone            int                  `json:"zone"`
	CoordinateKind  string               `json:"coordinate_kind"`
	References      []FishQueryReference `json:"references"`
	Chance          float64              `json:"chance"`
	FishShare       float64              `json:"fish_share"`
	Selection       float64              `json:"selection"`
	Acceptance      float64              `json:"acceptance"`
	GradeShare      float64              `json:"grade_share"`
	BaitTier        int64                `json:"bait_tier"`
	WeightMode      float64              `json:"weight_mode"`
	CapKg           float64              `json:"cap_kg"`
	Weather         map[string]any       `json:"weather"`
}

type FishQueryResponse struct {
	Fish       string             `json:"fish"`
	Grade      int64              `json:"grade"`
	AsOf       string             `json:"as_of"`
	Flags      map[string]float64 `json:"flags"`
	Plans      []FishQueryPlan    `json:"plans"`
	Evaluated  int                `json:"evaluated"`
	BaitItems  int                `json:"bait_items"`
	BaitGroups int                `json:"bait_groups"`
	Maps       int                `json:"maps"`
	Complete   bool               `json:"complete"`
	Elapsed    float64            `json:"elapsed_seconds"`
	Notes      []string           `json:"notes"`
}

type fishQueryMap struct {
	id, name, scene string
	points          []fishQueryPoint
	references      []FishQueryReference
}
type fishQueryPoint struct {
	x, z *float64
	zone int
	kind string
}
type fishQueryBait struct {
	id, name    string
	category    int64
	equivalents int
}

// QueryFish reads candidate weights without rolling fish or modifying player data.
func (s *Server) QueryFish(ctx context.Context, req FishQueryRequest) (*FishQueryResponse, error) {
	started := time.Now()
	if s.DB == nil || req.UID <= 0 || req.Fish == "" {
		return nil, fmt.Errorf("请选择玩家和目标鱼种")
	}
	if req.Map == "" {
		return nil, fmt.Errorf("请指定地图，快速查询只计算当前时段")
	}
	if req.RigType != 0 && rigTypeNames[req.RigType] == "" {
		return nil, fmt.Errorf("不支持的钓组")
	}
	if req.Grade == 0 {
		req.Grade = 1
	}
	if req.Grade < 1 || req.Grade > 4 || req.DepthCM < 0 || req.DepthCM > 30000 {
		return nil, fmt.Errorf("等级或钓深无效")
	}
	if (req.X == nil) != (req.Z == nil) || req.X != nil && (req.Map == "" || !finiteSonarNumber(*req.X) || !finiteSonarNumber(*req.Z) || math.Abs(*req.X) > 1e7 || math.Abs(*req.Z) > 1e7) {
		return nil, fmt.Errorf("手动坐标需要同时指定地图、X 和 Z")
	}
	var exists int
	if err := s.DB.SQL.QueryRowContext(ctx, `SELECT COUNT(*) FROM profile WHERE uid=?`, req.UID).Scan(&exists); err != nil {
		return nil, err
	}
	if exists != 1 {
		return nil, fmt.Errorf("玩家不存在")
	}
	cache := newFishingRollCache()
	base := fishRollContext{cache: cache, now: started}
	response := &FishQueryResponse{Fish: req.Fish, Grade: req.Grade, AsOf: started.UTC().Format(time.RFC3339), Flags: map[string]float64{}, Plans: []FishQueryPlan{}, Complete: true}
	for key, def := range map[string]float64{"fish_bait_rules_enabled": 0, "fish_distribution_enabled": 0, "fish_weather_enabled": 0, "fish_ecology_enabled": 0, "bait_pref_scale": 1, "bait_pref_weight_shift": 1, "fish_weight_scale": 1, "fish_weight_cap": 0, "hook_entry_cap": 0, "bite_probability": 1} {
		response.Flags[key] = s.tunableF(key, def)
	}
	capKg := 0.0
	var allowedRigs []int64
	if req.Gear != "" {
		gear, err := s.DB.GetItemByGUID(ctx, req.Gear)
		if err != nil || gear == nil || gear.UID != req.UID {
			return nil, fmt.Errorf("鱼竿不属于所选玩家")
		}
		def, err := s.DB.GetItemDef(ctx, gear.NumericID)
		if err != nil || def == nil || def.Category != gearCatRod {
			return nil, fmt.Errorf("请选择有效鱼竿")
		}
		allowedRigs = rodRigAllowed[rodDefRodType(def)]
		if req.RigType != 0 && len(allowedRigs) > 0 && !allowedContains(allowedRigs, req.RigType) {
			return nil, fmt.Errorf("所选鱼竿不支持此钓组")
		}
		capKg, _, _ = s.gearWeightCapForRig(ctx, req.UID, req.Gear)
	} else if response.Flags["fish_weight_cap"] > 0 || response.Flags["hook_entry_cap"] > 0 {
		return nil, fmt.Errorf("装备限重已开启，请先选择鱼竿")
	}
	maps, err := s.fishQueryMaps(ctx, req, base)
	if err != nil {
		return nil, err
	}
	response.Maps = len(maps)
	if len(maps) == 0 {
		response.Elapsed = time.Since(started).Seconds()
		response.Notes = []string{"当前地图的服务端鱼种名册没有目标鱼；可指定其他地图再查。"}
		return response, nil
	}
	baits, count, err := s.fishQueryBaits(ctx, req, base, response.Flags)
	if err != nil {
		return nil, err
	}
	response.BaitItems, response.BaitGroups = count, len(baits)
	bonuses := map[int64]fishingSkillBonus{}
	weatherSession := &Session{Srv: s}
	for _, loc := range maps {
		weatherSession.rememberWeather(loc.id, started.Unix())
	}
	happy, err := s.DB.GetHappyHourStatus(ctx, req.UID, started)
	if err != nil {
		return nil, err
	}
	type weatherKey struct {
		loc string
		at  int64
	}
	weatherCache := map[weatherKey]fishingWeather{}
	names, _ := s.cachedFishNames(ctx, base)
	rule := fishBaitRules[ruleName(names[req.Fish])]
	evaluate := func(loc fishQueryMap, bait fishQueryBait, point fishQueryPoint, at time.Time, rig int64, depth int64, layer float64, action string, mask uint32) FishQueryPlan {
		c := base
		c.loc, c.baits, c.now, c.rigType, c.depthCM, c.lureLayer, c.lureAction, c.lureMask = loc.scene, []string{bait.id}, at, rig, depth, layer, action, mask
		c.onBoat, c.boatKnown = sonarChartLevel(loc.id), true
		if point.x != nil {
			c.x, c.z, c.hasPos = *point.x, *point.z, true
		}
		if _, ok := bonuses[rig]; !ok {
			bonuses[rig] = s.fishingBonus(ctx, req.UID, rig, req.Gear)
		}
		bonus := bonuses[rig]
		c.skill = bonus
		if at.Equal(started) {
			c.happyBite = happy.BiteBonus
		}
		c.forRig = response.Flags["fish_bait_rules_enabled"] == 1 || response.Flags["fish_weather_enabled"] == 1 || c.happyBite > 0
		wk := weatherKey{loc.id, at.Unix()}
		w, ok := weatherCache[wk]
		if !ok {
			w = weatherSession.weatherAt(loc.id, at)
			weatherCache[wk] = w
		}
		c.weather = w
		c.query = &fishQueryCapture{fish: req.Fish, grade: req.Grade}
		s.rollFishWithRules(loc.scene, bait.id, capKg, bonus, c)
		response.Evaluated++
		_, hour := worldDayAndHour(at.Unix())
		minute := ((at.Unix() - worldDayEpochUnix) % realSecPerGameHour) * 60 / realSecPerGameHour
		return FishQueryPlan{Map: loc.id, MapName: loc.name, Scene: loc.scene, Bait: bait.id, BaitName: bait.name, EquivalentBaits: bait.equivalents, RigType: rig, RigName: rigNameForType(rig), DepthCM: depth, LureLayer: layer, Action: action, OnBoat: c.onBoat, At: at.UTC().Format(time.RFC3339), GameHour: hour, GameMinute: minute, WaitSeconds: int64(at.Sub(started).Seconds()), X: point.x, Z: point.z, Zone: point.zone, CoordinateKind: point.kind, References: loc.references, Chance: c.query.chance, FishShare: c.query.fishShare, Selection: c.query.selection, Acceptance: c.query.acceptance, GradeShare: c.query.gradeShare, BaitTier: s.cachedBaitTiers(ctx, base, bait.id)[req.Fish], WeightMode: s.baitWeightModeForRoll(req.Fish, []string{bait.id}, base), CapKg: capKg, Weather: map[string]any{"known": w.known, "kind": w.kind, "air_temp": roundTo(w.airTemp, 1), "wind": roundTo(w.wind, 2)}}
	}
	var candidates []FishQueryPlan
	for _, loc := range maps {
		for _, bait := range baits {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			rigs := []int64{1, 12}
			if sonarChartLevel(loc.id) {
				rigs = []int64{55}
				if b, ok := s.cachedBaitDefinition(ctx, base, bait.id); ok && b.matches("鱼肉类") {
					rigs = append(rigs, 52)
				}
			}
			if bait.category == 17 {
				rigs = []int64{5}
				if sonarChartLevel(loc.id) {
					rigs = []int64{50, 51}
				}
			}
			if req.RigType > 0 {
				rigs = []int64{req.RigType}
			}
			for _, rig := range rigs {
				if len(allowedRigs) > 0 && !allowedContains(allowedRigs, rig) {
					continue
				}
				if !fishQueryBaitFitsRig(bait.category, rig) {
					continue
				}
				depth := req.DepthCM
				if depth == 0 {
					depth = 150
				}
				layer, action, mask := fishQueryMotion(rig, rule)
				var best FishQueryPlan
				// Every shortlisted bait sees every candidate point, including
				// points where the first chart zone has zero target probability.
				for _, point := range loc.points {
					plan := evaluate(loc, bait, point, started, rig, depth, layer, action, mask)
					if plan.Chance > best.Chance {
						best = plan
					}
				}
				if best.Chance > 0 {
					candidates = append(candidates, best)
				}
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if math.Abs(candidates[i].Chance-candidates[j].Chance) < 1e-12 {
			return candidates[i].WaitSeconds < candidates[j].WaitSeconds
		}
		return candidates[i].Chance > candidates[j].Chance
	})
	seen := map[string]bool{}
	for _, plan := range candidates {
		key := fmt.Sprintf("%s/%s/%d", plan.Map, plan.Bait, plan.RigType)
		if !seen[key] {
			response.Plans = append(response.Plans, plan)
			seen[key] = true
		}
		if len(response.Plans) >= 12 {
			break
		}
	}
	response.Elapsed = time.Since(started).Seconds()
	response.Notes = []string{"概率按模拟器一次判鱼估计，不是官服概率；不包含窝料、刺鱼、跑鱼、收鱼和鱼竿操作。", "快速推荐只查指定地图和当前时段：按目标鱼饵效档位及观测数筛选天然饵、拟饵各最多24组，再用实际服务端规则计算；不是全饵、全地图或全天候最优解。", "海图比较最多6个有依据的候选落点；指定鱼饵可单独精算。海钓假设已上船，内陆默认岸钓。", "世界X/Z是抛竿落点，不是角色站位；内陆攻略坐标只作历史观测参考，未换算成世界坐标。", "钓深、水层、底质与水温使用模拟器模型；使用单饵与代表动作，未穷举双饵、多钩和全部钓组。"}
	if response.Flags["fish_distribution_enabled"] == 1 || response.Flags["fish_ecology_enabled"] == 1 {
		response.Notes = append(response.Notes, "内陆未输入世界坐标时，位置项按场景钓点0预估，抵达真实落点后的概率可能变化。")
	}
	return response, nil
}

func fishQueryBaitFitsRig(category, rig int64) bool {
	if rig == 52 || rig == 54 {
		return category == 27
	}
	if rigTypeLureFamily[rig] {
		return category == 17
	}
	return category == 27
}

func fishQueryMotion(rig int64, rule fishBaitRule) (float64, string, uint32) {
	if !rigTypeLureFamily[rig] {
		return 0, "", 0
	}
	if rule.needDrift {
		return .5, "drift", lureMotionDriftBit
	}
	return .5, "jig", lureMotionJigBit
}
