package gameserver

// 搏鱼期间的渔轮磨损结算。
//
// 客户端力度条顶满只等于"顶到此刻张力上限"，并不是超过上限，所以爆不爆件
// 客户端永远说不出口（实机满红几十秒也不报断件）。代价只能由服务端结算：
// 未锁轮且鱼拉力高于当前泄力 → 泄力打滑磨摩擦片；锁轮且鱼拉力高于此刻承力 →
// 按发热曲线烧摩擦片，解锁散热、下次锁轮从曲线开头重新慢慢爬。
// 节拍跟着客户端 14/8 负载上报：实测搏鱼期间每 1.8~2.0 秒一条，一条算一次判定。
// 口径见 specs/changes/reel-friction-life-model/spec.md。

import (
	"context"
	"math"

	"rf4server/internal/proto"
)

const (
	// defaultReelLoadTickSeconds 是一条 14/8 上报折算的时长（tunable reel_load_tick_seconds）。
	defaultReelLoadTickSeconds = 1.9
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

	SlipTicks int
	SlipWear  float64 // 通道一：泄力打滑累计的寿命比例
	BurnSecs  float64 // 通道二：锁轮过载累计秒数（跨段，供零件应力账使用）
	BurnWear  float64 // 通道二：过载曲线累计的寿命比例
	SegSecs   float64 // 当前过载段内已持续秒数；解锁清零，下段从曲线开头重爬
	settled   bool    // 本竿是否已落库（每竿一次）
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
func (w *fightReelWear) claimLocked() (fightReelWear, bool) {
	if w.settled || (w.SlipWear <= 0 && w.BurnWear <= 0) {
		return *w, false
	}
	w.settled = true
	return *w, true
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

// fightReelTickWear 把一条 14/8 折算成磨损增量。
//
// 未锁轮：鱼拉力高于当前泄力才算打滑，单次 reelSlipWear 再乘超出比例与轮油系数。
// 锁轮：鱼拉力高于此刻承力才算过载，按 (段内秒数/T)^p 在段内加速；一旦不再过载
// （解锁、卸力、鱼停），段作废，下段从曲线开头重新爬。
func (s *Server) fightReelTickWear(w fightReelWear, tick float64) (ticks int, slipWear, burnWear, burnSecs, segSecs float64) {
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
	tick := s.tunableF("reel_load_tick_seconds", defaultReelLoadTickSeconds)
	if tick <= 0 || tick > 30 {
		tick = defaultReelLoadTickSeconds
	}
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
		*cur = cached
	}
	cur.SlipTicks += ticks
	cur.SlipWear += slipWear
	cur.BurnWear += burnWear
	cur.BurnSecs += burnSecs
	cur.SegSecs = segSecs
	st.mu.Unlock()
	if hasPrevious {
		s.applyFightReelWear(s.dbCtx(), uid, previous, "14/8")
	}
}

// applyFightReelWear 把一笔已取走的磨损账写入摩擦片，返回是否真的写了库。
// event 只用于损伤日志（14/4 收竿、17/9 断件、14/12 鱼离场、14/7 抛竿兜底）。
func (s *Server) applyFightReelWear(ctx context.Context, uid int64, w fightReelWear, event string) bool {
	if s == nil || s.DB == nil || w.reelGUID == "" {
		return false
	}
	wear := w.SlipWear + w.BurnWear
	if wear > 1 {
		wear = 1
	}
	if wear <= 0 {
		return false
	}
	round2 := func(v float64) float64 { return math.Round(v*100) / 100 }
	detail := map[string]any{
		"event": event, "slip_ticks": w.SlipTicks,
		"slip_wear": round2(w.SlipWear * 10000), "burn_seconds": round2(w.BurnSecs),
		"burn_wear": round2(w.BurnWear * 10000), "wear_bp": round2(wear * 10000),
		"locked": w.locked, "pull_kg": round2(w.pullKg), "drag_kg": round2(w.dragKg),
		"locked_drag_kg": round2(w.lockedKg), "washer_rated_kg": round2(w.ratedKg),
	}
	if _, err := s.damageReelSlipWear(ctx, uid, w.rodGUID, w.reelGUID, wear, detail); err != nil {
		if s.log != nil {
			s.log.Info("渔轮磨损落库跳过", "rod", guidShort(w.rodGUID), "event", event, "err", err)
		}
		return false
	}
	return true
}

// settleFightReelWearState 结算指定竿的渔轮磨损：同一竿只写一次，重发不重复扣。
// 磨损落库后立刻把渔轮树推给在线客户端，黄标/红标与损耗% 都读物品树上的耐久。
func (s *Server) settleFightReelWearState(ctx context.Context, st *FishState, uid int64, event string) {
	if s == nil || s.DB == nil || st == nil {
		return
	}
	st.mu.Lock()
	w, ok := st.reelWear.claimLocked()
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
