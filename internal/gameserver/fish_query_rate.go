package gameserver

import (
	"math"
	"sort"
	"time"
)

// meanBiteDelay averages the production delay AFTER applying its clamps.
// It does not sample RNG or include client wait, occupied rods or landing time.
func meanBiteDelay(cfg biteDelaySettings, lure bool, weather, speed float64) float64 {
	if cfg.fixed > 0 {
		return math.Max(.05, cfg.fixed)
	}
	if !lure {
		var sum float64
		for _, sample := range officialPushDelayEligible {
			sum += standardBiteDelay(sample*weather, speed, cfg.maxDelay, cfg.scale)
		}
		if len(officialPushDelayEligible) > 0 {
			return sum / float64(len(officialPushDelayEligible))
		}
		// Same rounded uniform bands as the production fallback.
		for _, band := range pushDelayBands {
			for cent := int(math.Floor(band.lo * 100)); cent <= int(math.Ceil(band.hi*100)); cent++ {
				x := float64(cent) / 100
				width := math.Max(0, math.Min(band.hi, x+.005)-math.Max(band.lo, x-.005))
				sum += band.p * width / (band.hi - band.lo) * standardBiteDelay(x*weather, speed, cfg.maxDelay, cfg.scale)
			}
		}
		return sum
	}
	// Uniform [1.5,6], transformed by piecewise-linear clamps. Integration at
	// each segment midpoint is exact; averaging the raw window is not.
	points := []float64{1.5, 6}
	k := weather * speed
	for _, x := range []float64{.8 / k, cfg.lureMax / k, .8 / (k * cfg.scale)} {
		if x > 1.5 && x < 6 {
			points = append(points, x)
		}
	}
	sort.Float64s(points)
	var sum float64
	for i := 1; i < len(points); i++ {
		a, b := points[i-1], points[i]
		sum += (b - a) * lureBiteDelay((a+b)/2*weather, speed, cfg.lureMax, cfg.scale)
	}
	return sum / 4.5
}

func (s *Server) fishQueryMeanDelay(st *FishState, weather fishingWeather, at time.Time) float64 {
	cfg := s.effectiveBiteDelaySettings(st)
	if cfg.fixed > 0 {
		return meanBiteDelay(cfg, false, 1, 1)
	}
	return meanBiteDelay(cfg, rigTypeLureFamily[st.RigType], s.weatherBiteDelayFactor(st, weather, at), st.Bonus.BiteSpeed)
}

func (p *FishQueryPlan) setTheoreticalRate(delay float64) {
	if !finiteSonarNumber(delay) || delay <= 0 || !finiteSonarNumber(p.Chance) {
		return
	}
	p.MeanDelaySeconds = delay
	p.TheoreticalPerMinute = 60 * clampFloat(p.Chance, 0, 1) / delay
	p.TheoreticalPerHour = 60 * p.TheoreticalPerMinute
}

func fishQueryPlanBetter(a, b FishQueryPlan) bool {
	if math.Abs(a.TheoreticalPerHour-b.TheoreticalPerHour) < 1e-12 {
		return a.WaitSeconds < b.WaitSeconds
	}
	return a.TheoreticalPerHour > b.TheoreticalPerHour
}
