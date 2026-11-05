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
		p.ActionJSON = `{"moving":1.2,"water_column_motionless":1.05}`
	case has("catfish", "sturgeon", "beluga", "kaluga", "sterlet", "osetr", "burbot", "eel", "lamprey", "flounder", "kambala", "halibut", "cod"):
		p.LayerMin, p.LayerPref, p.LayerMax = .45, .9, 1
		p.Aggression = 1.15
		p.DietJSON = `{"fish":1.5,"worm":1.1}`
		p.ActionJSON = `{"bottom_motionless":1.25,"moving":1.08}`
	case has("carp", "bream", "tench", "barbel", "buffalo", "crucian", "roach", "rudd", "dace", "chub", "gudgeon", "bleak"):
		p.LayerMin, p.LayerPref, p.LayerMax = .35, .8, 1
		p.DietJSON = `{"plant":1.4,"worm":1.2,"boilie":1.3}`
		p.ActionJSON = `{"bottom_motionless":1.25,"water_column_motionless":1.12}`
	case has("salmon", "trout", "char", "taimen", "lenok", "nelma", "omul", "muksun", "smelt", "vendace"):
		p.LayerMin, p.LayerPref, p.LayerMax = .1, .42, .9
		p.Aggression = 1.2
		p.DietJSON = `{"worm":1.1,"fish":1.2,"lure":1.3}`
		p.ActionJSON = `{"moving":1.2,"surface_motionless":1.05}`
	case has("pike", "perch", "zander", "walleye", "asp", "bass", "barsch", "orfe"):
		p.LayerMin, p.LayerPref, p.LayerMax = .08, .45, .95
		p.Aggression = 1.3
		p.DietJSON = `{"fish":1.4,"worm":1.1,"lure":1.35}`
		p.ActionJSON = `{"moving":1.3,"water_column_motionless":1.1,"surface_motionless":1.15}`
	case has("herring", "mackerel", "makrel", "shad", "sabrefish", "smelt"):
		p.LayerMin, p.LayerPref, p.LayerMax = .05, .38, .8
		p.Aggression = 1.05
		p.DietJSON = `{"plankton":1.3,"worm":1,"fish":1.1}`
		p.ActionJSON = `{"moving":1.2,"surface_motionless":1.08}`
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
			if weight, ok := weights[c.lureAction]; ok {
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

// classifyLureMotion 只标记协议位已经确认的粗状态。完整 31 种运动模式仍需
// 结合客户端动作抓包逐位映射，原始 f5 保存在 LureMotionMask 供后续校准。
func classifyLureMotion(mask uint32) string {
	switch {
	case mask&0x1000 != 0:
		return "surface_motionless"
	case mask&0x800 != 0:
		return "bottom_motionless"
	case mask&0x200 != 0:
		return "water_column_motionless"
	case mask&0x400 != 0:
		return "moving"
	default:
		return ""
	}
}

func lureLayerFromMotion(mask uint32, previous float64) float64 {
	if previous <= 0 {
		previous = .5
	}
	switch {
	case mask&0x1000 != 0:
		return .08
	case mask&0x800 != 0:
		return .92
	case mask&0x200 != 0:
		return .48
	case mask&0x400 != 0:
		return normalizeWaterLayer(previous + .03)
	default:
		return normalizeWaterLayer(previous)
	}
}
