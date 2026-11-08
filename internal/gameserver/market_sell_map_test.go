package gameserver

import (
	"math"
	"testing"

	"rf4server/internal/store"
)

// 同一批鱼在不同水域卖出，价格必须随该水域倍率变化。
func TestMarketPriceFollowsSellMap(t *testing.T) {
	e := &store.KeepnetEntry{Species: "bream", Cat: 3, WeightG: 6054}
	now := 1750000000.0
	lake := float64(marketPriceCentsForMap(e, now, "level_001_lake"))           // 0.831
	old := float64(marketPriceCentsForMap(e, now, "level_004_torfyanoe_ozero")) // 0.916
	sea := float64(marketPriceCentsForMap(e, now, "level_012_north_sea"))       // 1.000
	if lake <= 0 || old <= 0 || sea <= 0 {
		t.Fatalf("覆盖价缺失: lake=%v old=%v sea=%v", lake, old, sea)
	}
	if !(lake < old && old < sea) {
		t.Fatalf("水域价差未生效: lake=%v old=%v sea=%v", lake, old, sea)
	}
	if got := old / sea; math.Abs(got-0.916) > 0.01 {
		t.Fatalf("老奥/海图 = %.4f，期望 0.916", got)
	}
	if got := lake / sea; math.Abs(got-0.831) > 0.01 {
		t.Fatalf("1 图/海图 = %.4f，期望 0.831", got)
	}
}

// 询价与结算必须使用同一地图，避免出现"看 48.18 入账 44.13"。
func TestMarketRateAndSellUseSameMap(t *testing.T) {
	e := &store.KeepnetEntry{Species: "bream", Cat: 3, WeightG: 6054}
	now := 1750000000.0
	for _, level := range []string{"level_001_lake", "level_004_torfyanoe_ozero", "level_012_north_sea"} {
		rate := marketPriceCentsForMap(e, now, level)
		sell := marketPriceCentsForMap(e, now, level)
		if rate != sell {
			t.Fatalf("%s 询价 %d 与结算 %d 不一致", level, rate, sell)
		}
	}
}
