package gameserver

// 搏鱼期间的渔轮磨损结算。
//
// 客户端力度条顶满只等于"顶到此刻张力上限"，并不是超过上限，所以爆不爆件
// 客户端永远说不出口（实机满红几十秒也不报断件）。代价只能由服务端结算：
// 未锁轮且鱼拉力高于当前泄力 → 泄力打滑磨摩擦片；锁轮且鱼拉力高于此刻承力 →
// 按发热曲线烧摩擦片，解锁散热、下次锁轮从曲线开头重新慢慢爬。
// 节拍跟着客户端 14/8 负载上报：实测搏鱼期间每 1.8~2.0 秒一条，一条算一次判定。
// 过载秒数按服务端收到 14/8 的真实间隔累加，不按“一条上报 = 固定节拍”折算：
// 实测节拍 1.8~2.0 秒只是客户端的默认节奏，帧率与卡顿都会改变它。
// 口径见 specs/changes/reel-friction-life-model/spec.md。

import (
	"context"
	"math"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

const (
	// defaultReelLoadTickSeconds 是本竿第一条 14/8 的兜底时长（tunable reel_load_tick_seconds）：
	// 还没收到过上一条上报时服务端不知道前面已经过了多久，按实测节拍 1.9 秒起算。
	defaultReelLoadTickSeconds = 1.9
	// defaultReelLoadMaxTickSeconds 是两条 14/8 之间最多折算的秒数（tunable
	// reel_load_max_tick_seconds）：客户端卡顿、切场景或掉线会把间隔拉长，超过上限
	// 就不再补记，宁可少烧一点摩擦片，也不让一条迟到上报把整段发热曲线一次烧完。
	defaultReelLoadMaxTickSeconds = 5.0
	// defaultReelBurnBaseSeconds 是校准点摩擦片连续锁轮的烧穿秒数（tunable reel_locked_burn_base_s）。
	defaultReelBurnBaseSeconds = 10.0
	// defaultReelBurnCurve 是锁轮过载的发热曲线指数（tunable reel_locked_burn_curve）：
	// 前 10% 时间只掉 3%，最后 10% 时间掉 15%。
	defaultReelBurnCurve = 1.5
	// reelBurnCalibrationDragKg 是校准点制动力，取库里摩擦片的 p25 分位。
	reelBurnCalibrationDragKg = 7.7
)

// fightReelWear 是本竿搏鱼期间的渔轮磨损账：承力参数缓存 + 累计磨损。
// 承力参数一次搏鱼只查一次库，玩家改摩擦档位即作废重取。
type fightReelWear struct {
	valid    bool
	fishGUID string
	rodGUID  string
	reelGUID string

	locked   bool
	dragKg   float64 // 当前摩擦档位的泄力（已含摩擦片剩余寿命与轴承效率）
	lockedKg float64 // 锁轮承力（此刻）
	ratedKg  float64 // 摩擦片额定制动力（含母轮工艺）
	oilWear  float64 // 轮油磨损倍率：好油 <1，差油 >1
	oilBurn  float64 // 轮油对烧穿时长的系数，含轮油自身干涸衰减
	pullKg   float64 // 服务端已知的鱼拉力（公斤力）

	SlipTicks   int
	SlipWear    float64 // 通道一：泄力打滑累计的寿命比例
	BurnSecs    float64 // 通道二：锁轮过载累计秒数（跨段，供零件应力账使用）
	BurnWear    float64 // 通道二：过载曲线累计的寿命比例
	SegSecs     float64 // 当前过载段内已持续秒数；解锁清零，下段从曲线开头重爬
	Used        bool    // 本竿是否真的下水使用过（抛过竿就算，与是否中鱼无关）
	BaseCharged bool    // 本竿的“每竿基准 + 泡水”是否已计入本账
	BreakHits   int     // 断件冲击次数：只在真断线/断竿那一次扣
	settled     bool    // 本竿是否已落库（每竿一次）
	// lastLoadAt 是上一条 14/8 到达服务端的时刻，用来把过载秒数换成真实时长；
	// loadProbe 累计负载上报里的字段读数。两者都不结算，也只是进程内状态，
	// 所以做成非导出字段，不进钓组状态的 JSON 快照。
	lastLoadAt time.Time
	loadProbe  fightLoadProbe
}

// reset 清空本竿账本（新的一杆）。
func (w *fightReelWear) reset() { *w = fightReelWear{} }

// invalidate 丢掉承力缓存（玩家改了摩擦档位），并结束当前过载段：
// 下一段从曲线开头重新慢慢爬，已经烧掉的寿命不还原。
func (w *fightReelWear) invalidate() {
	kept := *w
	kept.valid = false
	kept.SegSecs = 0
	*w = kept
}

// claimLocked 取走本竿待落库的磨损账（调用方必须持有 FishState 锁）。已落库或没有
// 磨损时返回 false，保证同一竿的账只写一次。
// claimLocked 取走本竿待落库的磨损账（调用方必须持有 FishState 锁）。已落库或
// 本竿什么都没发生时返回 false，保证同一竿的账只写一次。抛过竿就算有账：
// 即使没咬钩，轮内零件也要按使用时间结算基准与泡水。
func (w *fightReelWear) claimLocked() (fightReelWear, bool) {
	if w.settled {
		return *w, false
	}
	if w.SlipWear <= 0 && w.BurnWear <= 0 && w.BreakHits <= 0 && !w.BaseCharged {
		return *w, false
	}
	w.settled = true
	return *w, true
}

// markRodUsedLocked 把本竿标成“已下水使用”：抛竿即使没咬钩，轮内零件也要结算
// 每竿基准与泡水一次。已落库的旧账不再重复记，避免同一竿扣两次基准。
// 调用方必须持有 FishState 锁。
func (w *fightReelWear) markRodUsedLocked(rodGUID string) {
	if w.settled {
		return
	}
	w.Used = true
	w.BaseCharged = true
	if w.rodGUID == "" {
		w.rodGUID = rodGUID
	}
}

// markRodBreakLocked 记下本竿发生过断件冲击（断线/断竿那一次），轮内零件随之再扣一点。
func (w *fightReelWear) markRodBreakLocked() {
	if w.settled {
		return
	}
	w.Used = true
	w.BreakHits++
}

// loadTickSecondsLocked 把两条 14/8 之间的真实间隔折算成结算时长（秒）。
// 首条上报按 tunable 的兜底节拍起算：在那之前过了多久服务端无从得知。间隔为负
// （时钟回拨）算 0 秒，超过上限只记上限，免得一条迟到上报把整段发热曲线一次烧完。
func (w *fightReelWear) loadTickSecondsLocked(now time.Time, def, maxTick float64) float64 {
	if w.lastLoadAt.IsZero() {
		return def
	}
	gap := now.Sub(w.lastLoadAt).Seconds()
	if gap <= 0 {
		return 0
	}
	if gap > maxTick {
		return maxTick
	}
	return gap
}

// partCounts 把本竿账本折算成零件消耗事件：过载秒数与打滑次数直接沿用摩擦片那条账，
// 不重复计数。
func (w fightReelWear) partCounts() reelPartCounts {
	var c reelPartCounts
	if w.BaseCharged {
		c.Rods, c.Soaks = 1, 1
	}
	c.OverloadSecs = w.BurnSecs
	c.SlipTicks = w.SlipTicks
	c.BreakHits = w.BreakHits
	return c
}

// reelOilBurnFactor 是轮油对锁轮烧穿时长的系数：品质越高越耐烧，
// 轮油自身快干涸时保护效果打折（对半）。
func reelOilBurnFactor(quality, durability float64) float64 {
	if quality < 0 {
		quality = 0
	}
	if quality > 100 {
		quality = 100
	}
	if durability < 0 {
		durability = 0
	}
	if durability > 1 {
		durability = 1
	}
	return (0.75 + quality/200) * (0.5 + 0.5*durability)
}

// reelSlipOilFactor 把轮油磨损倍率收进 0.8~1.2：好油省、差油费。
func reelSlipOilFactor(v float64) float64 {
	if v <= 0 {
		return 1
	}
	if v < 0.8 {
		return 0.8
	}
	if v > 1.2 {
		return 1.2
	}
	return v
}

// childWireFloat 读子实例上的浮点字段，兼容 FLOAT/DOUBLE 与 U32 承载 IEEE754
// 两种 registry 布局；字段缺失返回 0。
func childWireFloat(obj *proto.Value, csName string) float64 {
	if obj == nil || obj.Kind != proto.KindObj {
		return 0
	}
	idx := fieldIdxByCS(obj.Class, csName)
	if idx < 0 || idx >= obj.FieldCount() {
		return 0
	}
	f := obj.Fields[idx]
	switch fieldOpAt(obj.Class, idx) {
	case "FLOAT", "DOUBLE":
		return f.Float
	case "U32":
		if f.Num == nil {
			return 0
		}
		return float64(proto.F32FromBits(uint32(*f.Num)))
	default:
		return 0
	}
}

// fightReelCache 读取本竿搏鱼需要的渔轮承力、摩擦片额定值、轮油状态与鱼拉力。
// 只在缓存失效时调用（一次搏鱼一次），所以允许读库。
func (s *Server) fightReelCache(ctx context.Context, uid int64, rodGUID string, f *RolledFish) fightReelWear {
	var out fightReelWear
	if s == nil || s.DB == nil || f == nil || rodGUID == "" {
		return out
	}
	reelGUID, err := s.DB.GetItemGUIDByRigSlot(ctx, rodGUID, gearCatReel)
	if err != nil || reelGUID == "" {
		return out
	}
	row, err := s.DB.GetItemByGUID(ctx, reelGUID)
	if err != nil || row == nil || row.UID != uid {
		return out
	}
	perf, err := s.DB.GetReelPerformance(ctx, row.GUID)
	if err != nil || perf == nil {
		perf, err = s.syncReelPerformance(ctx, row)
		if err != nil || perf == nil {
			return out
		}
	}
	rated, ok := s.reelWasherRatedFromRow(ctx, row)
	if !ok || rated <= 0 {
		return out
	}
	oilQuality, oilDurability := s.reelOilStateFromRow(ctx, row)
	setting, _ := s.rodReelDragSetting(ctx, rodGUID)
	return fightReelWear{
		valid:    true,
		fishGUID: f.FishGUID,
		rodGUID:  rodGUID,
		reelGUID: reelGUID,
		locked:   setting >= maxReelDragStop,
		dragKg:   reelDragAtSetting(perf, setting),
		lockedKg: perf.LockedDragKg,
		ratedKg:  rated,
		oilWear:  perf.OilWearFactor,
		oilBurn:  reelOilBurnFactor(oilQuality, oilDurability),
		pullKg:   s.fishFightPullFor(f, s.fishFightProfileFor(f)),
	}
}

// reelBurnSecondsFor 给出当前承力状态下的锁轮烧穿时长 T：片子越弱、油越差、
// 鱼越猛，T 越短。强度系数按摩擦片额定线性铺开（顶级约为校准点 5.3 倍）。
// 返回 0 表示算不出来，本条不按曲线烧。
func (s *Server) reelBurnSecondsFor(w fightReelWear) float64 {
	if w.ratedKg <= 0 || w.lockedKg <= 0 {
		return 0
	}
	strength := w.ratedKg / reelBurnCalibrationDragKg
	if strength <= 0 {
		return 0
	}
	base := s.tunableF("reel_locked_burn_base_s", defaultReelBurnBaseSeconds)
	if base <= 0 {
		base = defaultReelBurnBaseSeconds
	}
	oil := w.oilBurn
	if oil <= 0 {
		oil = 1
	}
	overload := math.Max(0.35, math.Min(3.0, w.pullKg/w.lockedKg-1+0.35))
	T := base * strength * oil / overload
	if T <= 0 || T > 600 {
		return 0
	}
	return T
}

// fightReelTickWear 把一条 14/8 折算成磨损增量，tick 是它与上一条上报之间的真实秒数。
//
// 未锁轮：鱼拉力高于当前泄力才算打滑，单次 reelSlipWear 再乘超出比例与轮油系数。
// 锁轮：鱼拉力高于此刻承力才算过载，按 (段内秒数/T)^p 在段内加速；一旦不再过载
// （解锁、卸力、鱼停），段作废，下段从曲线开头重新爬。
func (s *Server) fightReelTickWear(w fightReelWear, tick float64) (ticks int, slipWear, burnWear, burnSecs, segSecs float64) {
	if tick <= 0 {
		return 0, 0, 0, 0, 0
	}
	if w.locked {
		if w.lockedKg <= 0 || w.pullKg <= w.lockedKg+1e-9 {
			return 0, 0, 0, 0, 0
		}
		segSecs = w.SegSecs
		T := s.reelBurnSecondsFor(w)
		if T > 0 {
			p := s.tunableF("reel_locked_burn_curve", defaultReelBurnCurve)
			if p <= 0 {
				p = defaultReelBurnCurve
			}
			segEnd := segSecs + tick
			if segEnd > T {
				segEnd = T
			}
			delta := math.Pow(segEnd/T, p) - math.Pow(segSecs/T, p)
			if delta > 0 {
				if delta > 1 {
					delta = 1
				}
				burnWear = delta
			}
			segSecs = segEnd
		}
		return 0, 0, burnWear, tick, segSecs
	}
	if w.dragKg <= 0 || w.pullKg <= w.dragKg+1e-9 {
		return 0, 0, 0, 0, 0
	}
	exceed := math.Max(1, math.Min(2, w.pullKg/w.dragKg))
	return 1, reelSlipWear * exceed * reelSlipOilFactor(w.oilWear), 0, 0, 0
}

// recordFightReelLoad 结算一条 14/8 负载上报：只有正在搏鱼的那根竿算账。
// 查库放在锁外，写回只加本条增量，避免并发上报互相覆盖。
func (s *Server) recordFightReelLoad(ctx *Ctx) {
	if s == nil || s.DB == nil || ctx == nil || ctx.Sess == nil {
		return
	}
	uid := ctx.Sess.Userid
	st := s.fishingStateForRequest(ctx)
	st.mu.Lock()
	fish, gear, castActive, hooked, resolved, cached := st.Pending, st.GearGuid, st.CastActive, st.Hooked, st.Resolved, st.reelWear
	st.mu.Unlock()
	if !castActive || !hooked || resolved || fish == nil || gear == "" {
		return
	}
	if !cached.valid || cached.fishGUID != fish.FishGUID || cached.rodGUID != gear {
		cached = s.fightReelCache(s.dbCtx(), uid, gear, fish)
	}
	if !cached.valid || cached.ratedKg <= 0 {
		return
	}
	// 过载秒数按真实时长结算：上一条 14/8 到这一条之间隔了多久就记多久，
	// 只有本竿第一条上报才用兜底节拍（那之前服务端不知道已经过了多久）。
	now := time.Now()
	tick := s.tunableF("reel_load_tick_seconds", defaultReelLoadTickSeconds)
	if tick <= 0 || tick > 30 {
		tick = defaultReelLoadTickSeconds
	}
	maxTick := s.tunableF("reel_load_max_tick_seconds", defaultReelLoadMaxTickSeconds)
	if maxTick <= 0 || maxTick > 60 {
		maxTick = defaultReelLoadMaxTickSeconds
	}
	probeOn := s.tunableF("fight_load_probe", 1) != 0
	tick = cached.loadTickSecondsLocked(now, tick, maxTick)
	ticks, slipWear, burnWear, burnSecs, segSecs := s.fightReelTickWear(cached, tick)

	st.mu.Lock()
	if st.Pending != fish || st.GearGuid != gear {
		st.mu.Unlock()
		return // 期间换竿或换鱼：这条上报作废
	}
	previous, hasPrevious := fightReelWear{}, false
	cur := &st.reelWear
	if !cur.valid || cur.fishGUID != fish.FishGUID || cur.rodGUID != gear {
		// 换鱼即换一本账：旧鱼那笔若还没落库（客户端没报离场也要兜底），先取走再开新账。
		previous, hasPrevious = cur.claimLocked()
		cached.Used = cached.Used || cur.Used
		cached.BaseCharged = cached.BaseCharged || cur.BaseCharged
		cached.BreakHits += cur.BreakHits
		*cur = cached
	}
	cur.SlipTicks += ticks
	cur.SlipWear += slipWear
	cur.BurnWear += burnWear
	cur.BurnSecs += burnSecs
	cur.SegSecs = segSecs
	cur.lastLoadAt = now
	if probeOn {
		// 只观测不结算：14/8 里哪个载荷是鱼拉力还没有第二份证据，先把每竿的字段
		// 范围写进损伤日志，攒够凭据再决定要不要用它。
		cur.loadProbe.observe(ctx.Tree, ctx.DecodeOK)
	}
	st.mu.Unlock()
	if hasPrevious {
		s.applyFightReelWear(s.dbCtx(), uid, previous, "14/8")
	}
}

// applyFightReelWear 把一笔已取走的磨损账写入摩擦片，返回是否真的写了库。
// event 只用于损伤日志（14/4 收竿、17/9 断件、14/12 鱼离场、14/7 抛竿兜底）。
// applyFightReelWear 把一笔已取走的磨损账写进渔轮：摩擦片制动力、轮内零件耐久
// 与本体耐久在同一次物品树写入里落库，避免出现“片磨了但零件与本体没动”的两套数字。
// 本体耐久取 min(现值, 零件聚合值)：只降不升，换件与修理才允许抬回去。
// event 只用于损伤日志（14/4 收竿、17/9 断件、14/12 鱼离场、14/7 抛竿兜底）。
func (s *Server) applyFightReelWear(ctx context.Context, uid int64, w fightReelWear, event string) bool {
	if s == nil || s.DB == nil {
		return false
	}
	if w.reelGUID == "" && w.rodGUID != "" {
		// 抛竿只登记了竿：空竿结算要按钓组解析出渔轮，否则每竿基准与泡水会被整笔丢掉。
		if parsed, perr := s.DB.GetItemGUIDByRigSlot(ctx, w.rodGUID, gearCatReel); perr == nil && parsed != "" {
			w.reelGUID = parsed
		}
	}
	if w.reelGUID == "" {
		return false
	}
	wear := clampUnit(w.SlipWear + w.BurnWear)
	counts := w.partCounts()
	if wear <= 0 && counts.total() <= 0 {
		return false
	}
	reel, err := s.DB.GetItemByGUID(ctx, w.reelGUID)
	if err != nil || reel == nil || reel.UID != uid {
		return false
	}
	fields, err := FieldsFromJSON(reel.FieldsJSON)
	if err != nil {
		return false
	}
	var wornOut, found bool
	if wear > 0 {
		wornOut, found = s.degradeFrictionWasherFields(ctx, reel, fields, wear)
		if !found {
			// 老档没有独立的摩擦片子件：回退到既有的本体耐久通道，继续走零件经济只会读不出东西。
			_, derr := s.damageReelSlipWear(ctx, uid, w.rodGUID, w.reelGUID, wear, fightWearDetailFields(w, wear, nil))
			return derr == nil
		}
	}
	parts := s.wearReelPartsInFields(ctx, fields, counts)
	durability := reel.Durability
	if body, ok := s.reelBodyDurabilityInFields(ctx, reel, fields); ok && body < durability {
		durability = body
	}
	before := reel.Durability
	bumpCreatedTicks(fields, reel.WireClass)
	fj, err := FieldsToJSON(fields)
	if err != nil {
		return false
	}
	// 摩擦片与母轮受力上限必须一起写，否则下次下发会用旧制动力覆盖新磨损。
	snapshot := *reel
	snapshot.FieldsJSON = fj
	if s.canonicalizeReelStrength(ctx, &snapshot, fields) {
		if fj, err = FieldsToJSON(fields); err != nil {
			return false
		}
	}
	if err := s.DB.UpdateItemFields(ctx, reel.GUID, fj, reel.Qty, durability, reel.Workmanship, reel.RigGUID); err != nil {
		return false
	}
	reel.Durability = durability
	reel.FieldsJSON = fj
	perf, perr := s.syncReelPerformance(ctx, reel)
	if perr != nil {
		return false
	}
	if err := s.DB.AddItemDamageLog(ctx, store.ItemDamageLog{
		UID: uid, ItemGUID: reel.GUID, ItemID: reel.ItemID, Event: damageEventReelOverload,
		BeforeDurability: ptrFloat64(before), AfterDurability: ptrFloat64(reel.Durability),
		DetailJSON: frictionWearDetail(w.rodGUID, perf, reel.Durability, wornOut, fightWearDetailFields(w, wear, parts)),
	}); err != nil {
		return false
	}
	return true
}

// fightWearDetailFields 把本竿磨损账折算成损伤日志里的诊断字段（万分比），
// parts 是本次各零件扣掉的寿命比例。
func fightWearDetailFields(w fightReelWear, wear float64, parts map[string]float64) map[string]any {
	round2 := func(v float64) float64 { return math.Round(v*100) / 100 }
	detail := map[string]any{
		"slip_ticks": w.SlipTicks,
		"slip_wear":  round2(w.SlipWear * 10000), "burn_seconds": round2(w.BurnSecs),
		"burn_wear": round2(w.BurnWear * 10000), "wear_bp": round2(wear * 10000),
		"locked": w.locked, "pull_kg": round2(w.pullKg), "drag_kg": round2(w.dragKg),
		"locked_drag_kg": round2(w.lockedKg), "washer_rated_kg": round2(w.ratedKg),
	}
	if w.BaseCharged {
		detail["rod_base"] = 1
	}
	if w.BreakHits > 0 {
		detail["break_hits"] = w.BreakHits
	}
	// 负载观测只进日志：字段范围是下一步挑出鱼拉力的凭据，不参与结算。
	for key, val := range w.loadProbe.logFields() {
		detail[key] = val
	}
	if len(parts) > 0 {
		partWear := map[string]float64{}
		for itemID, dropped := range parts {
			partWear[itemID] = round2(dropped * 10000)
		}
		detail["part_wear_bp"] = partWear
	}
	return detail
}

// settleFightReelWearState 结算指定竿的渔轮磨损：同一竿只写一次，重发不重复扣。
// 磨损落库后立刻把渔轮树推给在线客户端，黄标/红标与损耗% 都读物品树上的耐久。
func (s *Server) settleFightReelWearState(ctx context.Context, st *FishState, uid int64, event string) {
	if s == nil || s.DB == nil || st == nil {
		return
	}
	st.mu.Lock()
	w, ok := st.reelWear.claimLocked()
	if w.rodGUID == "" {
		// 账上只有竿 GUID 时也能结算：状态里的竿就是这一竿的钓组。
		w.rodGUID = st.GearGuid
	}
	st.mu.Unlock()
	if !ok {
		return
	}
	if s.applyFightReelWear(ctx, uid, w, event) {
		for _, sess := range s.sessionsForUID(uid) {
			sess.Push4_22Items([]string{w.reelGUID})
		}
	}
}

// settleFightReelWear 按竿 GUID 解析状态后结算一次，供手上没有状态指针的调用方使用。
func (s *Server) settleFightReelWear(ctx context.Context, uid int64, rodGUID, event string) {
	if s == nil || s.DB == nil || rodGUID == "" {
		return
	}
	s.settleFightReelWearState(ctx, s.fishingStateForRod(uid, rodGUID), uid, event)
}

// markFightReelBreak 记下本竿发生过断件冲击（17/9 的断线/断竿/断轮），轮内
// 零件的断件冲击账只在真断件那一次扣，不随打滑与过载重复累加。
// 必须在结算之前调用：磨损账一旦取走就不再追记。
func (s *Server) markFightReelBreak(uid int64, rodGUID string) {
	if s == nil || rodGUID == "" {
		return
	}
	st := s.fishingStateForRod(uid, rodGUID)
	st.mu.Lock()
	st.reelWear.markRodBreakLocked()
	st.mu.Unlock()
}
