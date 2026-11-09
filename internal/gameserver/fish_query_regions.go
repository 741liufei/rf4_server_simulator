package gameserver

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

var errFishQueryRegionMapUnsupported = errors.New("暂不支持该地图的区域查询")

const (
	fishQueryCoarseRegionLimit = 32
	fishQueryRegionAxisLimit   = 76 // Includes the offset 3000-unit islands terrain.
)

type fishQueryRegionScene struct {
	originX, originZ, size float64
	paper                  *fishQueryPaperMap
}

// fishQueryPaperMap stores numeric calibration, not image content. Client map
// pixels are scene-relative world coordinates * scale + offset. Preserve the
// serialized float32 calibration; use float64 for continuous scan bounds.
type fishQueryPaperMap struct {
	offsetX, offsetZ, scale float32
	width, height           float64
}

// Only scenes with verified display origins, TerrainData extents and paper-map
// calibration are included. The islands use their outer main terrain; the inner terrain is
// contained within it and must not be scanned again. TerrainData.size is already
// in world units; do not multiply it by Transform scale or rotate its X/Z bounds.
// The scan intersects terrain with the paper map; neither is a water mask.
// See docs/fish-query-coordinates.md; other maps must not inherit a default.
var fishQueryRegionScenes = map[string]fishQueryRegionScene{
	"level_001_lake":            {size: 600, paper: &fishQueryPaperMap{-745, -835, 5, 2048, 2048}},
	"level_002_kirzah_river":    {originX: -500, originZ: -500, size: 1000, paper: &fishQueryPaperMap{-945, -1113, 4.2, 2048, 2048}},
	"level_003_viiveri_lake":    {originX: -500, originZ: -500.0000305175781, size: 1000, paper: &fishQueryPaperMap{-1116, -1115, 4.24, 2048, 2048}},
	"level_004_torfyanoe_ozero": {size: 400, paper: &fishQueryPaperMap{56, 37, 4.824, 2048, 2048}},
	"level_005_volhov":          {size: 1000, paper: &fishQueryPaperMap{77, 89, 1.88, 2048, 2048}},
	"level_006_ladoga":          {size: 500, paper: &fishQueryPaperMap{-102, 8, 4.22, 2048, 2048}},
	"level_007_grass_lake":      {size: 500, paper: &fishQueryPaperMap{-108.4, -72.8, 4.54, 2048, 2048}},
	"level_008_sura_river":      {size: 800, paper: &fishQueryPaperMap{61, 28.6, 2.445, 2048, 2048}},
	"level_009_ahtuba":          {size: 1000, paper: &fishQueryPaperMap{62, 74, 1.91, 2048, 2048}},
	"level_010_belaya":          {size: 500, paper: &fishQueryPaperMap{15, 145, 3.56, 2048, 2048}},
	"level_011_ladoga_02":       {originX: -500, originZ: -500, size: 3000, paper: &fishQueryPaperMap{78, 80, .63, 2048, 2048}},
	"level_013_sev_don":         {size: 1000, paper: &fishQueryPaperMap{-55, -45, 2.15, 2048, 2048}},
	"level_014_carp_lake":       {size: 1200, paper: &fishQueryPaperMap{15, -185, 2.1, 2048, 2048}},
	"level_015_tunguska":        {size: 1200, paper: &fishQueryPaperMap{80, 80, 1.57, 2048, 2048}},
	"level_016_yama":            {size: 1500, paper: &fishQueryPaperMap{78, 74, 1.27, 2048, 2048}},
	"level_018_moskitnoe":       {originX: -128, originZ: -128, size: 512, paper: &fishQueryPaperMap{-906.3, -884.7, 7.58, 2048, 2048}},
	"level_019_american_pond":   {originX: -127.4000015258789, originZ: -126.9000015258789, size: 1000, paper: &fishQueryPaperMap{-619, -619, 3.29, 2048, 2048}},
}

// FishQueryRegion is a half-open rectangle in continuous HUD/map coordinates,
// not a selected cast point or a guarantee of water/shore access.
type FishQueryRegion struct {
	MinX float64 `json:"min_x"`
	MinZ float64 `json:"min_z"`
	MaxX float64 `json:"max_x"`
	MaxZ float64 `json:"max_z"`
}

type fishQueryRegionalWeight struct {
	key                string
	weight, acceptance float64
}

type fishQueryRegions struct {
	areas                             []string
	bounds                            []FishQueryRegion
	now                               time.Time
	enabled                           bool
	factors                           map[string][]float64
	scanned                           int
	worldBounds                       [4]float64
	minCellX, minCellZ, width, height int
	waterFiltered                     bool
}

func newFishQueryRegions(loc string, scene fishQueryRegionScene, now time.Time, enabled bool) (*fishQueryRegions, error) {
	minWorldX, minWorldZ := scene.originX, scene.originZ
	maxWorldX, maxWorldZ := scene.originX+scene.size, scene.originZ+scene.size
	minCellX, minCellZ := math.Floor(scene.originX/fishQueryAreaSize), math.Floor(scene.originZ/fishQueryAreaSize)
	maxCellX, maxCellZ := math.Ceil(maxWorldX/fishQueryAreaSize), math.Ceil(maxWorldZ/fishQueryAreaSize)
	if !validFishQueryCoordinate(scene.size) || scene.size <= 0 ||
		!validFishQueryCoordinate(scene.originX) || !validFishQueryCoordinate(scene.originZ) ||
		!validFishQueryCoordinate(maxWorldX) || !validFishQueryCoordinate(maxWorldZ) ||
		maxWorldX <= scene.originX || maxWorldZ <= scene.originZ ||
		maxCellX-minCellX > fishQueryRegionAxisLimit || maxCellZ-minCellZ > fishQueryRegionAxisLimit {
		return nil, fmt.Errorf("区域扫描范围无效")
	}
	if p := scene.paper; p != nil {
		scale, offsetX, offsetZ := float64(p.scale), float64(p.offsetX), float64(p.offsetZ)
		if !validFishQueryCoordinate(scale) || scale <= 0 ||
			!validFishQueryCoordinate(offsetX) || !validFishQueryCoordinate(offsetZ) ||
			!validFishQueryCoordinate(p.width) || !validFishQueryCoordinate(p.height) || p.width <= 0 || p.height <= 0 {
			return nil, fmt.Errorf("纸地图范围无效")
		}
		// Intersect in world space before scoring. Do not clamp a winning result
		// afterwards: that would move its probability to a different fish cell.
		minWorldX = math.Max(minWorldX, scene.originX-offsetX/scale)
		minWorldZ = math.Max(minWorldZ, scene.originZ-offsetZ/scale)
		maxWorldX = math.Min(maxWorldX, scene.originX+(p.width-offsetX)/scale)
		maxWorldZ = math.Min(maxWorldZ, scene.originZ+(p.height-offsetZ)/scale)
		if minWorldX >= maxWorldX || minWorldZ >= maxWorldZ {
			return nil, fmt.Errorf("纸地图与地形没有有效交集")
		}
		minCellX, minCellZ = math.Floor(minWorldX/fishQueryAreaSize), math.Floor(minWorldZ/fishQueryAreaSize)
		maxCellX, maxCellZ = math.Ceil(maxWorldX/fishQueryAreaSize), math.Ceil(maxWorldZ/fishQueryAreaSize)
	}
	g := &fishQueryRegions{now: now, enabled: enabled, factors: map[string][]float64{},
		worldBounds: [4]float64{minWorldX, minWorldZ, maxWorldX, maxWorldZ},
		minCellX:    int(minCellX), minCellZ: int(minCellZ), width: int(maxCellX - minCellX), height: int(maxCellZ - minCellZ)}
	if !enabled {
		return g, nil
	}
	frame := fishQueryMapFrame{originX: scene.originX, originZ: scene.originZ}
	// Align with production's world grid, not the scene origin. An offset scene
	// or paper map can begin/end in a cell; clip both ends before projecting.
	for ix := minCellX; ix < maxCellX; ix++ {
		for iz := minCellZ; iz < maxCellZ; iz++ {
			x, z := math.Max(ix*fishQueryAreaSize, minWorldX), math.Max(iz*fishQueryAreaSize, minWorldZ)
			mx, mz, _ := frame.project(x, z)
			maxX, maxZ, _ := frame.project(math.Min((ix+1)*fishQueryAreaSize, maxWorldX), math.Min((iz+1)*fishQueryAreaSize, maxWorldZ))
			g.bounds = append(g.bounds, FishQueryRegion{MinX: mx, MinZ: mz, MaxX: maxX, MaxZ: maxZ})
			g.areas = append(g.areas, fishArea(loc, fishRollContext{x: x, z: z, hasPos: true}))
		}
	}
	g.scanned = len(g.areas)
	return g, nil
}

func (g *fishQueryRegions) distributionFactors(ctx context.Context, key string) ([]float64, error) {
	if factors, ok := g.factors[key]; ok {
		return factors, nil
	}
	factors := make([]float64, len(g.areas))
	for i, area := range g.areas {
		if i%64 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		factors[i] = distributionMultiplier(area, key, g.now)
	}
	g.factors[key] = factors
	return factors, nil
}

// shortlist ranks regions before bait evaluation. Its bait-free weight share
// is only a coarse score, not the final probability of a successful bite.
func (g *fishQueryRegions) shortlist(ctx context.Context, fish string, weights map[string]float64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !g.enabled {
		return nil
	}
	keys := make([]string, 0, len(weights))
	for key, weight := range weights {
		if weight > 0 {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	total := make([]float64, len(g.areas))
	for _, key := range keys {
		factors, err := g.distributionFactors(ctx, key)
		if err != nil {
			return err
		}
		for i, factor := range factors {
			total[i] += weights[key] * factor
		}
	}
	type scoredRegion struct {
		index int
		score float64
	}
	var ranked []scoredRegion
	if weights[fish] > 0 {
		for i, factor := range g.factors[fish] {
			if i%64 == 0 && ctx.Err() != nil {
				return ctx.Err()
			}
			if factor > 0 && total[i] > 0 {
				ranked = append(ranked, scoredRegion{i, weights[fish] * factor / total[i]})
			}
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	ranked = ranked[:min(len(ranked), fishQueryCoarseRegionLimit)]
	areas := make([]string, len(ranked))
	bounds := make([]FishQueryRegion, len(ranked))
	for i, row := range ranked {
		areas[i], bounds[i] = g.areas[row.index], g.bounds[row.index]
	}
	for key, factors := range g.factors {
		selected := make([]float64, len(ranked))
		for i, row := range ranked {
			selected[i] = factors[row.index]
		}
		g.factors[key] = selected
	}
	g.areas, g.bounds = areas, bounds
	return nil
}

// best applies the shared regional distribution to each bait's captured species
// weights. It compares normalized probabilities, not just target abundance.
// No SQL, production RNG or point-by-point ecology evaluation occurs here.
func (g *fishQueryRegions) best(ctx context.Context, q *fishQueryCapture) (*FishQueryRegion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !g.enabled {
		return nil, nil // uniform distribution: do not invent a winning location
	}
	q.chance, q.selection, q.fishShare = 0, 0, 0
	target := -1
	vectors := make([][]float64, len(q.regionalWeights))
	for j, row := range q.regionalWeights {
		if row.weight <= 0 {
			continue
		}
		if row.key == q.fish {
			target = j
		}
		factors, err := g.distributionFactors(ctx, row.key)
		if err != nil {
			return nil, err
		}
		vectors[j] = factors
	}
	if target < 0 || q.gradeShare <= 0 {
		return nil, nil
	}
	best := -1
	for i := range g.areas {
		if i%64 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		targetWeight := q.regionalWeights[target].weight * vectors[target][i]
		if targetWeight <= 0 {
			continue
		}
		var total, successful float64
		for j, row := range q.regionalWeights {
			if row.weight > 0 {
				weight := row.weight * vectors[j][i]
				total += weight
				successful += weight * row.acceptance
			}
		}
		selection := targetWeight / total
		chance := q.biteProbability * selection * q.acceptance * q.gradeShare
		if chance > q.chance {
			best, q.chance, q.selection = i, chance, selection
			q.fishShare = targetWeight * q.acceptance / successful
		}
	}
	if best < 0 {
		return nil, nil
	}
	region := g.bounds[best]
	return &region, nil
}
