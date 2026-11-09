package gameserver

import "math"

const (
	fishQueryWorldUnitsPerMapUnit = 5.0
	fishQueryAreaSize             = 40.0
	fishQueryCoordinateLimit      = 1e7
)

// fishQueryMapFrame requires a verified scene origin in the same coordinate
// system as the supplied world positions. A zero origin is not a map default.
type fishQueryMapFrame struct {
	originX, originZ float64
}

func validFishQueryCoordinate(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && math.Abs(v) <= fishQueryCoordinateLimit
}

// project keeps fractional map coordinates for half-open area bounds. The HUD
// rounds the scene-relative float32 coordinate / 5 to the nearest even integer;
// rounding a bound would incorrectly include parts of a neighbouring area.
func (f fishQueryMapFrame) project(x, z float64) (mx, mz float64, ok bool) {
	if !validFishQueryCoordinate(x) || !validFishQueryCoordinate(z) ||
		!validFishQueryCoordinate(f.originX) || !validFishQueryCoordinate(f.originZ) {
		return 0, 0, false
	}
	return (x - f.originX) / fishQueryWorldUnitsPerMapUnit, (z - f.originZ) / fishQueryWorldUnitsPerMapUnit, true
}

// unproject maps the centre value of a displayed coordinate back to world
// space, not the exact position of a player whose HUD has already rounded it.
func (f fishQueryMapFrame) unproject(mx, mz float64) (x, z float64, ok bool) {
	if !validFishQueryCoordinate(mx) || !validFishQueryCoordinate(mz) ||
		!validFishQueryCoordinate(f.originX) || !validFishQueryCoordinate(f.originZ) {
		return 0, 0, false
	}
	x, z = mx*fishQueryWorldUnitsPerMapUnit+f.originX, mz*fishQueryWorldUnitsPerMapUnit+f.originZ
	return x, z, validFishQueryCoordinate(x) && validFishQueryCoordinate(z)
}

func fishQueryHUDCoordinate(relative float32) (int64, bool) {
	if !validFishQueryCoordinate(float64(relative)) {
		return 0, false
	}
	return int64(math.RoundToEven(float64(relative / fishQueryWorldUnitsPerMapUnit))), true
}

// fishQueryAreaBounds uses exactly the floor-based partition used by fishArea.
// Both axes are [minimum, maximum), including for negative world coordinates.
func fishQueryAreaBounds(x, z float64) (minX, minZ, maxX, maxZ float64, ok bool) {
	if !validFishQueryCoordinate(x) || !validFishQueryCoordinate(z) {
		return 0, 0, 0, 0, false
	}
	minX = math.Floor(x/fishQueryAreaSize) * fishQueryAreaSize
	minZ = math.Floor(z/fishQueryAreaSize) * fishQueryAreaSize
	return minX, minZ, minX + fishQueryAreaSize, minZ + fishQueryAreaSize, true
}
