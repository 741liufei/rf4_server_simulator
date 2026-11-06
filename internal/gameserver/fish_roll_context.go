package gameserver

import (
	"hash/fnv"
	"time"
)

// fishRollContext 只传递本次判鱼的区域和钩上饵，不改动存档格式。
type fishRollContext struct {
	x, z       float64
	hasPos     bool
	spot       uint64
	loc        string
	now        time.Time
	baits      []string
	forRig     bool
	depthCM    int64
	lureLayer  float64
	lureAction string
	lureMask   uint32
	lureSpeed  int64
	weather    fishingWeather
	rigType    int64
	happyBite  float64
	biteRoll   func() float64
	cache      *fishingRollCache
	skill      fishingSkillBonus
	spotEnv    fishSpotEnvironment
	runtime    *fishRollRuntime
}

type fishRollRuntime struct {
	lure      lureAttributes
	lureKnown bool
	// levelChecked 保证一次判鱼只解析一次地点与海图区号。
	levelChecked bool
	// chartKnown 表示该地点有没有海图（目前只有挪威海）；zoneKnown 表示本次
	// 落点能否定位到具体区号。两者共同决定海图条件算不算有证据。
	chartKnown bool
	zone       int
	zoneKnown  bool
	spotPref   map[string]float64
	spotData   bool
}

func rollContext(contexts []fishRollContext) fishRollContext {
	var c fishRollContext
	if len(contexts) > 0 {
		c = contexts[0]
	}
	if c.now.IsZero() {
		c.now = time.Now()
	}
	if c.cache == nil {
		c.cache = newFishingRollCache()
	}
	return c
}

func fishVariation(key string) float64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return float64(h.Sum64()%10000) / 10000
}
