package gameserver

import (
	"testing"
	"time"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

// gbDeposit 测试用的投放封装：坐标固定在 (100,100) 附近同一个 5 米格。
func gbDeposit(t *testing.T, s *Server, uid int64, itemID string, portions float64, channel, txn string) {
	t.Helper()
	s.groundbaitDeposit(t.Context(), uid, "gb-test-loc", 100, 100, true, 7, itemID, portions, channel, txn)
}

func gbSpotState(t *testing.T, s *Server, uid int64) groundbaitSpotState {
	t.Helper()
	return s.groundbaitSpot(t.Context(), uid, "gb-test-loc",
		int64(100/groundbaitCellSizeM), int64(100/groundbaitCellSizeM))
}

// gbAge 把某格所有批次往前推 gameHours 游戏小时（改投放时的游戏钟读数）。
func gbAge(t *testing.T, s *Server, uid int64, gameHours float64) {
	t.Helper()
	_, err := s.DB.SQL.Exec(`UPDATE groundbait_deposit SET deposit_hour=deposit_hour-?
		WHERE uid=? AND loc='gb-test-loc'`, gameHours, uid)
	if err != nil {
		t.Fatalf(" aging 失败: %v", err)
	}
}

func gbCount(t *testing.T, s *Server, uid int64) int {
	t.Helper()
	var n int
	if err := s.DB.SQL.QueryRow(`SELECT COUNT(*) FROM groundbait_deposit WHERE uid=?`, uid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestGroundbaitLedgerPortionTimeAndOverdose(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	setWeatherTestTunable(t, s, "groundbait_enabled", "1")
	const uid = int64(1000001)

	gbDeposit(t, s, uid, "feed_karp", 10, "catapult", "gb:t1")
	st := gbSpotState(t, s, uid)
	if got := st.units; got < 9.99 || got > 10.01 {
		t.Fatalf("10 份窝料应得 N=10，实得 %.3f", got)
	}
	if got := st.hoursLeft; got < 5.99 || got > 6.01 {
		t.Fatalf("10 份窝料应得 T=6 游戏小时（少爷指示：10 份=6 小时），实得 %.3f", got)
	}
	if p := s.groundbaitOverdoseP(st.units); p != 1 {
		t.Fatalf("15 份以内不该死窝，实得 P=%.2f", p)
	}
	if gate := s.groundbaitBiteGate(ctx, uid, "gb-test-loc", 100, 100, true, 7, 1); gate != 1 {
		t.Fatalf("正常窝的咬钩闸门应为 1，实得 %.2f", gate)
	}

	// 幂等：同一条上报重复投 3 次，只算一次。
	gbDeposit(t, s, uid, "feed_karp", 10, "catapult", "gb:t1")
	gbDeposit(t, s, uid, "feed_karp", 10, "catapult", "gb:t1")
	if n := gbCount(t, s, uid); n != 1 {
		t.Fatalf("同 txn_guid 重复上报应只入账一次，实得 %d 批", n)
	}

	// 死窝：再堆 12 份，N=22 → 开口率归零。
	gbDeposit(t, s, uid, "feed_karp", 12, "spod", "gb:t2")
	st2 := gbSpotState(t, s, uid)
	if st2.units < 21.99 || st2.units > 22.01 {
		t.Fatalf("堆完应为 N=22，实得 %.3f", st2.units)
	}
	if gate := s.groundbaitBiteGate(ctx, uid, "gb-test-loc", 100, 100, true, 7, 1); gate > 0.001 {
		t.Fatalf("22 份以上这个格子应完全不开口，实得 P=%.3f", gate)
	}
	// 路亚族豁免死窝闸门（拟饵不是被窝料喂出来的口）。
	if gate := s.groundbaitBiteGate(ctx, uid, "gb-test-loc", 100, 100, true, 7, rigSpinningTypeTest()); gate == 0 {
		t.Fatal("路亚族应豁免死窝闸门")
	}

	// 窝时到点自动解除死窝：整格过期后被清掉。
	gbAge(t, s, uid, 7)
	st3 := gbSpotState(t, s, uid)
	if st3.units != 0 {
		t.Fatalf("超过 6 游戏小时后窝应归零，实得 %.3f", st3.units)
	}
	if n := gbCount(t, s, uid); n != 0 {
		t.Fatalf("过期批次应被清理，实得 %d 行", n)
	}
	if gate := s.groundbaitBiteGate(ctx, uid, "gb-test-loc", 100, 100, true, 7, 1); gate != 1 {
		t.Fatalf("死窝应随窝时耗尽自动解除，实得 %.2f", gate)
	}
}

func rigSpinningTypeTest() int64 {
	for rt := range rigTypeLureFamily {
		return rt
	}
	return 50
}

// 掠食者不蹭窝：冻结鱼种权重一动不动；对口鱼种被抬起来。
func TestGroundbaitWeightRaisesPreyNotPredator(t *testing.T) {
	s, _ := newRigTestServer(t)
	setWeatherTestTunable(t, s, "groundbait_enabled", "1")
	const uid = int64(1000001)
	gbDeposit(t, s, uid, "feed_karp", 8, "feeder", "gb:w1")

	c := fishRollContext{
		uid: uid, loc: "gb-test-loc", x: 100, z: 100, hasPos: true, spot: 7,
		now: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC), rigType: 1, depthCM: 100,
		cache:   newFishingRollCache(),
		weather: fishingWeather{known: true, kind: fishingWeatherClear, airTemp: 18, previousAirTemp: 18, wind: 3},
	}
	keys := []string{"carp", "n.pike", "unknown.fish"}
	wts := []float64{100, 100, 100}
	s.applyGroundbaitWeight(t.Context(), &c, keys, wts)

	if wts[0] <= 100 {
		t.Fatalf("喂鲤鱼的窝料应抬高普通鲤鱼权重，实得 %.2f", wts[0])
	}
	if wts[1] != 100 {
		t.Fatalf("掠食性鱼种（常见狗鱼）必须 x1.00 不蹭窝，实得 %.2f", wts[1])
	}
	if wts[2] != 100 {
		t.Fatalf("无名册鱼种不该被窝料改动，实得 %.2f", wts[2])
	}
	if wts[0] > 100*2.6001 {
		t.Fatalf("窝料乘子必须封顶 2.60，实得 %.3f", wts[0]/100)
	}
}

// 天气软化：烂天气里的乘子被抬起来，但绝不能超过该鱼好天气的基线。
func TestGroundbaitWeatherSofteningLiftsWithinBaseline(t *testing.T) {
	s, _ := newRigTestServer(t)
	setWeatherTestTunable(t, s, "groundbait_enabled", "1")
	const uid = int64(1000001)
	gbDeposit(t, s, uid, "feed_karp", 8, "feeder", "gb:s1")

	warm := fishRollContext{uid: uid, loc: "gb-test-loc", x: 100, z: 100, hasPos: true, spot: 7,
		now: time.Now(), rigType: 1, depthCM: 100, cache: newFishingRollCache(),
		weather: fishingWeather{known: true, kind: fishingWeatherClear, airTemp: 18, previousAirTemp: 18, wind: 3}}
	cold := warm
	// 深冬傍晚 + 大雨：季节、水温、天气三重不利，是软门最差的一档。
	cold.now = time.Date(2027, 1, 15, 17, 0, 0, 0, time.UTC)
	cold.weather = fishingWeather{known: true, kind: fishingWeatherHeavyRain, airTemp: -4, previousAirTemp: -4, wind: 6}

	mult := func(c fishRollContext) (float64, float64) {
		keys := []string{"carp"}
		on := []float64{100}
		s.applyGroundbaitWeight(t.Context(), &c, keys, on)
		setWeatherTestTunable(t, s, "groundbait_soft_beta", "0")
		base := []float64{100}
		s.applyGroundbaitWeight(t.Context(), &c, keys, base)
		setWeatherTestTunable(t, s, "groundbait_soft_beta", "0.45")
		return on[0] / 100, base[0] / 100
	}

	hotM, hotBase := mult(warm)
	coldM, coldBase := mult(cold)
	if coldM <= coldBase {
		t.Fatalf("烂天气里软化应把乘子抬起来：软化后 %.3f vs 关闭 %.3f", coldM, coldBase)
	}
	if hotM < hotBase-1e-9 {
		t.Fatalf("软化不能把天气带来的优势吃掉：暖 %.3f < 关软化 %.3f", hotM, hotBase)
	}
	if coldM > hotM {
		t.Fatalf("「深冬烂天气 + 一锅好料」不能打过「好天气」：冷 %.3f > 暖 %.3f", coldM, hotM)
	}
}

// 开关关掉时必须与上线窝料系统之前一模一样。
func TestGroundbaitDisabledLeavesRollUntouched(t *testing.T) {
	s, _ := newRigTestServer(t)
	setWeatherTestTunable(t, s, "groundbait_enabled", "0")
	const uid = int64(1000001)
	gbDeposit(t, s, uid, "feed_karp", 30, "feeder", "gb:off")
	if n := gbCount(t, s, uid); n != 0 {
		t.Fatalf("开关关闭时不该记账，实得 %d 批", n)
	}
	c := fishRollContext{uid: uid, loc: "gb-test-loc", x: 100, z: 100, hasPos: true, spot: 7,
		now: time.Now(), rigType: 1, depthCM: 100, cache: newFishingRollCache()}
	keys := []string{"carp"}
	wts := []float64{100}
	s.applyGroundbaitWeight(t.Context(), &c, keys, wts)
	if wts[0] != 100 {
		t.Fatalf("开关关闭时权重不该变，实得 %.2f", wts[0])
	}
	if gate := s.groundbaitBiteGate(t.Context(), uid, "gb-test-loc", 100, 100, true, 7, 1); gate != 1 {
		t.Fatalf("开关关闭时闸门应为 1，实得 %.2f", gate)
	}
}

// 弹弓通道：抛一发就得有一笔 catapult 投放落进落点格子。
func TestGroundbaitCatapultThrowDepositsSpot(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	setWeatherTestTunable(t, s, "groundbait_enabled", "1")
	const uid = int64(1000001)

	hand, err := s.CreateItem(ctx, uid, "wh_pc_gb", "backpack", 1)
	if err != nil {
		t.Fatalf("创建弹弓失败: %v", err)
	}
	// 弹药格挂的是窝料（cat46），扣的那一份就是投出去的那一份。
	stack, err := s.CreateItem(ctx, uid, "feedcompleted", "rig", 1)
	if err != nil {
		t.Fatalf("创建窝料失败: %v", err)
	}
	s.setStackCount(ctx, stack, 5)
	s.setHanditemLoadedRef(ctx, hand.GUID, stack.GUID)

	st := s.stateFor(uid)
	st.mu.Lock()
	st.Loc, st.CastPosX, st.CastPosZ, st.HasCastPos, st.Spot = "gb-test-loc", 100, 100, true, 7
	st.mu.Unlock()

	sess, _ := newTunnelProbe(t, s)
	req := rigRequestTree(t, proto.Guid(hand.GUID),
		wiregen.MustMakeObj("olieodeoaof", map[int]*proto.Value{0: proto.F32(12)}))
	hctx := &Ctx{Sess: sess, Main: 13, Sub: 5, CallID: 9001, Tree: req,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard()}
	if _, ok := hDynamicHanditemThrow(hctx); !ok {
		t.Fatal("13/5 未处理")
	}
	gate := s.groundbaitSpot(ctx, uid, "gb-test-loc", int64(100/groundbaitCellSizeM), int64(100/groundbaitCellSizeM))
	if gate.units < 0.99 || gate.units > 1.01 {
		t.Fatalf("弹弓一弹应投进 1 份窝料，实得 %.3f", gate.units)
	}
	var channel, itemID string
	if err := s.DB.SQL.QueryRowContext(ctx, `SELECT channel,item_id FROM groundbait_deposit WHERE uid=?`, uid).Scan(&channel, &itemID); err != nil {
		t.Fatalf("弹弓投放没落账: %v", err)
	}
	if channel != "catapult" || itemID != "feedcompleted" {
		t.Fatalf("投放通道/物品不符: %s %s", channel, itemID)
	}
}

// 食笼/PVA 通道：入水扣掉的那一份必须同时进窝料账本。
func TestGroundbaitCastDepositMapsChannels(t *testing.T) {
	s, _ := newRigTestServer(t)
	setWeatherTestTunable(t, s, "groundbait_enabled", "1")
	const uid = int64(1000001)
	sess, _ := newTunnelProbe(t, s)
	st := s.stateFor(uid)
	st.mu.Lock()
	st.Loc, st.CastPosX, st.CastPosZ, st.HasCastPos, st.Spot = "gb-test-loc", 100, 100, true, 7
	st.mu.Unlock()

	s.depositCastGroundbait(sess, st, baitChange{Groundbait: []groundbaitUse{
		{ItemID: "feedcompleted", Units: 1, Category: 46},
		{ItemID: "pva_stick_m", Units: 1, Category: 30},
	}}, "rod-x:123")

	rows, err := s.DB.SQL.Query(`SELECT channel, item_id FROM groundbait_deposit ORDER BY channel`)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for rows.Next() {
		var ch, it string
		if serr := rows.Scan(&ch, &it); serr != nil {
			t.Fatal(serr)
		}
		got[ch] = it
	}
	_ = rows.Close()
	if got["feeder"] != "feedcompleted" || got["pva"] != "pva_stick_m" {
		t.Fatalf("通道映射错了: %+v", got)
	}
}

// 先打窝、后抛竿的顺序实测过：弹弓与手抛只上报距离、不上报落点坐标，
// 这种窝记在「落点未知」那一格。玩家抛竿后判鱼用的是浮标落点格子，两者不同格——
// 这份窝必须按衰减系数继续生效（否则少爷「先用弹弓打窝再抛竿」等于白打），
// 但抬升要低于同一份数、落点正好压在浮标格上的窝；衰减调 0 退回「只认同格」。
func TestGroundbaitUnknownLandingStillWorks(t *testing.T) {
	s, _ := newRigTestServer(t)
	ctx := t.Context()
	setWeatherTestTunable(t, s, "groundbait_enabled", "1")
	const uidUnknown = int64(1000001)
	const uidKnown = int64(1000002)

	// 落点未知：hasPos=false，落点退化成「整个钓点一格」(spot,-1)。
	s.groundbaitDeposit(ctx, uidUnknown, "gb-test-loc", 0, 0, false, 7, "feed_karp", 8, "catapult", "gb:n1")
	// 同份数、落点已知，并且正好压在浮标所在的 (300,300) 格。
	s.groundbaitDeposit(ctx, uidKnown, "gb-test-loc", 300, 300, true, 7, "feed_karp", 8, "feeder", "gb:n2")

	roll := func(uid int64) float64 {
		c := fishRollContext{
			uid: uid, loc: "gb-test-loc", x: 300, z: 300, hasPos: true, spot: 7,
			now: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC), rigType: 1, depthCM: 100,
			cache:   newFishingRollCache(),
			weather: fishingWeather{known: true, kind: fishingWeatherClear, airTemp: 18, previousAirTemp: 18, wind: 3},
		}
		keys := []string{"carp", "n.pike"}
		wts := []float64{100, 100}
		s.applyGroundbaitWeight(ctx, &c, keys, wts)
		if wts[1] != 100 {
			t.Fatalf("掠食者任何时候都不该蹭窝，实得 %.2f", wts[1])
		}
		return wts[0]
	}

	diffuse := roll(uidUnknown)
	if diffuse <= 100 {
		t.Fatalf("落点未知的窝也应在浮标格生效，实得 %.2f", diffuse)
	}
	full := roll(uidKnown)
	if full <= diffuse {
		t.Fatalf("落点未知的抬升（%.2f）必须低于同份数落点已知的窝（%.2f）", diffuse, full)
	}

	// 落点未知的批次要能在状态里看出来，现场日志靠这两个数分辨窝打没打上。
	st := s.groundbaitSpot(ctx, uidUnknown, "gb-test-loc", int64(300/groundbaitCellSizeM), int64(300/groundbaitCellSizeM))
	if st.batches != 1 || st.diffuse != 1 {
		t.Fatalf("应看到 1 批落点未知的窝，实得 batches=%d diffuse=%d", st.batches, st.diffuse)
	}
	if st.units < 3.19 || st.units > 3.21 {
		t.Fatalf("8 份落点未知按 0.40 衰减应得 N=3.2，实得 %.3f", st.units)
	}

	// 衰减调成 0：退回「只认同格」，浮标格彻底看不见这份窝。
	setWeatherTestTunable(t, s, "groundbait_diffuse_fall", "0")
	if back := roll(uidUnknown); back != 100 {
		t.Fatalf("衰减=0 时落点未知的窝不该影响浮标格，实得 %.2f", back)
	}
	setWeatherTestTunable(t, s, "groundbait_diffuse_fall", "0.40")

	// 咬口扣份也要能从落点未知那几行里扣，否则窝永远不吃薄、死窝也解不开。
	s.groundbaitConsumeBite(ctx, uidUnknown, "gb-test-loc", 300, 300, true, 7, 1)
	var left float64
	if err := s.DB.SQL.QueryRow(`SELECT units_left FROM groundbait_deposit WHERE txn_guid='gb:n1'`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left < 6.99 || left > 7.01 {
		t.Fatalf("咬口应从没落点的窝行里扣掉 1 份（8→7），实得 %.3f", left)
	}
}
