package gameserver

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"time"
)

// fishEcologyProfile 是鱼口系统使用的鱼种生态基线。水层使用 0=水面、1=水底
// 的归一化坐标，后续可叠加具体钓点水深，不把某条河的实际米数写死在鱼种上。
type fishEcologyProfile struct {
	FishKey     string
	LayerMin    float64
	LayerPref   float64
	LayerMax    float64
	TempMinC    float64
	TempPrefC   float64
	TempMaxC    float64
	DielMask    int64
	DietJSON    string
	WeatherJSON string
	ActionJSON  string
	Aggression  float64
	Enabled     bool
}

func (s *Server) ensureFishEcology(ctx context.Context) (int, error) {
	if s == nil || s.DB == nil {
		return 0, nil
	}
	rows, err := s.DB.SQL.QueryContext(ctx, `
		SELECT fish_key FROM species_weight
		UNION
		SELECT fish_key FROM loc_fish`)
	if err != nil {
		return 0, err
	}
	var keys []string
	for rows.Next() {
		var key string
		if rows.Scan(&key) == nil && strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return 0, err
	}
	now := float64(time.Now().Unix())
	inserted := 0
	for _, key := range keys {
		p := defaultFishEcology(key)
		res, ierr := s.DB.SQL.ExecContext(ctx, `
			INSERT OR IGNORE INTO fish_ecology(
				fish_key, layer_min, layer_pref, layer_max,
				temp_min_c, temp_pref_c, temp_max_c, diel_mask,
				diet_json, weather_json, action_json, aggression, enabled, updated_ts
			) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			p.FishKey, p.LayerMin, p.LayerPref, p.LayerMax,
			p.TempMinC, p.TempPrefC, p.TempMaxC, p.DielMask,
			p.DietJSON, p.WeatherJSON, p.ActionJSON, p.Aggression, boolInt(p.Enabled), now)
		if ierr != nil {
			return inserted, ierr
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		}
	}
	return inserted, nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func defaultFishEcology(key string) fishEcologyProfile {
	sp := strings.ToLower(key)
	has := func(parts ...string) bool {
		for _, part := range parts {
			if strings.Contains(sp, part) {
				return true
			}
		}
		return false
	}
	p := fishEcologyProfile{
		FishKey: key, LayerMin: 0, LayerPref: .55, LayerMax: 1,
		TempMinC: 4, TempPrefC: 18, TempMaxC: 28, DielMask: 0b1111,
		DietJSON:    `{"worm":1,"plant":1,"fish":1}`,
		WeatherJSON: `{"clear":1,"cloudy":1,"rain":1}`,
		ActionJSON:  `{}`, Aggression: 1, Enabled: true,
	}
	switch {
	case has("shark", "katran", "lamna", "cetorhinus", "somniosus", "tuna", "thynnus"):
		p.LayerMin, p.LayerPref, p.LayerMax = .15, .5, .95
		p.Aggression = 1.45
		p.DietJSON = `{"fish":1.6,"lure":1.5}`
		p.ActionJSON = `{"steady":1.15,"twitch":1.2,"jerk":1.2,"hard_twitch":1.2}`
	case has("catfish", "sturgeon", "beluga", "kaluga", "sterlet", "osetr", "burbot", "eel", "lamprey", "flounder", "kambala", "halibut", "cod"):
		p.LayerMin, p.LayerPref, p.LayerMax = .45, .9, 1
		p.Aggression = 1.15
		p.DietJSON = `{"fish":1.5,"worm":1.1}`
		p.ActionJSON = `{"bottom":1.25,"bottom_move":1.22,"jig":1.2,"steady":1.05}`
	case has("carp", "bream", "tench", "barbel", "buffalo", "crucian", "roach", "rudd", "dace", "chub", "gudgeon", "bleak"):
		p.LayerMin, p.LayerPref, p.LayerMax = .35, .8, 1
		p.DietJSON = `{"plant":1.4,"worm":1.2,"boilie":1.3}`
		p.ActionJSON = `{"bottom":1.25,"bottom_contact":1.15,"bottom_move":1.12,"sinking":1.08}`
	case has("salmon", "trout", "char", "taimen", "lenok", "nelma", "omul", "muksun", "smelt", "vendace"):
		p.LayerMin, p.LayerPref, p.LayerMax = .1, .42, .9
		p.Aggression = 1.2
		p.DietJSON = `{"worm":1.1,"fish":1.2,"lure":1.3}`
		p.ActionJSON = `{"steady":1.2,"trolling":1.2,"twitch":1.15,"sinking":1.05}`
	case has("pike", "perch", "zander", "walleye", "asp", "bass", "barsch", "orfe"):
		p.LayerMin, p.LayerPref, p.LayerMax = .08, .45, .95
		p.Aggression = 1.3
		p.DietJSON = `{"fish":1.4,"worm":1.1,"lure":1.35}`
		p.ActionJSON = `{"twitch":1.3,"jerk":1.35,"hard_twitch":1.35,"stop_go":1.2,"jig":1.25,"walk_dog":1.15,"steady":1.1}`
	case has("herring", "mackerel", "makrel", "shad", "sabrefish", "smelt"):
		p.LayerMin, p.LayerPref, p.LayerMax = .05, .38, .8
		p.Aggression = 1.05
		p.DietJSON = `{"plankton":1.3,"worm":1,"fish":1.1}`
		p.ActionJSON = `{"steady":1.2,"trolling":1.15,"drift":1.1,"twitch":1.1,"stop_go":1.05}`
	}
	if low, high, known := fishWeatherTemperatureBand(key); known {
		p.TempMinC, p.TempMaxC = low, high
		p.TempPrefC = (low + high) / 2
	}
	return p
}

func (s *Server) fishEcology(ctx context.Context, key string) fishEcologyProfile {
	if s == nil || s.DB == nil {
		return defaultFishEcology(key)
	}
	var p fishEcologyProfile
	var enabled int
	err := s.DB.SQL.QueryRowContext(ctx, `
		SELECT fish_key, layer_min, layer_pref, layer_max,
		       temp_min_c, temp_pref_c, temp_max_c, diel_mask,
		       diet_json, weather_json, action_json, aggression, enabled
		FROM fish_ecology WHERE fish_key=?`, key).Scan(
		&p.FishKey, &p.LayerMin, &p.LayerPref, &p.LayerMax,
		&p.TempMinC, &p.TempPrefC, &p.TempMaxC, &p.DielMask,
		&p.DietJSON, &p.WeatherJSON, &p.ActionJSON, &p.Aggression, &enabled)
	if err != nil {
		return defaultFishEcology(key)
	}
	p.Enabled = enabled != 0
	return p
}

func normalizeWaterLayer(v float64) float64 {
	return math.Max(0, math.Min(1, v))
}

func (s *Server) rigWaterLayer(c fishRollContext) float64 {
	if bottomRigTypes[c.rigType] {
		return .93
	}
	if rigTypeLureFamily[c.rigType] {
		if c.lureLayer > 0 {
			return normalizeWaterLayer(c.lureLayer)
		}
		return .5
	}
	if c.depthCM > 0 {
		return normalizeWaterLayer(float64(c.depthCM) / 500)
	}
	return .45
}

// fishLayerWeight 按当前钓法水层与鱼种范围计算软匹配权重。
// 底层杆对明显不属于底层水层的鱼保留极小杂口，而不是绝对删除。
func (s *Server) fishLayerWeight(ctx context.Context, key string, c fishRollContext) float64 {
	p := s.fishEcology(ctx, key)
	if !p.Enabled {
		return 0
	}
	target := s.rigWaterLayer(c)
	mid := p.LayerPref
	half := math.Max(.08, (p.LayerMax-p.LayerMin)/2)
	z := (target - mid) / half
	factor := .12 + .88*math.Exp(-.5*z*z)
	if target >= p.LayerMin-.08 && target <= p.LayerMax+.08 {
		factor = math.Max(factor, .75)
	}
	if bottomRigTypes[c.rigType] && p.LayerMax < .55 {
		factor *= .05
	}
	if rigTypeLureFamily[c.rigType] && target > p.LayerMax+.15 {
		factor *= .2
	}
	return math.Max(.005, factor)
}

func (s *Server) fishBiteEnvironmentWeight(ctx context.Context, key string, c fishRollContext) float64 {
	if c.rigType == 0 && c.depthCM == 0 && c.lureLayer == 0 {
		return 1
	}
	p := s.fishEcology(ctx, key)
	if !p.Enabled {
		return 0
	}
	factor := s.fishLayerWeight(ctx, key, c)
	_, hour := worldDayAndHour(c.now.Unix())
	factor *= fishDielWeight(p, hour)
	if c.weather.known && finiteWeatherValue(c.weather.airTemp) {
		// 初版水温由气温和水层估算；后续天气阶段会加入热惯性和季节。
		layer := s.rigWaterLayer(c)
		waterTemp := c.weather.airTemp - 1.5 + (layer-.5)*1.2
		tolerance := math.Max(2.5, (p.TempMaxC-p.TempMinC)/2)
		z := (waterTemp - p.TempPrefC) / tolerance
		thermal := .35 + .65*math.Exp(-.5*z*z)
		factor *= thermal
	}
	if c.lureAction != "" {
		if weights := fishEcologyJSONMap(p.ActionJSON); len(weights) > 0 {
			if weight, ok := fishEcologyActionWeight(weights, c.lureAction); ok {
				factor *= math.Max(.05, weight)
			}
		}
	}
	return math.Max(.001, factor)
}

// activityForDiel 将游戏小时映射到夜、晨、日、昏四段。
func fishActivityForHour(hour int64) float64 {
	hour = (hour%24 + 24) % 24
	switch {
	case hour >= 5 && hour < 9:
		return 1
	case hour >= 9 && hour < 16:
		return 2
	case hour >= 16 && hour < 20:
		return 3
	default:
		return 0
	}
}

func fishDielWeight(profile fishEcologyProfile, hour int64) float64 {
	bit := uint(fishActivityForHour(hour))
	if profile.DielMask&(1<<bit) != 0 {
		return 1
	}
	return .35
}

func fishEcologyJSONMap(raw string) map[string]float64 {
	var m map[string]float64
	_ = json.Unmarshal([]byte(raw), &m)
	return m
}

// lureMotionStatusMask 覆盖客户端 Gmkegncajem 已知的 0..22 位动作状态。
// 0xD0000000/0xD1000000 的高位是钓组阶段，不属于拟饵动作。
const lureMotionStatusMask uint32 = 0x007FFFFF

// classifyLureMotion 映射客户端 kRigAnimationType_* 状态位。优先级按
// “瞬时主动作 > 水层姿态 > 收线兜底”排列，避免 0x400 收线盖住抽、跳等动作。
func classifyLureMotion(mask uint32) string {
	mask &= lureMotionStatusMask
	switch {
	case mask&0x20000 != 0:
		return "hard_twitch"
	case mask&0x10000 != 0:
		return "stop_go"
	case mask&0x10 != 0:
		return "twitch"
	case mask&0x80 != 0:
		return "jig"
	case mask&0x100 != 0:
		return "walk_dog"
	case mask&0x400000 != 0:
		return "bottom_layer_move"
	case mask&0x4000 != 0:
		return "trolling"
	case mask&0x2000 != 0:
		return "bottom_move"
	case mask&0x20 != 0:
		return "splash"
	case mask&0x800 != 0:
		return "bottom"
	case mask&0x200 != 0:
		return "bottom_hang"
	case mask&0x1000 != 0:
		return "surface"
	case mask&0x40 != 0:
		return "drift"
	case mask&0x8 != 0:
		return "bottom_contact"
	case mask&0x4 != 0:
		return "jerk"
	case mask&0x2 != 0:
		return "speed_up"
	case mask&0x80000 != 0:
		return "sinking"
	case mask&0x200000 != 0:
		return "vertical_rise"
	case mask&0x400 != 0:
		return "steady"
	case mask&0x1 != 0:
		return "stir"
	default:
		return ""
	}
}

// fishEcologyActionWeight 兼容旧 fish_ecology.action_json 键。已有数据库行
// 不会被 INSERT OR IGNORE 更新，因此新细分动作必须能回退到旧粗分类。
func fishEcologyActionWeight(weights map[string]float64, action string) (float64, bool) {
	if weight, ok := weights[action]; ok {
		return weight, true
	}
	var candidates []string
	switch action {
	case "steady", "speed_up", "trolling", "drift", "twitch", "jerk", "hard_twitch", "stop_go":
		candidates = []string{"moving"}
	case "bottom", "bottom_contact", "bottom_hang":
		candidates = []string{"bottom_motionless"}
	case "bottom_move", "bottom_layer_move":
		candidates = []string{"bottom_motionless", "moving"}
	case "jig", "walk_dog":
		candidates = []string{"moving", "bottom_motionless"}
	case "surface", "splash":
		candidates = []string{"surface_motionless", "moving"}
	case "sinking", "vertical_rise":
		candidates = []string{"water_column_motionless", "moving"}
	case "stir":
		candidates = []string{"moving", "water_column_motionless"}
	}
	for _, candidate := range candidates {
		if weight, ok := weights[candidate]; ok {
			return weight, true
		}
	}
	return 0, false
}

func lureLayerFromMotion(mask uint32, previous float64) float64 {
	if previous <= 0 {
		previous = .5
	}
	mask &= lureMotionStatusMask
	switch {
	case mask&0x1000 != 0:
		return .08
	case mask&0x80000 != 0:
		return normalizeWaterLayer(previous + .08)
	case mask&0x200000 != 0:
		return normalizeWaterLayer(previous - .08)
	case mask&0x2000 != 0, mask&0x400000 != 0:
		return .94
	case mask&0x800 != 0:
		return .92
	case mask&0x200 != 0:
		return .85
	case mask&0x400 != 0:
		return normalizeWaterLayer(previous + .03)
	default:
		return normalizeWaterLayer(previous)
	}
}
