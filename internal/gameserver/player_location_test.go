package gameserver

import (
	"context"
	"sync"
	"testing"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
)

func newLocationTestServer(t *testing.T, db *store.DB) *Server {
	t.Helper()
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "location-test"}}, logging.Discard(), nil)
	srv.SetDB(db)
	return srv
}

func locationTestSession(srv *Server, uid int64) *Session {
	return &Session{ID: 1, Userid: uid, Srv: srv, state: map[string]any{}, log: logging.Discard()}
}

func locationTestCtx(sess *Session, tree *proto.Value) *Ctx {
	return &Ctx{Sess: sess, Main: 1, Sub: 13, Tree: tree, DecodeOK: true, Log: logging.Discard()}
}

func decodeLocator(t *testing.T, srv *Server, body []byte) (level, scene string) {
	t.Helper()
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatalf("1/13 响应解码失败: %v", err)
	}
	if dec.Kind != proto.KindObj || dec.Class != "cgbaffjpbkg" {
		t.Fatalf("1/13 响应应为 206 cgbaffjpbkg，实得 %s/%s", dec.Kind, dec.Class)
	}
	dobField := dec.Field(0)
	dob := dobField.Tree()
	if dob == nil || dob.FieldCount() == 0 {
		t.Fatal("1/13 响应缺少 dob")
	}
	keyField := dob.Field(0)
	sceneField := dec.Field(1)
	return keyField.StrOr(), sceneField.StrOr()
}

// 首次玩家没有位置记录时必须回出生图；1/14 成功后，同进程重连和
// 进程重启式新 Server 都要从 profile 恢复上次地图。
func TestPlayerLocationFirstLoginTravelReconnectAndRestart(t *testing.T) {
	t.Parallel()
	db := connectTestDB(t)
	const uid int64 = 1000001
	srv := newLocationTestServer(t, db)

	body, ok := hEnterMap(locationTestCtx(locationTestSession(srv, uid), proto.Args524()))
	if !ok {
		t.Fatal("首次 1/13 未处理")
	}
	level, scene := decodeLocator(t, srv, body)
	if level != spawnLevelKey || scene != spawnScene {
		t.Fatalf("首次登录应回出生图，实得 %s/%s", level, scene)
	}

	// 1/13 若携带合法地图 key，也应把这次成功进入原子写回。
	reqLevel := "level_003_viiveri_lake"
	body, ok = hEnterMap(locationTestCtx(locationTestSession(srv, uid), proto.Args524(proto.StrVal(reqLevel))))
	if !ok {
		t.Fatal("带地图 key 的 1/13 未处理")
	}
	level, _ = decodeLocator(t, srv, body)
	if level != reqLevel {
		t.Fatalf("1/13 切图后=%q，期望 %q", level, reqLevel)
	}
	if loc, err := db.GetPlayerLocation(context.Background(), uid); err != nil || loc == nil || loc.LevelKey != reqLevel {
		t.Fatalf("1/13 切图未落库: loc=%+v err=%v", loc, err)
	}

	target := "level_019_american_pond"
	travelCtx := locationTestCtx(locationTestSession(srv, uid),
		proto.Args524(proto.StrVal(target), proto.U32(42), proto.U32(99)))
	travelCtx.Main, travelCtx.Sub = 1, 14
	if _, ok := hTravel(travelCtx); !ok {
		t.Fatal("1/14 未处理")
	}
	loc, err := db.GetPlayerLocation(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	if loc == nil || loc.LevelKey != target || loc.Spot != 42 {
		t.Fatalf("1/14 未持久化目标位置: %+v", loc)
	}
	if got := srv.currentLevel(uid); got != target {
		t.Fatalf("同进程 currentLevel=%q，期望 %q", got, target)
	}

	// 同进程新连接：1/13 空请求必须返回上次地图。
	body, ok = hEnterMap(locationTestCtx(locationTestSession(srv, uid), proto.Args524()))
	if !ok {
		t.Fatal("重连 1/13 未处理")
	}
	level, _ = decodeLocator(t, srv, body)
	if level != target {
		t.Fatalf("同进程重连回 %q，期望 %q", level, target)
	}

	// 模拟进程重启：全新 Server 无 levelMap，只能从 DB 恢复。
	restarted := newLocationTestServer(t, db)
	body, ok = hEnterMap(locationTestCtx(locationTestSession(restarted, uid), proto.Args524()))
	if !ok {
		t.Fatal("重启后 1/13 未处理")
	}
	level, _ = decodeLocator(t, restarted, body)
	if level != target {
		t.Fatalf("重启后登录回 %q，期望 %q", level, target)
	}
	if got := restarted.currentLevel(uid); got != target {
		t.Fatalf("重启后 currentLevel=%q，期望 %q", got, target)
	}
}

// 切图必须按目标地图所需银币扣款，同图重入与免费地图不重复收费。
func TestTravelDeductsMapSilver(t *testing.T) {
	t.Parallel()
	db := connectTestDB(t)
	const uid int64 = 1000002
	srv := newLocationTestServer(t, db)
	if _, err := db.EnsureProfile(context.Background(), uid); err != nil {
		t.Fatal(err)
	}
	const start int64 = 10000
	if _, err := db.SQL.Exec(`UPDATE profile SET silver=? WHERE uid=?`, start, uid); err != nil {
		t.Fatal(err)
	}
	target := "level_004_torfyanoe_ozero"
	cost := placeTravelCost(target)
	if cost <= 0 {
		t.Fatalf("测试地图未配置银币价: %s", target)
	}
	travel := func(level string) {
		t.Helper()
		ctx := locationTestCtx(locationTestSession(srv, uid),
			proto.Args524(proto.StrVal(level), proto.U32(7)))
		ctx.Main, ctx.Sub = 1, 14
		if _, ok := hTravel(ctx); !ok {
			t.Fatalf("1/14 切图未处理: %s", level)
		}
	}
	silverOf := func() int64 {
		t.Helper()
		var v int64
		if err := db.SQL.QueryRow(`SELECT silver FROM profile WHERE uid=?`, uid).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	travel(target)
	if got := silverOf(); got != start-cost {
		t.Fatalf("切图未扣银币: silver=%d want=%d", got, start-cost)
	}
	var txnCount int
	if err := db.SQL.QueryRow(
		`SELECT COUNT(*) FROM txn WHERE uid=? AND note='place_travel' AND silver_delta=?`,
		uid, -cost).Scan(&txnCount); err != nil {
		t.Fatal(err)
	}
	if txnCount != 1 {
		t.Fatalf("切图流水应为 1 条，实得 %d", txnCount)
	}
	if loc, err := db.GetPlayerLocation(context.Background(), uid); err != nil ||
		loc == nil || loc.LevelKey != target {
		t.Fatalf("扣费后未切到目标地图: loc=%+v err=%v", loc, err)
	}

	travel(target)                    // 同图重入，客户端重新定位不算新旅行
	travel("level_019_american_pond") // 免费地图
	if got := silverOf(); got != start-cost {
		t.Fatalf("同图/免费地图不应再扣费: silver=%d want=%d", got, start-cost)
	}
}

// 银币不足时回业务失败，既不扣款也不切图。
func TestTravelInsufficientSilverStaysOnMap(t *testing.T) {
	t.Parallel()
	db := connectTestDB(t)
	const uid int64 = 1000003
	srv := newLocationTestServer(t, db)
	if _, err := db.EnsureProfile(context.Background(), uid); err != nil {
		t.Fatal(err)
	}
	target := "level_012_north_sea"
	cost := placeTravelCost(target)
	if cost <= 0 {
		t.Fatalf("测试地图未配置银币价: %s", target)
	}
	if _, err := db.SQL.Exec(`UPDATE profile SET silver=? WHERE uid=?`, cost-1, uid); err != nil {
		t.Fatal(err)
	}
	ctx := locationTestCtx(locationTestSession(srv, uid), proto.Args524(proto.StrVal(target)))
	ctx.Main, ctx.Sub = 1, 14
	body, ok := hTravel(ctx)
	if !ok || len(body) != 0 {
		t.Fatalf("银币不足应回业务失败: ok=%v body=%x", ok, body)
	}
	var silver int64
	if err := db.SQL.QueryRow(`SELECT silver FROM profile WHERE uid=?`, uid).Scan(&silver); err != nil {
		t.Fatal(err)
	}
	if silver != cost-1 {
		t.Fatalf("余额不足仍扣款: silver=%d want=%d", silver, cost-1)
	}
	if got := srv.currentLevel(uid); got == target {
		t.Fatal("余额不足不应切换地图")
	}
}

// 1/13 带地图 key 同样算一次切图，也要扣该地图的银币。
func TestEnterMapWithKeyDeductsSilver(t *testing.T) {
	t.Parallel()
	db := connectTestDB(t)
	const uid int64 = 1000005
	srv := newLocationTestServer(t, db)
	if _, err := db.EnsureProfile(context.Background(), uid); err != nil {
		t.Fatal(err)
	}
	target := "level_010_belaya"
	cost := placeTravelCost(target)
	if cost <= 0 {
		t.Fatalf("测试地图未配置银币价: %s", target)
	}
	const start int64 = 5000
	if _, err := db.SQL.Exec(`UPDATE profile SET silver=? WHERE uid=?`, start, uid); err != nil {
		t.Fatal(err)
	}
	ctx := locationTestCtx(locationTestSession(srv, uid), proto.Args524(proto.StrVal(target)))
	if body, ok := hEnterMap(ctx); !ok || len(body) == 0 {
		t.Fatalf("1/13 未处理: ok=%v", ok)
	}
	var silver int64
	if err := db.SQL.QueryRow(`SELECT silver FROM profile WHERE uid=?`, uid).Scan(&silver); err != nil {
		t.Fatal(err)
	}
	if silver != start-cost {
		t.Fatalf("1/13 切图未扣银币: silver=%d want=%d", silver, start-cost)
	}
	// 同一地图的登录恢复（不带 key）不得再次扣费。
	ctx = locationTestCtx(locationTestSession(srv, uid), proto.Args524())
	if _, ok := hEnterMap(ctx); !ok {
		t.Fatal("1/13 登录恢复未处理")
	}
	if err := db.SQL.QueryRow(`SELECT silver FROM profile WHERE uid=?`, uid).Scan(&silver); err != nil {
		t.Fatal(err)
	}
	if silver != start-cost {
		t.Fatalf("登录恢复不应再扣费: silver=%d want=%d", silver, start-cost)
	}
}

// 已金币买断（无限通行）的地图切图不再收银币。
func TestTravelBuyoutPlaceIsFree(t *testing.T) {
	t.Parallel()
	db := connectTestDB(t)
	const uid int64 = 1000004
	srv := newLocationTestServer(t, db)
	if _, err := db.EnsureProfile(context.Background(), uid); err != nil {
		t.Fatal(err)
	}
	target := "level_006_ladoga"
	cost := placeTravelCost(target)
	if cost <= 0 {
		t.Fatalf("测试地图未配置银币价: %s", target)
	}
	if _, err := db.SQL.Exec(`UPDATE profile SET silver=? WHERE uid=?`, cost+100, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.PlaceAccessInsert(context.Background(), uid, target, "buyout"); err != nil {
		t.Fatal(err)
	}
	ctx := locationTestCtx(locationTestSession(srv, uid), proto.Args524(proto.StrVal(target)))
	ctx.Main, ctx.Sub = 1, 14
	if _, ok := hTravel(ctx); !ok {
		t.Fatal("1/14 未处理")
	}
	var silver int64
	if err := db.SQL.QueryRow(`SELECT silver FROM profile WHERE uid=?`, uid).Scan(&silver); err != nil {
		t.Fatal(err)
	}
	if silver != cost+100 {
		t.Fatalf("已买断地图不应收旅费: silver=%d want=%d", silver, cost+100)
	}
}

// 数据库中的地图 key 无效时，1/13 必须返回出生图并修正记录。
func TestPlayerLocationInvalidMapFallsBackToSpawn(t *testing.T) {
	t.Parallel()
	db := connectTestDB(t)
	const uid int64 = 1000001
	if err := db.SetPlayerLocation(context.Background(), uid, "level_does_not_exist", "bad_scene", 7); err != nil {
		t.Fatal(err)
	}
	srv := newLocationTestServer(t, db)
	body, ok := hEnterMap(locationTestCtx(locationTestSession(srv, uid), proto.Args524()))
	if !ok {
		t.Fatal("1/13 未处理")
	}
	level, scene := decodeLocator(t, srv, body)
	if level != spawnLevelKey || scene != spawnScene {
		t.Fatalf("无效地图应回出生图，实得 %s/%s", level, scene)
	}
	loc, err := db.GetPlayerLocation(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	if loc == nil || loc.LevelKey != spawnLevelKey {
		t.Fatalf("无效地图未被修正: %+v", loc)
	}
}

// ---- 审查意见回归：原子性 / 有效钓鱼游 / 并发幂等 ----

func travelTestCtx(srv *Server, uid int64, level string) *Ctx {
	ctx := locationTestCtx(locationTestSession(srv, uid),
		proto.Args524(proto.StrVal(level), proto.U32(7)))
	ctx.Main, ctx.Sub = 1, 14
	return ctx
}

func seedTravelTestPlayer(t *testing.T, db *store.DB, uid, silver int64) {
	t.Helper()
	if _, err := db.EnsureProfile(context.Background(), uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE profile SET silver=? WHERE uid=?`, silver, uid); err != nil {
		t.Fatal(err)
	}
}

func travelTestSilver(t *testing.T, db *store.DB, uid int64) int64 {
	t.Helper()
	var v int64
	if err := db.SQL.QueryRow(`SELECT silver FROM profile WHERE uid=?`, uid).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func travelTestTxns(t *testing.T, db *store.DB, uid int64) int {
	t.Helper()
	var n int
	if err := db.SQL.QueryRow(
		`SELECT COUNT(*) FROM txn WHERE uid=? AND note='place_travel'`, uid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// 位置写入失败时，钱包扣款、流水与切图必须一起回滚（审查意见 P1）。
func TestTravelLocationFailureRollsBackCharge(t *testing.T) {
	t.Parallel()
	db := connectTestDB(t)
	const uid int64 = 1000006
	srv := newLocationTestServer(t, db)
	target := "level_004_torfyanoe_ozero"
	cost := placeTravelCost(target)
	if cost <= 0 {
		t.Fatalf("测试地图未配置银币价: %s", target)
	}
	const start int64 = 10000
	seedTravelTestPlayer(t, db, uid, start)

	// 只让「写目标地图」的 profile 更新失败：取写锁与钱包 UPDATE 不改动
	// last_level_key，仍会正常执行，因此扣款先发生、位置写入后失败。
	if _, err := db.SQL.Exec(`
		CREATE TRIGGER travel_fail_location BEFORE UPDATE ON profile
		WHEN NEW.last_level_key='level_004_torfyanoe_ozero'
		BEGIN SELECT RAISE(ABORT,'location write failed'); END`); err != nil {
		t.Fatal(err)
	}
	body, ok := hTravel(travelTestCtx(srv, uid, target))
	if !ok || len(body) != 0 {
		t.Fatalf("位置写入失败应回业务失败: ok=%v body=%x", ok, body)
	}
	if got := travelTestSilver(t, db, uid); got != start {
		t.Fatalf("位置写入失败仍扣旅费: silver=%d want=%d", got, start)
	}
	if got := travelTestTxns(t, db, uid); got != 0 {
		t.Fatalf("位置写入失败仍留下旅费流水: %d", got)
	}
	if loc, err := db.GetPlayerLocation(context.Background(), uid); err != nil ||
		(loc != nil && loc.LevelKey == target) {
		t.Fatalf("位置写入失败却已切图: loc=%+v err=%v", loc, err)
	}
	if got := srv.currentLevel(uid); got == target {
		t.Fatal("位置写入失败不应更新内存当前地图")
	}
}

// PR #171 合并后，有效钓鱼游期间前往该地图不再收普通切图门票；到期后恢复收费。
func TestTravelActiveTripIsFree(t *testing.T) {
	t.Parallel()
	db := connectTestDB(t)
	const uid int64 = 1000007
	srv := newLocationTestServer(t, db)
	target := "level_012_north_sea"
	cost := placeTravelCost(target)
	if cost <= 0 {
		t.Fatalf("测试地图未配置银币价: %s", target)
	}
	const start int64 = 200000
	seedTravelTestPlayer(t, db, uid, start)

	// 模拟 PR #171 的迁移 00035 与 3/38 购票写入（票有效、尚未进图启表）。
	if _, err := db.SQL.Exec(`CREATE TABLE IF NOT EXISTS trip_state (
		uid INTEGER NOT NULL, place TEXT NOT NULL, remaining_sec REAL NOT NULL,
		last_consume_ts REAL NOT NULL, purchases INTEGER NOT NULL DEFAULT 0,
		updated_ts REAL NOT NULL, PRIMARY KEY(uid, place))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO trip_state
		(uid, place, remaining_sec, last_consume_ts, purchases, updated_ts)
		VALUES(?,?,?,?,?,?)`, uid, target, 18000, 0, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := db.PlaceAccessInsert(context.Background(), uid, target, "trip"); err != nil {
		t.Fatal(err)
	}

	if _, ok := hTravel(travelTestCtx(srv, uid, target)); !ok {
		t.Fatal("1/14 未处理")
	}
	if got := travelTestSilver(t, db, uid); got != start {
		t.Fatalf("有效钓鱼游不应收旅费: silver=%d want=%d", got, start)
	}
	if got := travelTestTxns(t, db, uid); got != 0 {
		t.Fatalf("有效钓鱼游不应产生旅费流水: %d", got)
	}

	// 到期后回到同一张图应正常收费；同图判定以事务内 DB 位置为准，
	// 不受 Server 内存热缓存（仍是 target）影响。
	if _, err := db.SQL.Exec(
		`UPDATE trip_state SET remaining_sec=0 WHERE uid=? AND place=?`, uid, target); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(
		`DELETE FROM place_access WHERE uid=? AND place=? AND kind='trip'`, uid, target); err != nil {
		t.Fatal(err)
	}
	if err := db.SetPlayerLocation(context.Background(), uid, "level_019_american_pond", "", 0); err != nil {
		t.Fatal(err)
	}
	if _, ok := hTravel(travelTestCtx(srv, uid, target)); !ok {
		t.Fatal("过期后 1/14 未处理")
	}
	if got := travelTestSilver(t, db, uid); got != start-cost {
		t.Fatalf("钓鱼游到期后应收旅费: silver=%d want=%d", got, start-cost)
	}
	if got := travelTestTxns(t, db, uid); got != 1 {
		t.Fatalf("钓鱼游到期后应有 1 条旅费流水，实得 %d", got)
	}
}

// 房契（place_access kind='house'）同样免普通切图门票。
func TestTravelHouseAccessIsFree(t *testing.T) {
	t.Parallel()
	db := connectTestDB(t)
	const uid int64 = 1000008
	srv := newLocationTestServer(t, db)
	target := "level_003_viiveri_lake"
	if placeTravelCost(target) <= 0 {
		t.Fatalf("测试地图未配置银币价: %s", target)
	}
	const start int64 = 10000
	seedTravelTestPlayer(t, db, uid, start)
	if err := db.PlaceAccessInsert(context.Background(), uid, target+"_house", "house"); err != nil {
		t.Fatal(err)
	}
	if _, ok := hTravel(travelTestCtx(srv, uid, target)); !ok {
		t.Fatal("1/14 未处理")
	}
	if got := travelTestSilver(t, db, uid); got != start {
		t.Fatalf("已购房契不应收旅费: silver=%d want=%d", got, start)
	}
	if got := travelTestTxns(t, db, uid); got != 0 {
		t.Fatalf("已购房契不应产生旅费流水: %d", got)
	}
}

// 同一玩家两个并发 1/14：事务写锁 + 事务内同图判定，只允许扣一次旅费。
func TestConcurrentTravelChargesOnce(t *testing.T) {
	t.Parallel()
	db := connectTestDB(t)
	const uid int64 = 1000009
	srv := newLocationTestServer(t, db)
	target := "level_004_torfyanoe_ozero"
	cost := placeTravelCost(target)
	const start int64 = 10000
	seedTravelTestPlayer(t, db, uid, start)

	var wg sync.WaitGroup
	startCh := make(chan struct{})
	oks := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startCh
			body, ok := hTravel(travelTestCtx(srv, uid, target))
			oks <- ok && len(body) > 0
		}()
	}
	close(startCh)
	wg.Wait()
	close(oks)
	for ok := range oks {
		if !ok {
			t.Fatal("并发 1/14 有请求未成功")
		}
	}
	if got := travelTestSilver(t, db, uid); got != start-cost {
		t.Fatalf("并发切图重复扣费: silver=%d want=%d", got, start-cost)
	}
	if got := travelTestTxns(t, db, uid); got != 1 {
		t.Fatalf("并发切图旅费流水应为 1 条，实得 %d", got)
	}
	if loc, err := db.GetPlayerLocation(context.Background(), uid); err != nil ||
		loc == nil || loc.LevelKey != target {
		t.Fatalf("并发切图后位置异常: loc=%+v err=%v", loc, err)
	}
}

func travelTestProfileSilver(t *testing.T, srv *Server, body []byte) int64 {
	t.Helper()
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatalf("1/14 响应解码失败: %v", err)
	}
	prof := dec.ArgItem(2)
	if prof == nil || prof.Kind != proto.KindObj {
		t.Fatalf("1/14 响应缺少档案 307: %+v", prof)
	}
	f := prof.Field(7) // 307.f7 银币
	if f.Num != nil {
		return int64(*f.Num)
	}
	if v := f.Tree(); v != nil {
		return int64(v.Num)
	}
	t.Fatal("307.f7 银币字段缺失")
	return 0
}

// 1/14 响应内的档案 307 必须是扣款后的余额，不能用预构造的旧档案覆盖客户端。
func TestTravelResponseBodyHasChargedSilver(t *testing.T) {
	t.Parallel()
	db := connectTestDB(t)
	const uid int64 = 1000010
	srv := newLocationTestServer(t, db)
	target := "level_004_torfyanoe_ozero"
	cost := placeTravelCost(target)
	const start int64 = 10000
	seedTravelTestPlayer(t, db, uid, start)

	body, ok := hTravel(travelTestCtx(srv, uid, target))
	if !ok || len(body) == 0 {
		t.Fatalf("1/14 未处理: ok=%v body=%x", ok, body)
	}
	if got := travelTestProfileSilver(t, srv, body); got != start-cost {
		t.Fatalf("1/14 档案余额未反映扣款: f7=%d want=%d", got, start-cost)
	}
}

// 1/13 带 key 进入付费地图也必须保留银币门票入口，不能因为缺少
// 买断/钓鱼游就在 settleTravel 之前被弹回出生图。
func TestEnterMapKeyedPaidMapChargesSilver(t *testing.T) {
	db := connectTestDB(t)
	const uid int64 = 1000012
	target := "level_012_north_sea"
	if _, ok := placeTripPrices[target]; !ok {
		t.Fatalf("测试地图未配置钓鱼游价: %s", target)
	}
	cost := placeTravelCost(target)
	const start int64 = 20000
	seedTravelTestPlayer(t, db, uid, start)
	srv := newLocationTestServer(t, db)
	body, ok := hEnterMap(locationTestCtx(locationTestSession(srv, uid),
		proto.Args524(proto.StrVal(target))))
	if !ok {
		t.Fatal("1/13 带 key 进入付费地图未处理")
	}
	level, _ := decodeLocator(t, srv, body)
	if level != target {
		t.Fatalf("银币票进入回 %q，期望 %q", level, target)
	}
	if got := travelTestSilver(t, db, uid); got != start-cost {
		t.Fatalf("银币票进入后余额=%d，期望 %d", got, start-cost)
	}
	if got := travelTestTxns(t, db, uid); got != 1 {
		t.Fatalf("银币票进入流水=%d，期望 1", got)
	}
	if loc, err := db.GetPlayerLocation(context.Background(), uid); err != nil ||
		loc == nil || loc.LevelKey != target {
		t.Fatalf("银币票进入未落库: loc=%+v err=%v", loc, err)
	}
}

func seedTripStateForLocationTest(t *testing.T, db *store.DB, uid int64, place string,
	remaining float64, purchases int64, updated float64) {
	t.Helper()
	if _, err := db.SQL.Exec(`
		INSERT INTO trip_state(uid, place, remaining_sec, last_consume_ts, purchases, updated_ts)
		VALUES(?,?,?,?,?,?)`, uid, place, remaining, updated, purchases, updated); err != nil {
		t.Fatal(err)
	}
}

// 登录恢复应对三种付费地图入口一致：银币票停留、金币买断、有效钓鱼游
// 都保留上次地图，且空请求不得重复扣银币。
func TestLoginRestoreKeepsPaidMapByAccessMode(t *testing.T) {
	const target = "level_012_north_sea"
	if _, ok := placeTripPrices[target]; !ok {
		t.Fatalf("测试地图未配置钓鱼游价: %s", target)
	}
	cases := []struct {
		name  string
		setup func(*testing.T, *store.DB, int64)
	}{
		{name: "silver_ticket"},
		{name: "buyout", setup: func(t *testing.T, db *store.DB, uid int64) {
			t.Helper()
			if err := db.PlaceAccessInsert(context.Background(), uid, target, "buyout"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "active_trip", setup: func(t *testing.T, db *store.DB, uid int64) {
			t.Helper()
			seedTripStateForLocationTest(t, db, uid, target, 3600, 1, 1)
			if err := db.PlaceAccessInsert(context.Background(), uid, target, "trip"); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			db := connectTestDB(t)
			const uid int64 = 1000011
			const start int64 = 20000
			seedTravelTestPlayer(t, db, uid, start)
			if err := db.SetPlayerLocation(context.Background(), uid, target, "", 0); err != nil {
				t.Fatal(err)
			}
			if tc.setup != nil {
				tc.setup(t, db, uid)
			}
			srv := newLocationTestServer(t, db)
			body, ok := hEnterMap(locationTestCtx(locationTestSession(srv, uid), proto.Args524()))
			if !ok {
				t.Fatal("登录恢复 1/13 未处理")
			}
			level, _ := decodeLocator(t, srv, body)
			if level != target {
				t.Fatalf("登录恢复回 %q，期望保留 %q", level, target)
			}
			if got := srv.currentLevel(uid); got != target {
				t.Fatalf("currentLevel=%q，期望 %q", got, target)
			}
			if got := travelTestSilver(t, db, uid); got != start {
				t.Fatalf("登录恢复重复扣费: silver=%d want=%d", got, start)
			}
			if got := travelTestTxns(t, db, uid); got != 0 {
				t.Fatalf("登录恢复不应产生旅费流水，实得 %d", got)
			}
		})
	}
}

// 仅靠钓鱼游进入的玩家，游到期且没有后续银币票时必须回出生图。
func TestLoginRestoreExpiredTripFallsBackToSpawn(t *testing.T) {
	db := connectTestDB(t)
	const uid int64 = 1000013
	const start int64 = 20000
	target := "level_012_north_sea"
	seedTravelTestPlayer(t, db, uid, start)
	seedTripStateForLocationTest(t, db, uid, target, 0, 1, 100)
	if err := db.SetPlayerLocation(context.Background(), uid, target, "", 0); err != nil {
		t.Fatal(err)
	}
	srv := newLocationTestServer(t, db)
	body, ok := hEnterMap(locationTestCtx(locationTestSession(srv, uid), proto.Args524()))
	if !ok {
		t.Fatal("登录恢复 1/13 未处理")
	}
	level, _ := decodeLocator(t, srv, body)
	if level != spawnLevelKey {
		t.Fatalf("钓鱼游到期后登录回 %q，期望 %q", level, spawnLevelKey)
	}
	if got := srv.currentLevel(uid); got != spawnLevelKey {
		t.Fatalf("到期后 currentLevel=%q，期望 %q", got, spawnLevelKey)
	}
	if loc, err := db.GetPlayerLocation(context.Background(), uid); err != nil ||
		loc == nil || loc.LevelKey != spawnLevelKey {
		t.Fatalf("到期后位置未修正: loc=%+v err=%v", loc, err)
	}
	if got := travelTestSilver(t, db, uid); got != start {
		t.Fatalf("到期回出生图不应扣费: silver=%d want=%d", got, start)
	}
}

// 钓鱼游到期后玩家又用银币票重新进入并下线，重登仍应保留该地图。
func TestLoginRestoreExpiredTripWithLaterTicketKeepsMap(t *testing.T) {
	db := connectTestDB(t)
	const uid int64 = 1000014
	target := "level_012_north_sea"
	seedTravelTestPlayer(t, db, uid, 20000)
	seedTripStateForLocationTest(t, db, uid, target, 0, 1, 100)
	if _, err := db.SQL.Exec(`
		INSERT INTO txn(uid, ts, kind, item_id, silver_delta, gold_delta, note)
		VALUES(?,?,?,?,?,?,?)`, uid, 200.0, "buy", target,
		-placeTravelCost(target), 0, "place_travel"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetPlayerLocation(context.Background(), uid, target, "", 0); err != nil {
		t.Fatal(err)
	}
	srv := newLocationTestServer(t, db)
	body, ok := hEnterMap(locationTestCtx(locationTestSession(srv, uid), proto.Args524()))
	if !ok {
		t.Fatal("登录恢复 1/13 未处理")
	}
	level, _ := decodeLocator(t, srv, body)
	if level != target {
		t.Fatalf("到期后重新买票登录回 %q，期望保留 %q", level, target)
	}
}
