package gameserver

// 玩家状态条（308 f0/f1/f3/f4）服务端权威状态机。
//
// 客户端四条状态条为纯显示组件，只做约 10 秒的平滑过渡，
// 不做任何衰减/消耗计算（协议基线 4.0.25034 实测）。
// 机制依据与证据等级见 specs/changes/player-condition-core/spec.md；
// 全部速率与消耗量是标定值（tunable），不是官服常量。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

// tunables 键名。改值走 tunables 表，30 秒缓存生效，无需重启。
const (
	tunableConditionEnabled     = "player_condition_enabled"
	tunableHungerDecayPerHour   = "condition_hunger_decay_per_hour"
	tunableEnergyRegenPerHour   = "condition_energy_regen_per_hour"
	tunableEnergyMax            = "condition_energy_max"
	tunableHungerRegenFloor     = "condition_hunger_regen_floor"
	tunableCastEnergyCost       = "condition_cast_energy_cost"
	tunablePullEnergyCost       = "condition_pull_energy_cost"
	tunableLowEnergyThreshold   = "condition_low_energy_threshold"
	tunableMaxBackfillHours     = "condition_max_backfill_hours"
	tunableFoodHungerRestore    = "condition_food_hunger_restore"
	tunableCoffeeEnergyRestore  = "condition_coffee_energy_restore"
	tunableCocoaHungerRestore   = "condition_cocoa_hunger_restore"
	tunableCocoaEnergyRestore   = "condition_cocoa_energy_restore"
	tunableTeaComfortRestore    = "condition_tea_comfort_restore"
	tunableComfortRainPenalty   = "condition_comfort_rain_penalty"
	tunableComfortColdPenalty   = "condition_comfort_cold_penalty"
	tunableComfortColdTempC     = "condition_comfort_cold_temp_c"
	tunableComfortCampfireBoost = "condition_comfort_campfire_boost"
	tunableComfortSunBoost      = "condition_comfort_sun_boost"
	tunableComfortDriftPerHour  = "condition_comfort_drift_per_hour"
	tunableSunEnergyRegenMult   = "condition_sun_energy_regen_mult"
	tunableGearEnergyRegenCap   = "condition_gear_energy_regen_cap"
	tunableGearEnergyMaxCap     = "condition_gear_energy_max_cap"
)

// 缺省标定值：
// 饱食衰减 0.24/h 来自抓包（~0.0019/30s）；精力静止上限 0.85 来自抓包；
// 精力恢复 2.0/h 是抓包"15 秒恢复 0.0086"的量级下界；其余为占位标定。
const (
	defHungerDecayPerHour = 0.24
	defEnergyRegenPerHour = 2.0
	defEnergyMax          = store.DefaultPlayerConditionPrimary
	defHungerRegenFloor   = 0.25
	defCastEnergyCost     = 0.01
	defPullEnergyCost     = 0.005
	defLowEnergyThreshold = 0.05
	defMaxBackfillHours   = 12

	// 消耗品恢复量（饮用/食用一次的即时恢复，全部为标定值）。
	defFoodHungerRestore   = 0.25
	defCoffeeEnergyRestore = 0.15
	defCocoaHungerRestore  = 0.35
	defCocoaEnergyRestore  = 0.05
	defTeaComfortRestore   = 0.30

	// 舒适度环境均衡点修正与漂移速率（标定值，无官服样本）。
	defComfortRainPenalty   = 0.35
	defComfortColdPenalty   = 0.35
	defComfortColdTempC     = 5
	defComfortCampfireBoost = 0.5
	defComfortSunBoost      = 0.1
	defComfortDriftPerHour  = 0.5
	defSunEnergyRegenMult   = 1.25

	// 装备被动加成封顶。
	defGearEnergyRegenCap = 0.5
	defGearEnergyMaxCap   = 0.15
)

// conditionWriteBackDelta：读路径推进后，变化量低于该阈值不写回，
// 避免每 5 秒保活都对全量在线玩家做一次无效写库。0.0005 远低于
// float32 在 0.85 量级的显示噪声。
const conditionWriteBackDelta = 0.0005

// operationResultLowEnergyLevel 官服操作结果码 84：精力不足
// （kOperationResult_LowEnergyLevel），客户端弹对应文案。
const operationResultLowEnergyLevel = 84

type conditionParams struct {
	Enabled              bool
	HungerDecayPerHour   float64
	EnergyRegenPerHour   float64
	EnergyMax            float64
	HungerRegenFloor     float64
	CastEnergyCost       float64
	PullEnergyCost       float64
	LowEnergyThreshold   float64
	MaxBackfillHours     float64
	FoodHungerRestore    float64
	CoffeeEnergyRestore  float64
	CocoaHungerRestore   float64
	CocoaEnergyRestore   float64
	TeaComfortRestore    float64
	ComfortRainPenalty   float64
	ComfortColdPenalty   float64
	ComfortColdTempC     float64
	ComfortCampfireBoost float64
	ComfortSunBoost      float64
	ComfortDriftPerHour  float64
	SunEnergyRegenMult   float64
	GearEnergyRegenCap   float64
	GearEnergyMaxCap     float64
}

func (s *Server) conditionParams() conditionParams {
	return conditionParams{
		Enabled:              s.tunableF(tunableConditionEnabled, 1) >= 0.5,
		HungerDecayPerHour:   s.tunableF(tunableHungerDecayPerHour, defHungerDecayPerHour),
		EnergyRegenPerHour:   s.tunableF(tunableEnergyRegenPerHour, defEnergyRegenPerHour),
		EnergyMax:            s.tunableF(tunableEnergyMax, defEnergyMax),
		HungerRegenFloor:     s.tunableF(tunableHungerRegenFloor, defHungerRegenFloor),
		CastEnergyCost:       s.tunableF(tunableCastEnergyCost, defCastEnergyCost),
		PullEnergyCost:       s.tunableF(tunablePullEnergyCost, defPullEnergyCost),
		LowEnergyThreshold:   s.tunableF(tunableLowEnergyThreshold, defLowEnergyThreshold),
		MaxBackfillHours:     s.tunableF(tunableMaxBackfillHours, defMaxBackfillHours),
		FoodHungerRestore:    s.tunableF(tunableFoodHungerRestore, defFoodHungerRestore),
		CoffeeEnergyRestore:  s.tunableF(tunableCoffeeEnergyRestore, defCoffeeEnergyRestore),
		CocoaHungerRestore:   s.tunableF(tunableCocoaHungerRestore, defCocoaHungerRestore),
		CocoaEnergyRestore:   s.tunableF(tunableCocoaEnergyRestore, defCocoaEnergyRestore),
		TeaComfortRestore:    s.tunableF(tunableTeaComfortRestore, defTeaComfortRestore),
		ComfortRainPenalty:   s.tunableF(tunableComfortRainPenalty, defComfortRainPenalty),
		ComfortColdPenalty:   s.tunableF(tunableComfortColdPenalty, defComfortColdPenalty),
		ComfortColdTempC:     s.tunableF(tunableComfortColdTempC, defComfortColdTempC),
		ComfortCampfireBoost: s.tunableF(tunableComfortCampfireBoost, defComfortCampfireBoost),
		ComfortSunBoost:      s.tunableF(tunableComfortSunBoost, defComfortSunBoost),
		ComfortDriftPerHour:  s.tunableF(tunableComfortDriftPerHour, defComfortDriftPerHour),
		SunEnergyRegenMult:   s.tunableF(tunableSunEnergyRegenMult, defSunEnergyRegenMult),
		GearEnergyRegenCap:   s.tunableF(tunableGearEnergyRegenCap, defGearEnergyRegenCap),
		GearEnergyMaxCap:     s.tunableF(tunableGearEnergyMaxCap, defGearEnergyMaxCap),
	}
}

// conditionEnv 环境快照：舒适度均衡点与晒阳精力恢复的输入。
// 由会话侧解析天气/昼夜/篝火，推进函数只消费布尔结果；
// 零值表示"环境未知"，均衡点回到 1.0（与无环境联动时行为一致）。
type conditionEnv struct {
	Rain     bool // 降雨或雷暴
	Cold     bool // 气温低于阈值
	Sun      bool // 白昼且无雨（晒太阳）
	Campfire bool // 有未熄灭的篝火
}

// advancePlayerCondition 按时间差推进精力、饱食度与舒适度；纯函数，单测用手算样例覆盖。
//
// 只用于在线钟口径：登录/重连时 resumePlayerConditionClock 冻结离线跨度，
// 因此这里收到的时间差都来自在线会话；MaxBackfillHours 只是会话钟停滞的安全阀。
//   - 饱食度随时间衰减，到 0 为止；离线停扣（维护者决策，见 spec）。
//   - 精力按"基础速率 × 饱食因子"恢复（官方手册：恢复速度取决于饱食度和舒适度），
//     封顶 energy_max；晒太阳时乘以加成倍率；离线同样停恢复。
//   - 舒适度向环境均衡点漂移：1 − 降雨惩罚 − 寒冷惩罚 + 篝火加成 + 晒阳加成，
//     速率 ComfortDriftPerHour。
//   - luck_penalty 无动态写入者，原样透传。
func advancePlayerCondition(cur store.PlayerCondition, now float64, p conditionParams, env conditionEnv) store.PlayerCondition {
	out := cur
	out.UpdatedTS = now
	if !p.Enabled || now <= cur.UpdatedTS {
		return out
	}
	elapsedH := (now - cur.UpdatedTS) / 3600
	if elapsedH > p.MaxBackfillHours {
		elapsedH = p.MaxBackfillHours
	}
	out.Secondary = clampFloat(cur.Secondary-p.HungerDecayPerHour*elapsedH, 0, 1)
	regen := p.EnergyRegenPerHour
	if env.Sun {
		regen *= p.SunEnergyRegenMult
	}
	factor := p.HungerRegenFloor + (1-p.HungerRegenFloor)*out.Secondary
	out.Primary = clampFloat(cur.Primary+regen*factor*elapsedH, 0, p.EnergyMax)
	equilibrium := 1.0
	if env.Rain {
		equilibrium -= p.ComfortRainPenalty
	}
	if env.Cold {
		equilibrium -= p.ComfortColdPenalty
	}
	if env.Campfire {
		equilibrium += p.ComfortCampfireBoost
	}
	if env.Sun {
		equilibrium += p.ComfortSunBoost
	}
	equilibrium = clampFloat(equilibrium, 0, 1)
	drift := p.ComfortDriftPerHour * elapsedH
	out.Comfort = clampFloat(out.Comfort+clampFloat(equilibrium-out.Comfort, -drift, drift), 0, 1)
	return out
}

// resumePlayerConditionClock 登录/重连时冻结状态钟：把 updated_ts 推到当前时刻
// 但不做任何衰减/恢复，从而吞掉离线跨度——离线期间饱食度停扣、精力停恢复。
// （维护者决策，非官服验证口径；与窝料/饮酒在线钟同一范式。）
func (s *Server) resumePlayerConditionClock(ctx context.Context, uid int64) {
	if s == nil || s.DB == nil {
		return
	}
	if !s.conditionParams().Enabled {
		return
	}
	cur, err := s.DB.EnsurePlayerCondition(ctx, uid)
	if err != nil || cur == nil {
		return
	}
	now := float64(time.Now().Unix())
	if cur.UpdatedTS >= now {
		return
	}
	cur.UpdatedTS = now
	if err := s.DB.UpsertPlayerCondition(ctx, *cur); err != nil {
		s.log.Warn("状态钟冻结失败", "uid", uid, "err", err)
	}
}

// currentPlayerCondition 懒计算读取口：Ensure → 推进 → 变化超阈值才写回。
// 总开关关闭时回退协议默认常量，与改动前行为逐字段一致（不写库）。
func (s *Server) currentPlayerCondition(ctx context.Context, uid int64) store.PlayerCondition {
	return s.currentPlayerConditionP(ctx, uid, s.conditionParams(), conditionEnv{})
}

// currentPlayerConditionEnv 带环境快照的读取口（2/2 保活专用）：
// 舒适度向环境均衡点漂移、晒阳精力加成在这里生效。
func (s *Server) currentPlayerConditionEnv(ctx context.Context, uid int64, env conditionEnv) store.PlayerCondition {
	return s.currentPlayerConditionP(ctx, uid, s.conditionParams(), env)
}

func (s *Server) currentPlayerConditionP(ctx context.Context, uid int64, p conditionParams, env conditionEnv) store.PlayerCondition {
	now := float64(time.Now().Unix())
	def := store.PlayerCondition{
		Primary:     store.DefaultPlayerConditionPrimary,
		Secondary:   store.DefaultPlayerConditionSecondary,
		Comfort:     store.DefaultPlayerConditionComfort,
		LuckPenalty: store.DefaultPlayerConditionLuckPenalty,
		UpdatedTS:   now,
	}
	if s == nil || s.DB == nil {
		return def
	}
	cur, err := s.DB.EnsurePlayerCondition(ctx, uid)
	if err != nil || cur == nil {
		return def
	}
	if !p.Enabled {
		return def
	}
	next := advancePlayerCondition(*cur, now, p, env)
	if math.Abs(next.Primary-cur.Primary) > conditionWriteBackDelta ||
		math.Abs(next.Secondary-cur.Secondary) > conditionWriteBackDelta ||
		math.Abs(next.Comfort-cur.Comfort) > conditionWriteBackDelta {
		// 写回失败不阻断下发：本次返回推进值，下次读取再补写。
		if err := s.DB.UpsertPlayerCondition(ctx, next); err != nil {
			s.log.Warn("玩家状态写回失败", "uid", uid, "err", err)
		}
	}
	return next
}

// conditionAllowsCast 抛投精力闸门：低于阈值时 14/2 以结果码 84 拒绝。
func (s *Server) conditionAllowsCast(ctx context.Context, uid int64) bool {
	p := s.conditionParams()
	if !p.Enabled {
		return true
	}
	return s.currentPlayerCondition(ctx, uid).Primary > p.LowEnergyThreshold
}

// spendEnergy 立即扣减精力并写回。动作写入总是落库（不受读路径阈值约束）。
// 总开关关闭时不产生任何状态变化。
func (s *Server) spendEnergy(ctx context.Context, uid int64, amount float64) {
	if s == nil || s.DB == nil || amount <= 0 {
		return
	}
	p := s.conditionParams()
	if !p.Enabled {
		return
	}
	c := s.currentPlayerCondition(ctx, uid)
	c.Primary = clampFloat(c.Primary-amount, 0, p.EnergyMax)
	c.UpdatedTS = float64(time.Now().Unix())
	if err := s.DB.UpsertPlayerCondition(ctx, c); err != nil {
		s.log.Warn("精力扣减写回失败", "uid", uid, "err", err)
	}
}

// 日出/日落取游戏时刻 6:00/20:00。世界钟 24 倍速（一游戏日 = 一现实小时），
// 昼夜随现实时间一小时轮转；漂移速率远慢于轮转，表现为轻微起伏。
func isDaylightGameMinute(minute uint32) bool {
	return minute >= 6*60 && minute < 20*60
}

// conditionEnvForSession 解析玩家当前环境（天气/昼夜/篝火）。
// 天气计划或位置未知时相应位为 false，均衡点回到 1.0。
func (s *Session) conditionEnvForSession(now time.Time, p conditionParams) conditionEnv {
	var env conditionEnv
	if s == nil || s.Srv == nil || s.Srv.DB == nil {
		return env
	}
	st := s.Srv.stateFor(s.Userid)
	st.mu.Lock()
	loc := st.Loc
	st.mu.Unlock()
	if loc != "" {
		if w := s.weatherAt(loc, now); w.known {
			env.Rain = w.kind >= fishingWeatherRain
			env.Cold = w.airTemp < p.ComfortColdTempC
			env.Sun = !env.Rain && isDaylightGameMinute(gameMinuteOfDayAt(now))
		}
	} else {
		env.Sun = isDaylightGameMinute(gameMinuteOfDayAt(now))
	}
	if state, err := s.Srv.DB.GetCampfireState(s.pushCtx(), s.Userid); err == nil && state != nil {
		env.Campfire = state.ExpiresFT > int64(proto.WindowsFiletimeNow())
	}
	return env
}

// rigEnergyBonuses 汇总已装配竿/轮的精力被动加成。数据源为 item_def.attrs_json
// 的 oiicogcccao（精力恢复加成）与 oodjilnjeen（精力上限加成），0..1 比例，
// 与客户端 kItemDesc_Bonus_Energy* tooltip 同键；当前静态库未携带这些值时恒为 0，
// 标定写入 attrs 后客户端显示与服务端效果同源生效。总和受 tunables 封顶。
func (s *Server) rigEnergyBonuses(ctx context.Context, uid int64, rodGUID string) (regen, maxAdd float64) {
	if s == nil || s.DB == nil || rodGUID == "" {
		return 0, 0
	}
	p := s.conditionParams()
	guids := []string{rodGUID}
	if ids := s.rigGearIDs(ctx, uid, rodGUID); ids.ReelGUID != "" {
		guids = append(guids, ids.ReelGUID)
	}
	for _, guid := range guids {
		row, err := s.DB.GetItemByGUID(ctx, guid)
		if err != nil || row == nil {
			continue
		}
		def, err := s.itemDefForRow(ctx, row)
		if err != nil || def == nil || def.AttrsJSON == "" {
			continue
		}
		var attrs map[string]any
		if json.Unmarshal([]byte(def.AttrsJSON), &attrs) != nil {
			continue
		}
		regen += jsonFloatAttr(attrs, "oiicogcccao")
		maxAdd += jsonFloatAttr(attrs, "oodjilnjeen")
	}
	return clampFloat(regen, 0, p.GearEnergyRegenCap), clampFloat(maxAdd, 0, p.GearEnergyMaxCap)
}

func jsonFloatAttr(attrs map[string]any, key string) float64 {
	v, ok := attrs[key]
	if !ok {
		return 0
	}
	if n, ok := v.(float64); ok {
		return n
	}
	return 0
}

// buildPose308Env 带环境与装备加成的 308 构建（2/2 保活专用）：
// 装备加成调整精力恢复速率与上限后按环境推进。
func (s *Server) buildPose308Env(ctx context.Context, uid int64, env conditionEnv) *proto.Value {
	p := s.conditionParams()
	if p.Enabled {
		if regen, maxAdd := s.rigEnergyBonuses(ctx, uid, s.heldRodGUID(uid)); regen != 0 || maxAdd != 0 {
			p.EnergyRegenPerHour *= 1 + regen
			p.EnergyMax = clampFloat(p.EnergyMax+maxAdd, 0, 1)
		}
	}
	c := s.currentPlayerConditionP(ctx, uid, p, env)
	return wiregen.MustMakeObj("pnbphpdmppe", map[int]*proto.Value{
		0: proto.F32(float32(c.Primary)), 1: proto.F32(float32(c.Secondary)), 2: proto.F32(0.0),
		3: proto.F32(float32(c.Comfort)), 4: proto.F32(float32(c.LuckPenalty)),
		5: proto.F32(11.375), 6: proto.F32(0.0),
		7: proto.U64(uint64(proto.WindowsFiletimeNow())),
		8: proto.U32(0),
	})
}

func (s *Server) heldRodGUID(uid int64) string {
	st := s.stateFor(uid)
	st.mu.Lock()
	guid := st.GearGuid
	st.mu.Unlock()
	return guid
}

// applyConsumableRestoreTx 在经济事务内应用食品/热饮的即时状态恢复。
// 分类映射见 consumableRestoreForItem：咖啡→精力、茶→舒适度、
// 可可→饱食+少量精力、其余食品→饱食度。恢复量全部为 tunables 标定值。
func applyConsumableRestoreTx(ctx context.Context, tx *sql.Tx, uid int64, kind string, p conditionParams, now time.Time) error {
	cur, err := store.EnsurePlayerConditionTx(ctx, tx, uid)
	if err != nil {
		return err
	}
	next := *cur
	switch kind {
	case "coffee":
		next.Primary = clampFloat(next.Primary+p.CoffeeEnergyRestore, 0, p.EnergyMax)
	case "tea":
		next.Comfort = clampFloat(next.Comfort+p.TeaComfortRestore, 0, 1)
	case "cocoa":
		next.Secondary = clampFloat(next.Secondary+p.CocoaHungerRestore, 0, 1)
		next.Primary = clampFloat(next.Primary+p.CocoaEnergyRestore, 0, p.EnergyMax)
	default: // "food"
		next.Secondary = clampFloat(next.Secondary+p.FoodHungerRestore, 0, 1)
	}
	next.UpdatedTS = float64(now.Unix())
	return store.UpsertPlayerConditionTx(ctx, tx, next)
}

// conditionFreeMealKV 免费餐冷却戳（kv 表，值为世界钟游戏日号）。
const conditionFreeMealKV = "condition_free_meal:%d"

// operationResultFoodAidOnceADay 官服结果码 16：
// kOperationResult_FoodAidAvailableOnceADay。
const operationResultFoodAidOnceADay = 16

// gameDayNumberAt 世界钟游戏日号（24 倍速，1 游戏日 = 1 现实小时）。
func gameDayNumberAt(now time.Time) int64 {
	return int64(float64(now.Unix()-worldDayEpochUnix) * gameMinutesPerRealSecond / gameMinutesPerDay)
}

// hKitchenEat 处理 3/13 player/kitchen_eat：客户端在野外厨房/餐车物件上确认进食。
//
// 请求只携带厨房物件 ID，不携带食物实例；
// 官服由服务端厨房食材账本扣减，本服未建模食材账本，故按"免费餐车"语义实现：
// 每游戏日限一次免费餐（1 游戏日 = 1 现实小时），冷却中回官服结果码 16。
// 恢复对齐抓包：进食后下一条 2/2 饱食度回到 1.0。成功响应回 null（与官服
// resp-3-13 模板一致）；幂等由通用请求回执表兜底。
func hKitchenEat(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	p := srv.conditionParams()
	if !p.Enabled {
		return []byte{0x00}, true
	}
	cctx, uid := ctx.Sess.pushCtx(), ctx.Sess.Userid
	// 官服口径：免费餐每游戏日一次（一天=游戏时间，见 Further Notes [决策]）；
	// 冷却中回官服结果码 16，客户端弹 kOperationResult_FoodAidAvailableOnceADay 文案。
	gameDay := gameDayNumberAt(time.Now())
	key := fmt.Sprintf(conditionFreeMealKV, uid)
	var last string
	if err := srv.DB.SQL.QueryRowContext(cctx, `SELECT v FROM kv WHERE k=?`, key).Scan(&last); err == nil {
		if lastDay, perr := strconv.ParseInt(last, 10, 64); perr == nil && lastDay == gameDay {
			ctx.Log.Info("免费餐本游戏日已用，拒绝", "uid", uid, "game_day", gameDay)
			ctx.ReplyOperationResult(operationResultFoodAidOnceADay)
			return nil, true
		}
	}
	next := srv.currentPlayerCondition(cctx, uid)
	next.Secondary = 1.0
	next.UpdatedTS = float64(time.Now().Unix())
	if err := srv.DB.UpsertPlayerCondition(cctx, next); err != nil {
		ctx.Log.Warn("进食恢复饱食度失败", "uid", uid, "err", err)
		ctx.ReplyFailure("System.InvalidOperationException", "进食未完成，请重试")
		return nil, true
	}
	if _, err := srv.DB.SQL.ExecContext(cctx, `
		INSERT INTO kv(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`,
		key, strconv.FormatInt(gameDay, 10)); err != nil {
		ctx.Log.Warn("免费餐冷却戳写入失败", "uid", uid, "err", err)
	}
	if kitchen := argFirstStr(ctx.Tree); kitchen != "" {
		ctx.Log.Info("玩家进食", "uid", uid, "kitchen", kitchen)
	} else {
		ctx.Log.Info("玩家进食", "uid", uid)
	}
	// 立即补推一次 2/2，让饱食度条在客户端平滑回升。
	ctx.AfterResponse(func() { pushKeepalive(ctx.Sess) })
	return []byte{0x00}, true
}
