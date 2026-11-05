package gameserver

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestFishEcologySeedsSpeciesAndMatchesWaterLayers(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	staticPath := filepath.Join("..", "..", "server_data", "static.sqlite3")
	if _, err := db.SQL.Exec(`ATTACH DATABASE ? AS src`, staticPath); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT OR IGNORE INTO species_weight SELECT * FROM src.species_weight`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`DETACH DATABASE src`); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.ensureFishEcology(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM fish_ecology`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count < 200 {
		t.Fatalf("鱼种生态档案只初始化了 %d 条", count)
	}

	surface := fishRollContext{rigType: 1, depthCM: 50, now: time.Now()}
	bottom := fishRollContext{rigType: 12, depthCM: 500, now: time.Now()}
	pikeSurface := srv.fishBiteEnvironmentWeight(ctx, "n.pike", surface)
	pikeBottom := srv.fishBiteEnvironmentWeight(ctx, "n.pike", bottom)
	catSurface := srv.fishBiteEnvironmentWeight(ctx, "w_catfish", surface)
	catBottom := srv.fishBiteEnvironmentWeight(ctx, "w_catfish", bottom)
	if pikeSurface <= catSurface {
		t.Fatalf("上层水层应更偏向掠食鱼: pike=%f catfish=%f", pikeSurface, catSurface)
	}
	if catBottom <= pikeBottom {
		t.Fatalf("底层应更偏向鲶鱼: catfish=%f pike=%f", catBottom, pikeBottom)
	}
}

func TestEffectiveBaitDepthTracksFloatSinkingAndLureLayer(t *testing.T) {
	now := time.Now()
	floatState := &FishState{
		RigType: 1,
		DepthCM: 300,
		CastTs:  now.Add(-4 * time.Second),
		FallTs:  now.Add(-4 * time.Second),
	}
	if got := floatState.effectiveBaitDepthCMLocked(now); got != 100 {
		t.Fatalf("手杆下沉 4 秒后饵深=%d，期望 100cm", got)
	}
	floatState.FallTs = now.Add(-20 * time.Second)
	if got := floatState.effectiveBaitDepthCMLocked(now); got != 300 {
		t.Fatalf("手杆应停在调漂目标 300cm，实得 %d", got)
	}
	lureState := &FishState{RigType: 5, LureLayer: .8}
	if got := lureState.effectiveBaitDepthCMLocked(now); got != 400 {
		t.Fatalf("路亚水层 0.8 应折算 400cm，实得 %d", got)
	}
	if got := classifyLureMotion(0x800); got != "bottom_motionless" {
		t.Fatalf("0x800 应识别为底层静止，实得 %q", got)
	}
	if got := lureLayerFromMotion(0x1000, .5); got != .08 {
		t.Fatalf("水面运动应映射到浅层，实得 %f", got)
	}
}
