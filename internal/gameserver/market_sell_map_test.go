package gameserver

import (
	"context"
	"math"
	"testing"
	"time"

	"rf4server/internal/proto"
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

// 20/1 与 20/2 必须按玩家当前地图取价，而不是按鱼的 keep_loc。
// 这里让鱼的钓获地图与当前地图相反，并同时核对实际入账金额。
func TestMarketRateAndSellFollowCurrentMap(t *testing.T) {
	srv, db := newEconomyTxTestServer(t)
	const uid = int64(7201)
	ctx := context.Background()
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	now := float64(time.Now().Unix())
	insert := func(guid, keepLoc, keepLevel string) {
		t.Helper()
		if _, err := db.SQL.Exec(`INSERT INTO keepnet_items(echo_guid,uid,species,name_zh,weight_g,length_m,cat,keep_loc,keep_spot,keep_bait,keep_level,ts)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, guid, uid, "bream", "欧鳊", 6054, 0.2, 3, keepLoc, 1, "worm", keepLevel, now); err != nil {
			t.Fatal(err)
		}
	}
	ratePrice := func(guid string) int64 {
		t.Helper()
		body, ok := hMarketRate(economyTestCtx(srv, uid, proto.Args524(proto.Guid(guid))))
		if !ok {
			t.Fatal("20/1 未处理")
		}
		v, _, err := proto.DecodeValue(body, 0, srv.Table())
		if err != nil || v.Kind != proto.KindArgs || len(v.Items) != 1 || v.Items[0].Field(1).Num == nil {
			t.Fatalf("20/1 响应无效: %+v err=%v", v, err)
		}
		return int64(*v.Items[0].Field(1).Num)
	}
	sellPrice := func(guid string) int64 {
		t.Helper()
		before, err := db.GetProfile(ctx, uid)
		if err != nil {
			t.Fatal(err)
		}
		body, ok := hMarketSell(economyTestCtx(srv, uid, proto.Args524(proto.Guid(guid))))
		if !ok || len(body) == 0 || body[0] == 0x0F {
			t.Fatalf("20/2 未成功: ok=%v body=%x", ok, body)
		}
		after, err := db.GetProfile(ctx, uid)
		if err != nil {
			t.Fatal(err)
		}
		return after.Silver - before.Silver
	}

	const lakeFish = "d1000000000000000000000000000000"
	const seaFish = "d2000000000000000000000000000000"
	insert(lakeFish, "loc1_02", "level_001_lake")
	srv.setLevel(uid, "level_012_north_sea")
	e := &store.KeepnetEntry{Species: "bream", Cat: 3, WeightG: 6054}
	wantSea := marketPriceCentsForMap(e, now, "level_012_north_sea")
	if got := ratePrice(lakeFish); got != wantSea {
		t.Fatalf("海图询价=%d，期望卖鱼地图价 %d", got, wantSea)
	}
	if got := sellPrice(lakeFish); got != wantSea {
		t.Fatalf("海图入账=%d，期望询价 %d", got, wantSea)
	}

	insert(seaFish, "loc1_02", "level_012_north_sea")
	srv.setLevel(uid, "level_001_lake")
	wantLake := marketPriceCentsForMap(e, now, "level_001_lake")
	if got := ratePrice(seaFish); got != wantLake {
		t.Fatalf("1 图询价=%d，期望卖鱼地图价 %d", got, wantLake)
	}
	if got := sellPrice(seaFish); got != wantLake {
		t.Fatalf("1 图入账=%d，期望询价 %d", got, wantLake)
	}
}
