package gameserver

import (
	"math"
	"testing"
)

func TestFishQueryCoordinateProjection(t *testing.T) {
	for _, frame := range []fishQueryMapFrame{{}, {originX: 120, originZ: -75}} {
		mx, mz, ok := frame.project(425+frame.originX, 495+frame.originZ)
		if !ok || mx != 85 || mz != 99 {
			t.Fatalf("project(%+v) = %g:%g, %v", frame, mx, mz, ok)
		}
		x, z, ok := frame.unproject(mx, mz)
		if !ok || x != 425+frame.originX || z != 495+frame.originZ {
			t.Fatalf("unproject(%+v) = %g:%g, %v", frame, x, z, ok)
		}
		// A 3:4 map-coordinate displacement is 25 world units, independent
		// of scene origin. This is horizontal distance, not line length.
		x2, z2, _ := frame.unproject(mx+3, mz+4)
		if math.Hypot(x2-x, z2-z) != 25 {
			t.Fatal("coordinate scaling changed horizontal distance")
		}
	}
}

func TestFishQueryHUDRounding(t *testing.T) {
	for _, tc := range []struct {
		relative float32
		want     int64
	}{{425.65, 85}, {495.08, 99}, {2.5, 0}, {7.5, 2}, {12.5, 2}, {-2.5, 0}, {-7.5, -2}, {-12.5, -2}} {
		got, ok := fishQueryHUDCoordinate(tc.relative)
		if !ok || got != tc.want {
			t.Fatalf("HUD(%g) = %d, %v; want %d", tc.relative, got, ok, tc.want)
		}
	}
}

func TestFishQueryAreaBoundsMatchProduction(t *testing.T) {
	for _, tc := range []struct {
		x, z, minX, minZ float64
	}{{425, 495, 400, 480}, {40, 80, 40, 80}, {39.999, 79.999, 0, 40}, {-0.001, -40, -40, -40}, {-40.001, -80.001, -80, -120}} {
		minX, minZ, maxX, maxZ, ok := fishQueryAreaBounds(tc.x, tc.z)
		if !ok || minX != tc.minX || minZ != tc.minZ || maxX-minX != 40 || maxZ-minZ != 40 {
			t.Fatalf("bounds(%g,%g) = %g,%g .. %g,%g, %v", tc.x, tc.z, minX, minZ, maxX, maxZ, ok)
		}
		area := func(x, z float64) string {
			return fishArea("test", fishRollContext{x: x, z: z, hasPos: true})
		}
		want := area(tc.x, tc.z)
		if area(minX, minZ) != want || area(maxX-1e-6, maxZ-1e-6) != want || area(maxX, minZ) == want || area(minX, maxZ) == want {
			t.Fatalf("bounds do not match production area %s", want)
		}
		frame := fishQueryMapFrame{originX: 2.5, originZ: -7.5}
		loX, loZ, _ := frame.project(minX, minZ)
		hiX, hiZ, _ := frame.project(maxX, maxZ)
		if hiX-loX != 8 || hiZ-loZ != 8 {
			t.Fatal("40-world-unit area is not 8 map units wide")
		}
	}
}

func TestFishQueryCoordinatesRejectInvalidNumbers(t *testing.T) {
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), 1e8, -1e8} {
		for _, frame := range []fishQueryMapFrame{{originX: v}, {originZ: v}} {
			if _, _, ok := frame.project(1, 1); ok {
				t.Fatal("accepted invalid origin")
			}
			if _, _, ok := frame.unproject(1, 1); ok {
				t.Fatal("accepted invalid origin on inverse projection")
			}
		}
		frame := fishQueryMapFrame{}
		for _, p := range [][2]float64{{v, 1}, {1, v}} {
			if _, _, ok := frame.project(p[0], p[1]); ok {
				t.Fatal("accepted invalid position")
			}
			if _, _, ok := frame.unproject(p[0], p[1]); ok {
				t.Fatal("accepted invalid map coordinate")
			}
			if _, _, _, _, ok := fishQueryAreaBounds(p[0], p[1]); ok {
				t.Fatal("accepted invalid area position")
			}
		}
		if _, ok := fishQueryHUDCoordinate(float32(v)); ok {
			t.Fatal("accepted invalid HUD position")
		}
	}
	if _, _, ok := (fishQueryMapFrame{}).unproject(1e7, 0); ok {
		t.Fatal("inverse projection exceeded the supported world-coordinate range")
	}
}
