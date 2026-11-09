package gameserver

import (
	"context"
	"math"
)

// A continuous weight law on the original natural range, already truncated by
// equipment. mass returns the probability integral on [lo,hi]. Keeping it as an
// interval integral avoids cancellation in rare blue-crown tails.
type fishWeightLaw struct {
	lower, upper float64
	scale, capG  float64
	mass         func(lo, hi float64) float64
	// Ratio mode may collapse a capped band to one integer weight.
	roundedSurvival func(minimumG int64) float64
}

func (d fishWeightLaw) survival(weight float64) float64 {
	if d.mass == nil || d.upper <= d.lower {
		return 0
	}
	total := d.mass(d.lower, d.upper)
	if total <= 0 || math.IsNaN(total) {
		return 0
	}
	if weight <= d.lower {
		return 1
	}
	if weight >= d.upper {
		return 0
	}
	return math.Max(0, math.Min(1, d.mass(weight, d.upper)/total))
}

func (d fishWeightLaw) retainedMass(minimumG int64, luck float64) float64 {
	if d.roundedSurvival != nil {
		q := d.roundedSurvival(minimumG)
		return q * (1 + clampFloat(luck, 0, 1)*(1-q))
	}
	if d.scale <= 0 || d.mass == nil || d.upper <= d.lower {
		return 0
	}
	// The game grades integer grams AFTER rounding and scaling.
	q := d.survival((float64(minimumG) - .5) / d.scale)
	p := math.Max(0, math.Min(1, luck))
	// Existing trophy luck chooses max(X1,X2) with probability p.
	return q * (1 + p*(1-q))
}

func normalIntervalMass(lo, hi float64) float64 {
	if hi <= lo {
		return 0
	}
	if lo >= 0 {
		return .5 * (math.Erfc(lo/math.Sqrt2) - math.Erfc(hi/math.Sqrt2))
	}
	if hi <= 0 {
		return .5 * (math.Erfc(-hi/math.Sqrt2) - math.Erfc(-lo/math.Sqrt2))
	}
	return .5 * (math.Erf(hi/math.Sqrt2) - math.Erf(lo/math.Sqrt2))
}

func (s *Server) poolWeightLaw(e fishPoolEntry, scale, capG float64, c fishRollContext) fishWeightLaw {
	minG, maxG := float64(e.minG), float64(e.maxG)
	mode := minG + (maxG-minG)*.3
	mode = math.Max(minG, math.Min(maxG, mode*s.baitWeightModeForRoll(e.species, c.baits, c)))
	upper := maxG
	if capG > 0 {
		upper = math.Min(upper, math.Min(capG, math.Floor(capG)+.5)/scale)
	}
	return fishWeightLaw{lower: minG, upper: upper, scale: scale, capG: capG, mass: func(lo, hi float64) float64 {
		var mass float64
		if lo < mode {
			end := math.Min(hi, mode)
			mass += (end - lo) * (end + lo - 2*minG) / ((maxG - minG) * (mode - minG))
		}
		if hi > mode {
			start := math.Max(lo, mode)
			mass += (hi - start) * (2*maxG - hi - start) / ((maxG - minG) * (maxG - mode))
		}
		return mass
	}}
}

// weightModelDB supplies the same deterministic parameters to sampling and inspection.
type fishWeightDBModel struct{ lower, upper, mu, sd float64 }

func (s *Server) weightModelDB(ctx context.Context, key string, modeMult, scale, capG float64, c fishRollContext) fishWeightDBModel {
	lines := s.cachedGradeLines(ctx, c, key)
	val, tro, sup := lines.val, lines.trophy, lines.blue
	var vf, trof, supf float64
	if val != nil {
		vf = float64(*val)
	}
	if tro != nil {
		trof = float64(*tro)
	}
	if sup != nil {
		supf = float64(*sup)
	}
	// 峰值 M：达标带中点（缺 val→tro*0.6→sup*0.3→250）
	M := 250.0
	switch {
	case val != nil && tro != nil && trof > vf:
		M = (vf + trof) / 2.0
	case tro != nil:
		M = trof * 0.6
	case sup != nil:
		M = supf * 0.3
	}
	if modeMult != 1.0 {
		M *= modeMult // 饵效命中：峰值锚点向奖杯线右移
	}
	// 整体峰值向右上抬 15%，让日常鱼获里更容易出现中大个体。
	M *= 1.15
	// 上限 hi：蓝冠线×1.25（缺 sup→tro*1.45→M*6.0）
	var hi float64
	switch {
	case sup != nil:
		hi = supf * 1.25
	case tro != nil:
		hi = trof * 1.45
	default:
		hi = M * 6.0
	}
	// 下限 lo：少量"太小/不达标"个体
	lo := M * 0.12
	if val != nil {
		lo = math.Min(lo, vf*0.2)
	}
	lo = math.Max(1.0, math.Min(lo, M*0.6))
	if hi <= M {
		hi = M * 1.6
	}
	hi = math.Max(hi, lo+1.0)
	sd := math.Max(0.20, (math.Log(hi)-math.Log(M))/weightSkewK)
	mu := math.Log(M) + sd*sd
	if capG > 0 {
		hi = math.Min(hi, capG/scale)
	}
	return fishWeightDBModel{lo, hi, mu, sd}
}

func (s *Server) weightLawDB(ctx context.Context, key string, modeMult, scale, capG float64, c fishRollContext) fishWeightLaw {
	if s.tunableF("grade_ratio_enabled", 0) == 1 {
		lines := s.cachedSettleStatics(ctx, c, key)
		model, handled := s.gradeRatioModel(cachedGradeLines{val: &lines[0], trophy: &lines[1], blue: &lines[2]}, modeMult, scale, capG)
		if handled {
			return fishWeightLaw{scale: scale, capG: capG, roundedSurvival: model.survival}
		}
	}
	model := s.weightModelDB(ctx, key, modeMult, scale, capG, c)
	if capG > 0 {
		model.upper = math.Min(model.upper, (math.Floor(capG)+.5)/scale)
	}
	return fishWeightLaw{lower: model.lower, upper: model.upper, scale: scale, capG: capG, mass: func(lo, hi float64) float64 {
		return normalIntervalMass((math.Log(lo)-model.mu)/model.sd, (math.Log(hi)-model.mu)/model.sd)
	}}
}

// survival uses the same usable bands and post-scale rounding/cap as sampling.
func (m fishGradeRatioModel) survival(minimumG int64) float64 {
	if m.total <= 0 || !finiteSonarNumber(m.total) {
		return 0
	}
	if m.capG > 0 && math.Round(m.capG) < float64(minimumG) {
		return 0
	}
	var mass float64
	threshold := (float64(minimumG) - .5) / m.scale
	for i, b := range m.bands {
		a, z := math.Max(1, b.lo), b.hi
		if m.capG > 0 {
			z = math.Min(z, m.capG/m.scale)
		}
		q := 0.0
		if z <= a*1.0001 {
			if math.Max(1, math.Round(a*m.scale)) >= float64(minimumG) {
				q = 1
			}
		} else if threshold <= a {
			q = 1
		} else if threshold < z {
			q = math.Log(z/threshold) / math.Log(z/a)
		}
		mass += m.weights[i] * q
	}
	return clampFloat(mass/m.total, 0, 1)
}
