package gameserver

import "math"

type fishQueryCapture struct {
	fish                                                 string
	grade                                                int64
	selection, acceptance, gradeShare, fishShare, chance float64
	regional                                             bool
	regionalWeights                                      []fishQueryRegionalWeight
	biteProbability                                      float64
}

// captureFishQuery stops before production RNG, fish creation and bait use.
func (s *Server) captureFishQuery(keys []string, weights []float64, interests map[string]float64, strict bool, bonus fishingSkillBonus, c fishRollContext, dbFamily bool, lawFor func(string) fishWeightLaw) {
	q := c.query
	valid, accepts := make([]float64, len(keys)), make([]float64, len(keys))
	var total, successful float64
	for i, key := range keys {
		if weights[i] <= 0 || dbFamily && lawFor(key).retainedMass(1, 0) <= 0 {
			continue
		}
		valid[i], accepts[i] = weights[i], 1
		if c.forRig {
			interest := 1.0
			if strict {
				interest = interests[key]
			}
			accepts[i] = happyHourBiteChance(fishingBaitInterest(interest, bonus), c.happyBite) * clampFloat(s.weatherFishActivity(key, c), 0, 1)
		}
		total += valid[i]
		successful += valid[i] * accepts[i]
	}
	if total <= 0 {
		return
	}
	for i, key := range keys {
		if key != q.fish || valid[i] <= 0 {
			continue
		}
		minimum := s.fishQueryMinimumWeight(key, q.grade, c)
		q.selection, q.acceptance = valid[i]/total, accepts[i]
		if minimum > 0 {
			q.gradeShare = lawFor(key).retainedMass(minimum, bonus.TrophyLuck)
		}
		if successful > 0 {
			q.fishShare = valid[i] * accepts[i] / successful
		}
		p := s.biteProbabilityForLoc(c.loc)
		if strict || s.tunableF("fish_weather_enabled", 0) == 1 {
			p = math.Min(.95, p)
		}
		q.chance = p * q.selection * q.acceptance * q.gradeShare
		if q.regional {
			q.biteProbability = p
			q.regionalWeights = make([]fishQueryRegionalWeight, len(keys))
			for j, key := range keys {
				q.regionalWeights[j] = fishQueryRegionalWeight{key: key, weight: valid[j], acceptance: accepts[j]}
			}
		}
		return
	}
}

func (s *Server) fishQueryMinimumWeight(key string, grade int64, c fishRollContext) int64 {
	lines := s.cachedSettleStatics(s.dbCtx(), c, key)
	var result int64
	for _, weight := range []int64{1, max(1, lines[0]), max(1, lines[1]), max(1, lines[2])} {
		if gradeFromLines(&lines[0], &lines[1], &lines[2], weight) >= grade && (result == 0 || weight < result) {
			result = weight
		}
	}
	return result
}
