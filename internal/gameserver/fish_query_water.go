package gameserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
)

var errFishQueryWaterInvalid = errors.New("水陆数据无效，请重新生成并重启游戏服务")

type fishQueryWaterGrid struct {
	Bounds  []float64 `json:"bounds"`
	MinX    int       `json:"min_x"`
	MinZ    int       `json:"min_z"`
	Width   int       `json:"width"`
	Height  int       `json:"height"`
	Samples []int     `json:"samples"`
}

type fishQueryWaterTable struct {
	SchemaVersion int                            `json:"schema_version"`
	CellSize      int                            `json:"cell_size"`
	Sampling      string                         `json:"sampling"`
	Maps          map[string]*fishQueryWaterGrid `json:"maps"`
}

func parseFishQueryWater(data []byte) (*fishQueryWaterTable, error) {
	var table fishQueryWaterTable
	if err := json.Unmarshal(data, &table); err != nil {
		return nil, fmt.Errorf("%w: %v", errFishQueryWaterInvalid, err)
	}
	if table.SchemaVersion != 1 || table.CellSize != fishQueryAreaSize || table.Sampling != "center_corners_any" || len(table.Maps) == 0 {
		return nil, fmt.Errorf("%w: 文件版本或取样规则不匹配", errFishQueryWaterInvalid)
	}
	for key, grid := range table.Maps {
		if grid == nil || len(grid.Bounds) != 4 || grid.Width < 1 || grid.Height < 1 || grid.Width > fishQueryRegionAxisLimit || grid.Height > fishQueryRegionAxisLimit || len(grid.Samples) != grid.Width*grid.Height {
			return nil, fmt.Errorf("%w: %s 网格尺寸无效", errFishQueryWaterInvalid, key)
		}
		for _, value := range grid.Bounds {
			if !validFishQueryCoordinate(value) {
				return nil, fmt.Errorf("%w: %s 边界无效", errFishQueryWaterInvalid, key)
			}
		}
		b := grid.Bounds
		if b[0] >= b[2] || b[1] >= b[3] || int(math.Floor(b[0]/fishQueryAreaSize)) != grid.MinX || int(math.Floor(b[1]/fishQueryAreaSize)) != grid.MinZ || int(math.Ceil(b[2]/fishQueryAreaSize))-grid.MinX != grid.Width || int(math.Ceil(b[3]/fishQueryAreaSize))-grid.MinZ != grid.Height {
			return nil, fmt.Errorf("%w: %s 边界与网格不一致", errFishQueryWaterInvalid, key)
		}
		for _, sample := range grid.Samples {
			if sample < 0 || sample > 31 {
				return nil, fmt.Errorf("%w: %s 五点标记无效", errFishQueryWaterInvalid, key)
			}
		}
	}
	return &table, nil
}

func readFishQueryWater(path string) (*fishQueryWaterTable, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	const limit = 2 << 20
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("%w: 文件过大", errFishQueryWaterInvalid)
	}
	return parseFishQueryWater(data)
}

var (
	fishQueryWaterOnce  sync.Once
	fishQueryWaterData  *fishQueryWaterTable
	fishQueryWaterError error
)

// This optional, locally generated table is immutable for the process lifetime.
// Never read client assets or modify the production water-entry checks here.
func loadFishQueryWater() (*fishQueryWaterTable, error) {
	fishQueryWaterOnce.Do(func() {
		paths := []string{"data/fish_query_water.json", "server_data/fish_query_water.json"}
		explicit := os.Getenv("RF4_FISH_QUERY_WATER_FILE")
		if explicit != "" {
			paths = []string{explicit}
		} else if exe, err := os.Executable(); err == nil {
			dir := filepath.Dir(exe)
			paths = append(paths, filepath.Join(dir, "data", "fish_query_water.json"), filepath.Join(dir, "..", "server_data", "fish_query_water.json"))
		}
		for _, path := range paths {
			table, err := readFishQueryWater(path)
			if errors.Is(err, os.ErrNotExist) && explicit == "" {
				continue
			}
			if err != nil {
				fishQueryWaterError = fmt.Errorf("%w: %v", errFishQueryWaterInvalid, err)
			} else {
				fishQueryWaterData = table
			}
			return
		}
	})
	return fishQueryWaterData, fishQueryWaterError
}

func (g *fishQueryRegions) filterWater(grid *fishQueryWaterGrid) error {
	if !g.enabled || grid == nil {
		return nil
	}
	if len(grid.Bounds) != 4 || grid.MinX != g.minCellX || grid.MinZ != g.minCellZ || grid.Width != g.width || grid.Height != g.height || len(grid.Samples) != len(g.areas) || len(g.factors) != 0 {
		return fmt.Errorf("%w: 地图网格不匹配", errFishQueryWaterInvalid)
	}
	for i, value := range grid.Bounds {
		if !validFishQueryCoordinate(value) || math.Abs(value-g.worldBounds[i]) > 1e-7 {
			return fmt.Errorf("%w: 地图裁剪范围已变化", errFishQueryWaterInvalid)
		}
	}
	areas := make([]string, 0, len(g.areas))
	bounds := make([]FishQueryRegion, 0, len(g.bounds))
	for i, sample := range grid.Samples {
		if sample != 0 {
			areas = append(areas, g.areas[i])
			bounds = append(bounds, g.bounds[i])
		}
	}
	g.areas, g.bounds, g.scanned = areas, bounds, len(areas)
	g.waterFiltered = true
	return nil
}
