package gameserver

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"math/rand"
	"time"
)

const (
	fishHuntDaily = iota
	fishHuntGather
	fishHuntActive
	fishHuntGiant
)

const (
	fishHuntModeFreshwater = iota
	fishHuntModeMarine
)

const (
	fishHuntCompWindowSeconds = 10 * 60
	fishHuntSpotResetM        = 20.0
)

type fishHuntState struct {
	UID           int64
	Loc           string
	Spot          uint64
	Mode          int64
	Zone          int64
	ZoneKnown     bool
	AnchorX       float64
	AnchorZ       float64
	AnchorKnown   bool
	ActiveSeconds float64
	ValidBites    int64
	Pity          int64
	BigMiss       int64
	CompFish      int64
	CompGrade     int64
	CompUntil     float64
	LastCatchGUID string
	LastActiveTS  float64
	UpdatedTS     float64
}

type fishHuntReadiness struct {
	Stage int
	A     float64
	E     float64
	G     float64
	P     float64
	Q     float64
	Comp  bool
}

func (s *Server) fishHuntEnabled() bool {
	return s != nil && s.DB != nil && s.tunableF("fish_hunt_enabled", 1) != 0
}

func (s *Server) loadFishHuntState(ctx context.Context, uid int64, loc string, spot uint64) *fishHuntState {
	h := &fishHuntState{UID: uid, Loc: loc, Spot: spot}
	if !s.fishHuntEnabled() || uid == 0 || loc == "" {
		return h
	}
	var anchorKnown, zoneKnown, compGrade int64
	err := s.DB.SQL.QueryRowContext(ctx, `
		SELECT anchor_x,anchor_z,anchor_known,active_seconds,valid_bites,pity,
		       big_miss,comp_fish,comp_grade,comp_until,last_catch_guid,
		       last_active_ts,updated_ts,hunt_mode,sea_zone,sea_zone_known
		FROM fish_hunt_state WHERE uid=? AND loc=? AND spot=?`,
		uid, loc, int64(spot)).Scan(
		&h.AnchorX, &h.AnchorZ, &anchorKnown, &h.ActiveSeconds, &h.ValidBites, &h.Pity,
		&h.BigMiss, &h.CompFish, &compGrade, &h.CompUntil, &h.LastCatchGUID,
		&h.LastActiveTS, &h.UpdatedTS, &h.Mode, &h.Zone, &zoneKnown)
	h.AnchorKnown = anchorKnown != 0
	h.ZoneKnown = zoneKnown != 0
	h.CompGrade = compGrade
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.log.Warn("鱼猎状态读取失败", "uid", uid, "loc", loc, "spot", spot, "err", err)
	}
	return h
}

func (s *Server) saveFishHuntState(ctx context.Context, h *fishHuntState) {
	if !s.fishHuntEnabled() || h == nil || h.UID == 0 || h.Loc == "" {
		return
	}
	h.UpdatedTS = float64(time.Now().Unix())
	_, err := s.DB.SQL.ExecContext(ctx, `
		INSERT INTO fish_hunt_state(
			uid,loc,spot,anchor_x,anchor_z,anchor_known,active_seconds,valid_bites,
			pity,big_miss,comp_fish,comp_grade,comp_until,last_catch_guid,
			last_active_ts,updated_ts,hunt_mode,sea_zone,sea_zone_known)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(uid,loc,spot) DO UPDATE SET
			anchor_x=excluded.anchor_x,anchor_z=excluded.anchor_z,
			anchor_known=excluded.anchor_known,active_seconds=excluded.active_seconds,
			valid_bites=excluded.valid_bites,pity=excluded.pity,
			big_miss=excluded.big_miss,comp_fish=excluded.comp_fish,
			comp_grade=excluded.comp_grade,comp_until=excluded.comp_until,
			last_catch_guid=excluded.last_catch_guid,
			last_active_ts=excluded.last_active_ts,updated_ts=excluded.updated_ts,
			hunt_mode=excluded.hunt_mode,sea_zone=excluded.sea_zone,
			sea_zone_known=excluded.sea_zone_known`,
		h.UID, h.Loc, int64(h.Spot), h.AnchorX, h.AnchorZ, boolInt(h.AnchorKnown),
		h.ActiveSeconds, h.ValidBites, h.Pity, h.BigMiss, h.CompFish, h.CompGrade,
		h.CompUntil, h.LastCatchGUID, h.LastActiveTS, h.UpdatedTS,
		h.Mode, h.Zone, boolInt(h.ZoneKnown))
	if err != nil {
		s.log.Warn("鱼猎状态保存失败", "uid", h.UID, "loc", h.Loc, "spot", h.Spot, "err", err)
	}
}

// huntAnchorGroundbait 只在超过 20 米的新窝点时重置狩猎进程。
// 同一窝点补料只刷新窝账，不清空发窝度。
func (s *Server) huntAnchorGroundbait(ctx context.Context, uid int64, loc string, spot uint64, x, z float64) {
	if !s.fishHuntEnabled() {
		return
	}
	h := s.loadFishHuntState(ctx, uid, loc, spot)
	if h.AnchorKnown && math.Hypot(x-h.AnchorX, z-h.AnchorZ) <= fishHuntSpotResetM {
		return
	}
	h.AnchorX, h.AnchorZ, h.AnchorKnown = x, z, true
	h.ActiveSeconds, h.ValidBites, h.Pity = 0, 0, 0
	h.BigMiss, h.CompFish, h.CompGrade, h.CompUntil = 0, 0, 0, 0
	h.LastActiveTS = float64(time.Now().Unix())
	s.saveFishHuntState(ctx, h)
}

func (s *Server) huntTouch(h *fishHuntState) {
	if h == nil {
		return
	}
	now := float64(time.Now().Unix())
	if h.LastActiveTS > 0 {
		dt := now - h.LastActiveTS
		if dt > 0 && dt <= 60 {
			h.ActiveSeconds += dt
		}
	}
	h.LastActiveTS = now
}

func (s *Server) fishHuntBaitMatch(ctx context.Context, key string, c fishRollContext) float64 {
	best := int64(0)
	for _, bait := range c.baits {
		if bait == "" {
			continue
		}
		for fish, tier := range s.cachedBaitTiers(ctx, c, bait) {
			if fish == key && tier > best {
				best = tier
			}
		}
	}
	switch best {
	case 3:
		return 1
	case 2:
		return .75
	case 1:
		return .5
	default:
		return .35
	}
}

type marineFishTraits struct {
	schooling float64
	chase     float64
	ambush    float64
	pit       float64
}

// marineTraits 是海图 v1 的鱼群习性代理值。现有生态表没有单独的社会性字段，
// 先用活动度、攻击性、水层和稀有度推导，后续可由后台覆盖。
func (s *Server) marineTraits(ctx context.Context, key string, c fishRollContext) marineFishTraits {
	p := s.cachedFishEcology(ctx, c, key)
	activity := groundbaitClamp(p.Activity, .5, 1.5)
	aggression := groundbaitClamp(p.Aggression, .5, 1.5)
	layer := groundbaitClamp(p.LayerPref, 0, 1)
	return marineFishTraits{
		schooling: groundbaitClamp(.35+(activity-.7)*.45-(aggression-1)*.20+(1-layer)*.10, .05, .95),
		chase:     groundbaitClamp(.15+(aggression-.8)*.55+p.CurrentPref*.20, .05, .95),
		ambush:    groundbaitClamp(.15+layer*.60+(1.2-activity)*.20, .05, .95),
		pit:       groundbaitClamp(.15+layer*.65+float64(p.RarityTier)*.04, .05, .95),
	}
}

func marineActionMatch(c fishRollContext, t marineFishTraits) float64 {
	switch c.lureAction {
	case "trolling":
		return groundbaitClamp(.55+.45*(.60*t.schooling+.40*t.chase), .35, 1)
	case "marine_jig", "vertical_dive", "vertical_ascend", "bottom_layer_move", "bottom_move":
		return groundbaitClamp(.50+.50*(.60*t.ambush+.40*t.pit), .30, 1)
	case "drift":
		return groundbaitClamp(.50+.50*(.50*t.schooling+.50*t.chase), .30, 1)
	case "jig":
		return groundbaitClamp(.60+.40*(.55*t.ambush+.45*t.chase), .30, 1)
	default:
		return .55
	}
}

func marineDepthMatch(c fishRollContext, t marineFishTraits) float64 {
	layer := normalizeWaterLayer(c.lureLayer)
	if c.spotEnv.Known && c.spotEnv.DepthM > 0 {
		layer = groundbaitClamp(c.spotEnv.DepthM/220, 0, 1)
	}
	return groundbaitClamp(1-math.Abs(layer-.58*t.pit-.20*t.ambush), .25, 1)
}

// fishHuntReadiness 根据水域模式分派：淡水用窝料，海图用声呐区号和搜索动作。
func (s *Server) fishHuntReadiness(ctx context.Context, key string, c fishRollContext, h *fishHuntState) fishHuntReadiness {
	if marineHuntContext(c) {
		return s.fishMarineHuntReadiness(ctx, key, c, h)
	}
	return s.fishFreshwaterHuntReadiness(ctx, key, c, h)
}

func marineHuntContext(c fishRollContext) bool {
	return marineRigType(c.rigType) || c.runtime != nil && c.runtime.chartKnown
}

func (s *Server) fishMarineHuntReadiness(ctx context.Context, key string, c fishRollContext, h *fishHuntState) fishHuntReadiness {
	out := fishHuntReadiness{Stage: fishHuntDaily}
	if h == nil || s.DB == nil {
		return out
	}
	s.huntTouch(h)
	zone := 0
	zoneKnown := c.runtime != nil && c.runtime.zoneKnown
	if zoneKnown {
		zone = c.runtime.zone
	}
	if h.Mode != fishHuntModeMarine {
		h.ActiveSeconds, h.ValidBites, h.Pity = 0, 0, 0
		h.BigMiss, h.CompFish, h.CompGrade, h.CompUntil = 0, 0, 0, 0
		h.Zone, h.ZoneKnown = 0, false
	}
	if zoneKnown {
		if h.ZoneKnown && h.Zone != int64(zone) {
			h.ActiveSeconds, h.ValidBites, h.Pity = 0, 0, 0
			h.BigMiss, h.CompFish, h.CompGrade, h.CompUntil = 0, 0, 0, 0
		}
		h.Zone, h.ZoneKnown = int64(zone), true
	}
	h.Mode = fishHuntModeMarine
	traits := s.marineTraits(ctx, key, c)
	action := marineActionMatch(c, traits)
	depth := marineDepthMatch(c, traits)
	zoneMatch := .15
	if zoneKnown {
		zoneMatch = 1
	}
	sonar := .15
	if zoneKnown {
		sonar = .75
		if c.spotEnv.Known {
			sonar += .10
		}
	}
	if c.runtime != nil && c.runtime.sonarSystemID != "" {
		if profile := s.sonarProfileFor(ctx, c.runtime.sonarSystemID); profile != nil {
			sonar = math.Max(sonar, .80)
			if profile.depthMax > profile.depthMin {
				sonar += .08
			}
			if profile.sizeP90 >= profile.sizeMed && profile.sizeMed > 0 {
				sonar += .05
			}
			sonar = math.Min(sonar, 1)
		}
	}
	out.P = .65*(1-math.Exp(-h.ActiveSeconds/720)) + .35*(1-math.Exp(-float64(h.ValidBites)/12))
	out.P = groundbaitClamp(out.P, 0, 1)
	out.A = groundbaitClamp(zoneMatch*(.35+.45*sonar+.20*out.P)*(.55+.45*action), 0, 1)
	env := s.fishBiteEnvironmentWeight(ctx, key, c)
	eco := groundbaitClamp((env-.35)/1.65, 0, 1)
	out.E = groundbaitClamp(.70*eco+.30*.35, 0, 1)
	water := fishWaterConditionsFor(s.cachedFishEcology(ctx, c, key), c)
	lure := groundbaitClamp((s.fishLureWeight(s.cachedFishEcology(ctx, c, key), c, water)-.45)/1.05, 0, 1)
	out.G = groundbaitClamp(.45*lure+.35*action+.20*depth, 0, 1)
	k := groundbaitClamp(.5+c.skill.EcologyInterest*2+c.skill.TrophyLuck*2, 0, 1)
	out.Q = groundbaitClamp(out.E*out.G*(.25+.75*out.P)*(.90+.10*k), 0, 1)
	out.Comp = h.CompFish > 0 && float64(time.Now().Unix()) <= h.CompUntil
	switch {
	case out.A < .25 || out.E < .25 || out.G < .25:
		out.Stage = fishHuntDaily
	case out.A >= .70 && out.Q >= .82:
		out.Stage = fishHuntGiant
	case out.A >= .55 && out.Q >= .58:
		out.Stage = fishHuntActive
	default:
		out.Stage = fishHuntGather
	}
	return out
}

// hookScore 把实际钩号与钩体长度合成为一个连续值；0=小钩，0.5=中钩，1=大钩。
// 不把钩号直接当阈值，避免同一尺寸族里出现突然断层。
func hookScore(spec gearPhysicsSpec) float64 {
	if !spec.HookPresent {
		return .5
	}
	size := groundbaitClamp(float64(spec.HookSize)/33.0, 0, 1)
	length := groundbaitClamp((spec.HookLengthMM-1.8)/(76.0-1.8), 0, 1)
	return groundbaitClamp(.65*size+.35*length, 0, 1)
}

// hookGapeG 是钩型的参考入口容量。这里只用于软匹配，不再作为派鱼硬上限。
func hookGapeG(spec gearPhysicsSpec) float64 {
	if !spec.HookPresent {
		return 0
	}
	return hookWeightCapG(spec.HookLengthMM, spec.HookSize) * gearOverloadFactor
}

// hookBiteDelayFactor 让大钩上口慢一点、小钩入口快一点。
func hookBiteDelayFactor(spec gearPhysicsSpec) float64 {
	if !spec.HookPresent {
		return 1
	}
	return .90 + .30*hookScore(spec)
}

func (s *Server) hookBiteDelayFactorForRig(ctx context.Context, uid int64, rod string) float64 {
	if s == nil || s.DB == nil || uid == 0 || rod == "" {
		return 1
	}
	ids := s.rigGearIDs(ctx, uid, rod)
	return hookBiteDelayFactor(s.loadGearSpecForRig(ctx, ids, rod))
}

// hookSolidHold 返回普通鱼的稳定入口概率与中钩后保持率。
// 蓝冠不走这里，蓝冠由 luckProtected 直接保送。
func hookSolidHold(weightG int64, spec gearPhysicsSpec) (solid, hold float64) {
	if weightG <= 0 || !spec.HookPresent {
		return .98, .98
	}
	gape := hookGapeG(spec)
	if gape <= 0 {
		return .98, .98
	}
	r := float64(weightG) / gape
	solid = .96 - .45*math.Max(0, r-.8)
	if r < .25 {
		solid *= .55 + 1.8*r
	}
	solid = groundbaitClamp(solid, .15, .96)
	hold = groundbaitClamp(.98-.30*math.Max(0, r-1), .50, .98)
	return
}

func (s *Server) applyHookBiteQuality(f *RolledFish, spec gearPhysicsSpec) {
	if f == nil {
		return
	}
	f.BiteQuality = "solid"
	f.HookHoldP = 1
	if f.Grade >= 4 {
		// 蓝冠是低概率天命鱼：钩型、嘬饵、保持率全部免检。
		f.LuckProtected = true
		return
	}
	solid, hold := hookSolidHold(f.WeightG, spec)
	if p, ok := s.hookBiteProfileFor(spec.HookItemID); ok {
		solid, hold = applyHookBiteProfile(f.WeightG, spec, p, solid, hold)
	}
	f.HookHoldP = hold
	if rand.Float64() > solid {
		f.BiteQuality = "nibble"
	}
}

func hookRetentionFails(f *RolledFish) bool {
	return f != nil && !f.LuckProtected && f.BiteQuality == "solid" &&
		f.HookHoldP > 0 && rand.Float64() > f.HookHoldP
}

func (s *Server) fishFreshwaterHuntReadiness(ctx context.Context, key string, c fishRollContext, h *fishHuntState) fishHuntReadiness {
	out := fishHuntReadiness{Stage: fishHuntDaily}
	if h == nil || s.DB == nil || !s.groundbaitEnabled() {
		return out
	}
	h.Mode = fishHuntModeFreshwater
	s.huntTouch(h)
	gb := s.groundbaitSpotAt(ctx, h.UID, h.Loc, c.x, c.z, c.hasPos, h.Spot)
	if gb.units <= 0 {
		return out
	}
	S := s.groundbaitStrength(gb.units)
	overdose := s.groundbaitOverdoseP(gb.units)
	out.A = groundbaitClamp(S*overdose, 0, 1)
	m := s.groundbaitSpeciesMultiplier(ctx, c, gb, key, S, overdose)
	capM := s.tunableF("groundbait_cap", 2.60)
	if capM <= 1.01 {
		capM = 2.60
	}
	out.G = groundbaitClamp((m-1)/(capM-1), 0, 1)

	env := s.fishBiteEnvironmentWeight(ctx, key, c)
	eco := groundbaitClamp((env-0.35)/1.65, 0, 1)
	out.E = groundbaitClamp(.70*eco+.30*s.fishHuntBaitMatch(ctx, key, c), 0, 1)
	out.P = .65*(1-math.Exp(-h.ActiveSeconds/720)) + .35*(1-math.Exp(-float64(h.ValidBites)/12))
	out.P = groundbaitClamp(out.P, 0, 1)
	k := groundbaitClamp(.5+c.skill.EcologyInterest*2+c.skill.TrophyLuck*2, 0, 1)
	out.Q = groundbaitClamp(out.E*out.G*(.25+.75*out.P)*(.90+.10*k), 0, 1)
	out.Comp = h.CompFish > 0 && float64(time.Now().Unix()) <= h.CompUntil

	switch {
	case out.A < .25 || out.E < .45 || out.G < .25:
		out.Stage = fishHuntDaily
	case out.A >= .70 && out.Q >= .85:
		out.Stage = fishHuntGiant
	case out.A >= .55 && out.Q >= .60:
		out.Stage = fishHuntActive
	default:
		out.Stage = fishHuntGather
	}
	return out
}

func fishHuntWeights(stage int, h *fishHuntState, trophyLuck, hookMatch float64,
	comp bool) (w1, w2, w3, w4 float64, forceTrophy bool) {
	switch stage {
	case fishHuntGather:
		w1, w2, w3, w4 = 54.5, 43, 2, .5
	case fishHuntActive:
		w1, w2, w3, w4 = 30, 61.2, 6.8, 2
	case fishHuntGiant:
		w1, w2, w3, w4 = 10, 64, 20, 6
	default:
		w1, w2, w3, w4 = 74.7, 24, 1, .3
	}
	if stage > fishHuntDaily && h != nil {
		switch {
		case h.Pity >= 50:
			w3 *= 2
		case h.Pity >= 30:
			w3 *= 1.5
		}
		w3 *= 1 + groundbaitClamp(trophyLuck, 0, .5)
		if comp {
			w3 *= 1.3
			if h.CompGrade >= 4 {
				w4 *= 1.5
			}
		}
		if h.Pity >= 60 || h.BigMiss >= 2 {
			forceTrophy = true
		}
	}
	// 小钩只改变普通入口内部的奖杯占比，不能顺带改变蓝冠的绝对概率。
	// 非硬保底时按非蓝冠总权重同比缩放 w4；硬保底只比较奖杯/蓝冠，
	// 因此直接跳过钩型偏置，保持原奖杯与蓝冠比例。
	if !forceTrophy {
		baseNonBlue := w1 + w2 + w3
		hookMatch = groundbaitClamp(hookMatch, 0, 1)
		if hookMatch < .5 {
			t := (.5 - hookMatch) / .5
			w1 *= 1 + .15*t
			w3 *= 1 - .35*t
		} else if hookMatch > .5 {
			t := (hookMatch - .5) / .5
			w1 *= 1 - .20*t
			w3 *= 1 + .30*t
		}
		biasedNonBlue := w1 + w2 + w3
		if baseNonBlue > 0 && biasedNonBlue > 0 {
			w4 *= biasedNonBlue / baseNonBlue
		}
	}
	return
}

func chooseHuntGrade(w1, w2, w3, w4 float64, forceTrophy bool) int64 {
	if forceTrophy {
		w1, w2 = 0, 0
		if w3+w4 <= 0 {
			w3 = 1
		}
	}
	total := w1 + w2 + w3 + w4
	if total <= 0 {
		return 1
	}
	r := rand.Float64() * total
	switch {
	case r < w1:
		return 1
	case r < w1+w2:
		return 2
	case r < w1+w2+w3:
		return 3
	default:
		return 4
	}
}

func triangularLog(lo, hi, mode float64) float64 {
	if lo <= 0 {
		lo = 1
	}
	if hi <= lo {
		return lo
	}
	if mode <= lo || mode >= hi {
		mode = math.Sqrt(lo * hi)
	}
	llo, lhi, lm := math.Log(lo), math.Log(hi), math.Log(mode)
	u := rand.Float64()
	f := (lm - llo) / (lhi - llo)
	switch {
	case u < f && lm > llo:
		return math.Exp(llo + math.Sqrt(u*(lhi-llo)*(lm-llo)))
	case lhi > lm:
		return math.Exp(lhi - math.Sqrt((1-u)*(lhi-llo)*(lhi-lm)))
	default:
		return mode
	}
}

func huntBandBounds(q, t, b float64, grade int64) (lo, hi, mode float64, ok bool) {
	if q <= 0 || t <= q || b <= t {
		return 0, 0, 0, false
	}
	switch grade {
	case 1:
		lo, hi, mode = math.Max(1, .15*q), .98*q, .45*q
	case 2:
		lo, hi, mode = 1.02*q, .98*t, math.Min(1.25*q, .92*t)
	case 3:
		lo, hi, mode = 1.02*t, .98*b, math.Min(1.15*t, .92*b)
	default:
		lo, hi, mode = 1.02*b, 1.60*b, 1.10*b
	}
	return lo, hi, mode, true
}

func clampHuntBandWeight(weight, low, high, modeMult, capG float64) float64 {
	if modeMult > 1 {
		weight *= math.Min(modeMult, 2)
	}
	if weight < low {
		weight = low
	}
	if weight > high {
		weight = high
	}
	if capG > 0 && weight > capG {
		weight = capG
	}
	return weight
}

func (s *Server) sampleHuntBand(ctx context.Context, key string, grade int64, modeMult, scale, capG float64, c fishRollContext) int64 {
	lines := s.cachedSettleStatics(ctx, c, key)
	lo, hi, mode, ok := huntBandBounds(float64(lines[0]), float64(lines[1]), float64(lines[2]), grade)
	if !ok {
		return 0
	}
	if capG > 0 {
		cap := capG / math.Max(scale, .0001)
		if cap < lo {
			return 0
		}
		if hi > cap {
			hi = cap
		}
		if mode > hi {
			mode = hi
		}
	}
	w := triangularLog(lo, hi, mode) * scale
	if w < 1 {
		w = 1
	}
	w = clampHuntBandWeight(w, lo*scale, hi*scale, modeMult, capG)
	return int64(math.Round(w))
}

func (s *Server) sampleHuntWeight(ctx context.Context, key string, modeMult, scale, capG float64, c fishRollContext,
	h *fishHuntState) (int64, int64, fishHuntReadiness) {
	readiness := s.fishHuntReadiness(ctx, key, c, h)
	w1, w2, w3, w4, force := fishHuntWeights(readiness.Stage, h, c.skill.TrophyLuck,
		hookScore(c.hook), readiness.Comp)
	if capG > 0 {
		lines := s.cachedSettleStatics(ctx, c, key)
		if capG/math.Max(scale, .0001) < float64(lines[2]) {
			w4 = 0
		}
		if capG/math.Max(scale, .0001) < float64(lines[1]) {
			w3, w4 = 0, 0
		}
	}
	grade := chooseHuntGrade(w1, w2, w3, w4, force)
	weight := int64(0)
	selectedGrade := grade
	for _, candidate := range []int64{grade, 3, 2, 1} {
		if candidate > grade {
			continue
		}
		if weight = s.sampleHuntBand(ctx, key, candidate, modeMult, scale, capG, c); weight > 0 {
			selectedGrade = candidate
			break
		}
	}
	if weight <= 0 {
		// 档位线缺失时仍保留旧模型，避免未知鱼种完全不咬钩。
		fallbackMult := 1.0
		if modeMult > 1 {
			fallbackMult = math.Min(modeMult, 2)
		}
		weight = s.sampleWeightDB(ctx, key, fallbackMult, scale, capG, c)
		selectedGrade = s.rollGradeForRoll(ctx, key, weight, c)
	}
	lines := s.cachedSettleStatics(ctx, c, key)
	if weight > 0 && lines[2] > 0 {
		if maxBlue := int64(math.Round(float64(lines[2]) * 1.60)); weight > maxBlue {
			weight = maxBlue
		}
	}
	if capG > 0 && weight > int64(capG) {
		weight = int64(capG)
	}
	return weight, selectedGrade, readiness
}

func (s *Server) recordHuntBite(ctx context.Context, h *fishHuntState) {
	if !s.fishHuntEnabled() || h == nil {
		return
	}
	h.ValidBites++
	s.saveFishHuntState(ctx, h)
}

// recordHuntCatch 只对同一 FishGUID 计一次，避免 14/4 重放重复推进保底。
func (s *Server) recordHuntCatch(ctx context.Context, uid int64, loc string, spot uint64, f *RolledFish) {
	if f == nil || !s.fishHuntEnabled() {
		return
	}
	h := s.loadFishHuntState(ctx, uid, loc, spot)
	if h.LastCatchGUID == f.FishGUID {
		return
	}
	h.LastCatchGUID = f.FishGUID
	consumeFishHuntCompensation(h, time.Now().Unix())
	if f.Grade >= 3 {
		h.Pity = 0
		h.BigMiss = 0
		if f.Grade >= 4 {
			h.ActiveSeconds *= .2
			h.ValidBites = int64(float64(h.ValidBites) * .2)
		} else {
			h.ActiveSeconds *= .5
			h.ValidBites = int64(float64(h.ValidBites) * .5)
		}
	} else if f.HuntStageKnown && f.HuntStage >= fishHuntGather {
		h.Pity++
	}
	s.saveFishHuntState(ctx, h)
}

func (s *Server) recordHuntEscape(ctx context.Context, uid int64, loc string, spot uint64, f *RolledFish) {
	if f == nil || f.Grade < 3 || !s.fishHuntEnabled() {
		return
	}
	h := s.loadFishHuntState(ctx, uid, loc, spot)
	h.BigMiss++
	h.CompFish = 3
	h.CompGrade = f.Grade
	h.CompUntil = float64(time.Now().Unix() + fishHuntCompWindowSeconds)
	if f.Grade >= 4 {
		h.CompFish = 4
	}
	s.saveFishHuntState(ctx, h)
	s.log.Info("大鱼脱钩补偿", "uid", uid, "loc", loc, "spot", spot,
		"grade", f.Grade, "big_miss", h.BigMiss, "until", h.CompUntil)
}

func consumeFishHuntCompensation(h *fishHuntState, now int64) {
	if h == nil || h.CompFish <= 0 {
		return
	}
	if h.CompUntil <= float64(now) {
		h.CompFish, h.CompGrade, h.CompUntil = 0, 0, 0
		return
	}
	h.CompFish--
	if h.CompFish <= 0 {
		h.CompFish, h.CompGrade, h.CompUntil = 0, 0, 0
	}
}

func fishHuntStageName(stage int) string {
	switch stage {
	case fishHuntGather:
		return "聚鱼"
	case fishHuntActive:
		return "发窝"
	case fishHuntGiant:
		return "巨物发窝"
	default:
		return "日常"
	}
}
