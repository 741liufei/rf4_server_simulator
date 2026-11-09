package gameserver

// 搏鱼渔轮磨损回归：通道一（泄力打滑）只在未锁轮且鱼力超过当前泄力时计；通道二
// （锁轮过载）按发热曲线烧，且过载段之间不累积（解锁即段作废，下段从曲线开头重爬）。
// 摩擦片剩余寿命必须同步压低泄力与锁轮承力，结算每竿一次、重发不重复扣。
// 过载秒数按服务端收到 14/8 的真实间隔累加：客户端上报密度会变，固定节拍折算会把摩擦片烧错。
// 入账的鱼拉力必须是公斤力（27.f8 是牛，进账前换算），口径见
// specs/changes/reel-friction-life-model/spec.md 与 specs/changes/reel-wear-pull-unit/spec.md。

import (
	"context"
	"math"
	"testing"
	"time"

	"rf4server/internal/logging"
)

// newFightWearForTest 造一条搏鱼磨损账：ratedKg 是摩擦片额定，drag/locked 是此刻
// 泄力与锁轮承力，pull 是鱼拉力。oilBurn/oilWear 是轮油对烧穿时长与打滑的系数。
func newFightWearForTest(rated, drag, locked, pull, oilBurn, oilWear float64, lockedState bool) fightReelWear {
	return fightReelWear{
		valid: true, fishGUID: "fish0000000000000000000000000001",
		rodGUID: "rod0000000000000000000000000000001", reelGUID: "reel000000000000000000000000000001",
		locked: lockedState, setting: 25, dragKg: drag, lockedKg: locked, ratedKg: rated,
		oilWear: oilWear, oilBurn: oilBurn, pullKg: pull,
	}
}

func TestFightReelSlipOnlyWhenDragExceeded(t *testing.T) {
	srv := &Server{}
	tick := defaultReelLoadTickSeconds
	// 鱼力没超过泄力：泄力没打滑，摩擦片不发热。
	w := newFightWearForTest(7.7, 5, 9, 5, 1, 1, false)
	if ticks, slip, burn, _, _ := srv.fightReelTickWear(w, tick); ticks != 0 || slip != 0 || burn != 0 {
		t.Fatalf("鱼力等于泄力不应磨损，实得 ticks=%d slip=%v burn=%v", ticks, slip, burn)
	}
	// 超泄力只触发一次打滑；单次磨损固定为 0.01%，不按超出比例放大。
	w = newFightWearForTest(7.7, 5, 9, 7.5, 1, 1, false)
	ticks, slip, burn, _, _ := srv.fightReelTickWear(w, tick)
	if ticks != 1 || burn != 0 {
		t.Fatalf("超泄力应记一次打滑且不按曲线烧，实得 ticks=%d burn=%v", ticks, burn)
	}
	if want := reelSlipWearForSetting(25); math.Abs(slip-want) > 1e-12 {
		t.Fatalf("单次打滑磨损应为 %.6f，实得 %.6f", want, slip)
	}
	// 鱼再猛也不能放大单次打滑磨损。
	w = newFightWearForTest(7.7, 1, 9, 50, 1, 1, false)
	if _, slip, _, _, _ := srv.fightReelTickWear(w, tick); math.Abs(slip-reelSlipWearForSetting(25)) > 1e-12 {
		t.Fatalf("打滑磨损应固定为基准，实得 %.6f", slip)
	}
}

func TestLongHugeFishSlipWearUsesDragSettingCurve(t *testing.T) {
	srv := &Server{}
	// 2026-11-09 实机：28.373kg 鲨鱼换算 46.53kgf，泄力档实际 19.04kg，
	// 一条长搏斗累计 38 条 14/8。旧实现把超出比例再乘一次，摩擦片直接掉 34.2%。
	w := newFightWearForTest(20.9, 19.04, 27.47, 46.53, 1, 0.9, false)
	w.setting = 21
	var total float64
	for i := 0; i < 38; i++ {
		ticks, slip, burn, _, _ := srv.fightReelTickWear(w, defaultReelLoadTickSeconds)
		if ticks != 1 || burn != 0 {
			t.Fatalf("第 %d 次应只记打滑，实得 ticks=%d burn=%v", i+1, ticks, burn)
		}
		total += slip
	}
	want := 38 * reelSlipWearLow * 0.9
	if math.Abs(total-want) > 1e-12 {
		t.Fatalf("38 次打滑应累计 %.4f，实得 %.4f", want, total)
	}
	if total > 0.005 {
		t.Fatalf("38 次未锁轮打滑不应超过 0.5%%，实得 %.4f", total)
	}
}

func TestFightReelSlipWearCurveByDragSetting(t *testing.T) {
	srv := &Server{}
	for _, tc := range []struct {
		setting int64
		want    float64
	}{
		{24, reelSlipWearLow},
		{25, 0.000025},
		{26, 0.00005},
		{27, 0.000075},
		{28, 0.000095},
		{29, 0.0001},
	} {
		w := newFightWearForTest(7.7, 5, 9, 7.5, 1, 1, false)
		w.setting = tc.setting
		_, slip, _, _, _ := srv.fightReelTickWear(w, defaultReelLoadTickSeconds)
		if math.Abs(slip-tc.want) > 1e-12 {
			t.Fatalf("%d 档单次磨损=%v，期望 %v", tc.setting, slip, tc.want)
		}
	}
}

func TestFightReelLockedBurnCurveAndSegmentReset(t *testing.T) {
	srv := &Server{}
	tick := defaultReelLoadTickSeconds
	// 锁轮且鱼力超过承力 1.5 倍：按发热曲线烧，校准片连续 T 秒刚好烧穿。
	base := newFightWearForTest(7.7, 5, 7.7, 11.55, 1, 1, true)
	T := srv.reelBurnSecondsFor(base)
	if want := defaultReelBurnBaseSeconds / 0.85; math.Abs(T-want) > 1e-9 {
		t.Fatalf("烧穿时长应为 %.2fs（过载系数 0.85），实得 %.2fs", want, T)
	}
	continuous, continuousSeg := base, base.SegSecs
	var continuousWear float64
	for i := 0; i < 200; i++ {
		_, _, burn, _, next := srv.fightReelTickWear(continuous, tick)
		continuousWear += burn
		continuousSeg = next
		continuous.SegSecs = continuousSeg
		if continuousSeg >= T {
			break
		}
	}
	if math.Abs(continuousWear-1) > 1e-9 {
		t.Fatalf("连续锁轮满 T 秒应刚好烧完一整套摩擦片，实得 %.6f", continuousWear)
	}
	// 同样时长下对比：段间解锁散热的，曲线反复从开头重爬，累计磨损必须更少。
	const steps = 5
	staggered, staggeredSeg := base, base.SegSecs
	var staggeredWear float64
	for i := 0; i < steps; i++ {
		_, _, burn, _, next := srv.fightReelTickWear(staggered, tick)
		staggeredWear += burn
		staggeredSeg = next
		staggered.SegSecs = staggeredSeg
	}
	intermittent := base
	var intermittentWear float64
	for i := 0; i < steps; i++ {
		_, _, burn, _, _ := srv.fightReelTickWear(intermittent, tick)
		intermittentWear += burn
		intermittent.SegSecs = 0 // 解锁散热：段作废，下段从曲线开头重爬
	}
	if !(intermittentWear < staggeredWear && staggeredWear < continuousWear) {
		t.Fatalf("同时长下分段过载应最省：连续 %.4f 分段 %.4f 反复解锁 %.4f",
			continuousWear, staggeredWear, intermittentWear)
	}
	// 曲线前慢后快：前 10% 时间远少于最后 10%。
	curve, curveSeg := base, base.SegSecs
	var first, last float64
	step := T / 10
	for i := 0; i < 10; i++ {
		_, _, burn, _, next := srv.fightReelTickWear(curve, step)
		if i == 0 {
			first = burn
		}
		if i == 9 {
			last = burn
		}
		curveSeg = next
		curve.SegSecs = curveSeg
	}
	if first >= last {
		t.Fatalf("过载曲线必须前慢后快，实得前 10%%=%.4f 最后 10%%=%.4f", first, last)
	}
}

func TestFightReelOilLowersWearAndExtendsBurnTime(t *testing.T) {
	srv := &Server{}
	tick := defaultReelLoadTickSeconds
	// 打滑：好油省、差油费，且都被收进 0.8~1.2 区间。
	w := newFightWearForTest(7.7, 5, 9, 7.5, 1, 1, false)
	_, normal, _, _, _ := srv.fightReelTickWear(w, tick)
	w.oilWear = 0.2
	_, best, _, _, _ := srv.fightReelTickWear(w, tick)
	w.oilWear = 3
	_, worst, _, _, _ := srv.fightReelTickWear(w, tick)
	if !(best < normal && normal < worst) {
		t.Fatalf("轮油应改变打滑磨损：最佳 %.6f 常规 %.6f 最差 %.6f", best, normal, worst)
	}
	baseWear := reelSlipWearForSetting(25)
	if math.Abs(best-baseWear*0.8) > 1e-12 || math.Abs(worst-baseWear*1.2) > 1e-12 {
		t.Fatalf("轮油系数应被收进 0.8~1.2，实得 best=%.6f worst=%.6f", best, worst)
	}
	// 锁轮烧穿时长：油越好越长，油自身干涸则保护打折。
	base := newFightWearForTest(7.7, 5, 7.7, 11.55, 1, 1, true)
	def := srv.reelBurnSecondsFor(base)
	base.oilBurn = reelOilBurnFactor(100, 1)
	if got := srv.reelBurnSecondsFor(base); got <= def {
		t.Fatalf("满耐久优质轮油应更耐烧：缺省 %.2fs 实得 %.2fs", def, got)
	}
	base.oilBurn = reelOilBurnFactor(100, 0)
	if got := srv.reelBurnSecondsFor(base); got >= def {
		t.Fatalf("轮油干涸应缩短烧穿时长：缺省 %.2fs 实得 %.2fs", def, got)
	}
	if !(reelOilBurnFactor(15, 1) < reelOilBurnFactor(100, 1)) {
		t.Fatal("轮油品质越高应越耐烧")
	}
}

func TestFightReelSettleWearsWasherAndLowersDrag(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatalf("造竿失败: %v", err)
	}
	reel, err := srv.CreateItem(ctx, uid, "bclp_5004", "backpack", 1)
	if err != nil {
		t.Fatalf("造轮失败: %v", err)
	}
	mountReelOnRod(t, srv, rod.GUID, reel.GUID)

	row, err := db.GetItemByGUID(ctx, reel.GUID)
	if err != nil || row == nil {
		t.Fatalf("读取轮子失败: %v", err)
	}
	rated, ok := srv.reelWasherRatedFromRow(ctx, row)
	if !ok || rated <= 0 {
		t.Fatal("装配好的轮子应能读到摩擦片额定制动力")
	}
	before, perr := srv.syncReelPerformance(ctx, row)
	if perr != nil || before.BaseDragKg <= 0 || before.LockedDragKg <= 0 {
		t.Fatalf("轮子应有泄力与锁轮承力: %+v err=%v", before, perr)
	}

	st := srv.fishingStateForRod(uid, rod.GUID)
	st.mu.Lock()
	st.reelWear = newFightWearForTest(rated, before.BaseDragKg, before.LockedDragKg, before.BaseDragKg*3, 1, 1, false)
	st.reelWear.rodGUID, st.reelWear.reelGUID = rod.GUID, reel.GUID
	st.reelWear.SlipWear = 0.50 // 本竿打滑累计烧掉一半寿命
	st.mu.Unlock()

	srv.settleFightReelWearState(ctx, st, uid, "14/4")

	afterRow, err := db.GetItemByGUID(ctx, reel.GUID)
	if err != nil || afterRow == nil {
		t.Fatalf("读取轮子失败: %v", err)
	}
	after, perr := srv.syncReelPerformance(ctx, afterRow)
	if perr != nil {
		t.Fatalf("重建轮子物理参数失败: %v", perr)
	}
	if math.Abs(after.BaseDragKg-before.BaseDragKg*0.5) > before.BaseDragKg*0.02 {
		t.Fatalf("烧掉一半寿命后泄力应约减半：before=%.3f after=%.3f", before.BaseDragKg, after.BaseDragKg)
	}
	if after.LockedDragKg >= before.LockedDragKg {
		t.Fatalf("锁轮承力必须随摩擦片剩余寿命一起下降：before=%.3f after=%.3f", before.LockedDragKg, after.LockedDragKg)
	}

	// 每竿一次：重复结算不得再扣一次。
	srv.settleFightReelWearState(ctx, st, uid, "14/4")
	againRow, err := db.GetItemByGUID(ctx, reel.GUID)
	if err != nil || againRow == nil {
		t.Fatalf("读取轮子失败: %v", err)
	}
	again, perr := srv.syncReelPerformance(ctx, againRow)
	if perr != nil {
		t.Fatalf("重建轮子物理参数失败: %v", perr)
	}
	if math.Abs(again.BaseDragKg-after.BaseDragKg) > 1e-9 {
		t.Fatalf("重复结算不应重复扣耐久：%.6f -> %.6f", after.BaseDragKg, again.BaseDragKg)
	}

	// 烧穿：制动力归零，泄力与锁轮承力都失效，只能剪线后修。
	st.mu.Lock()
	st.reelWear = newFightWearForTest(rated, after.BaseDragKg, after.LockedDragKg, after.BaseDragKg*3, 1, 1, false)
	st.reelWear.rodGUID, st.reelWear.reelGUID = rod.GUID, reel.GUID
	st.reelWear.SlipWear = 1
	st.mu.Unlock()
	srv.settleFightReelWearState(ctx, st, uid, "14/4")
	deadRow, err := db.GetItemByGUID(ctx, reel.GUID)
	if err != nil || deadRow == nil {
		t.Fatalf("读取轮子失败: %v", err)
	}
	dead, perr := srv.syncReelPerformance(ctx, deadRow)
	if perr != nil {
		t.Fatalf("重建轮子物理参数失败: %v", perr)
	}
	if dead.BaseDragKg > 1e-9 || dead.LockedDragKg > 1e-9 {
		t.Fatalf("摩擦片烧穿后泄力应失效，实得 base=%.6f locked=%.6f", dead.BaseDragKg, dead.LockedDragKg)
	}
}

func TestFightReelWearWithoutReelIsSafe(t *testing.T) {
	srv, _ := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	handRod, err := srv.CreateItem(ctx, uid, "tele_10012_tl16", "backpack", 1)
	if err != nil {
		t.Fatalf("造手竿失败: %v", err)
	}
	if got := srv.fightReelCache(ctx, uid, handRod.GUID, &RolledFish{FishGUID: "fishx"}); got.valid {
		t.Fatal("手竿没有渔轮，不应生成磨损账")
	}
	// 空账与空 GUID 都不该写库、不该 panic。
	srv.settleFightReelWear(ctx, uid, handRod.GUID, "14/4")
	srv.settleFightReelWear(ctx, uid, "", "14/4")
	// 没在搏鱼时收到 14/8 也必须安全返回。
	sess := newProbeSess(srv)
	srv.recordFightReelLoad(&Ctx{Sess: sess, Main: 14, Sub: 8, Tree: nil, DecodeOK: true, Log: logging.Discard()})
}

// TestFightReelLoadTickUsesRealGaps 断过载时长的折算口径：本竿第一条上报按兜底
// 节拍起算，之后按两条上报之间的真实秒数，间隔过大只记上限，时钟回拨不倒扣。
func TestFightReelLoadTickUsesRealGaps(t *testing.T) {
	base := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	w := fightReelWear{}
	if got := w.loadTickSecondsLocked(base, defaultReelLoadTickSeconds, defaultReelLoadMaxTickSeconds); got != defaultReelLoadTickSeconds {
		t.Fatalf("首条上报应按兜底节拍 %.2fs 起算，实得 %.2fs", defaultReelLoadTickSeconds, got)
	}
	w.lastLoadAt = base
	cases := []struct {
		name   string
		offset time.Duration
		want   float64
	}{
		{"上报比节拍快只记真实间隔", 600 * time.Millisecond, 0.6},
		{"上报比节拍慢记到 4 秒", 4 * time.Second, 4},
		{"迟到上报只记上限", 40 * time.Second, defaultReelLoadMaxTickSeconds},
		{"时钟回拨不倒扣", -2 * time.Second, 0},
	}
	for _, tc := range cases {
		got := w.loadTickSecondsLocked(base.Add(tc.offset), defaultReelLoadTickSeconds, defaultReelLoadMaxTickSeconds)
		if math.Abs(got-tc.want) > 1e-9 {
			t.Fatalf("%s: 应折算 %.2fs，实得 %.2fs", tc.name, tc.want, got)
		}
	}
}

// TestFightReelBurnSecondsFollowRealGaps 断发热秒数不再假设“一条上报 = 1.9 秒”：
// 客户端上报翻倍密集时，累计秒数仍只跟着真实时间走，摩擦片不会被虚增时长多烧。
func TestFightReelBurnSecondsFollowRealGaps(t *testing.T) {
	srv := &Server{}
	base := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	ledger := newFightWearForTest(7.7, 5, 7.7, 11.55, 1, 1, true)
	var secs, wear float64
	for i := 0; i < 8; i++ {
		at := base.Add(time.Duration(i) * 500 * time.Millisecond)
		tick := ledger.loadTickSecondsLocked(at, defaultReelLoadTickSeconds, defaultReelLoadMaxTickSeconds)
		_, _, burn, _, seg := srv.fightReelTickWear(ledger, tick)
		wear += burn
		secs += tick
		ledger.SegSecs = seg
		ledger.lastLoadAt = at
	}
	// 首条 1.9 秒兜底，其余 7 条真实间隔 0.5 秒。
	if want := defaultReelLoadTickSeconds + 7*0.5; math.Abs(secs-want) > 1e-9 {
		t.Fatalf("发热秒数应按真实时长 %.2fs 累加，实得 %.2fs", want, secs)
	}
	// 同样的 8 条上报，按旧口径一律折 1.9 秒会多记近三倍时长，磨损必须更狠。
	fixed := newFightWearForTest(7.7, 5, 7.7, 11.55, 1, 1, true)
	var fixedWear float64
	for i := 0; i < 8; i++ {
		_, _, burn, _, seg := srv.fightReelTickWear(fixed, defaultReelLoadTickSeconds)
		fixedWear += burn
		fixed.SegSecs = seg
	}
	if !(wear < fixedWear) {
		t.Fatalf("密集上报按真实时长结算应更省：真实 %.4f 固定节拍 %.4f", wear, fixedWear)
	}
	// 同一瞬间的重复上报不带时长，也就一分磨损都不该记。
	dup := newFightWearForTest(7.7, 5, 7.7, 11.55, 1, 1, true)
	if ticks, slip, burn, burnSecs, _ := srv.fightReelTickWear(dup, 0); ticks != 0 || slip != 0 || burn != 0 || burnSecs != 0 {
		t.Fatalf("零时长上报不该产生任何磨损： ticks=%d slip=%v burn=%v", ticks, slip, burn)
	}
}

// TestFishFightPullKgfIsBodyWeight 钉住换算恒等式：27.f8 是牛（官方样本严格 =
// 体重kg×g），磨损账比较的泄力是公斤，所以入账前除以 newtonPerKgf 后必须恰好
// 等于 体重kg×冲刺倍率。期望值由量纲恒等式手算，不用实现代码复算。
func TestFishFightPullKgfIsBodyWeight(t *testing.T) {
	srv := &Server{}
	f := &RolledFish{FishGUID: "fish0000000000000000000000000001", Species: "perch", WeightG: 461, Grade: 2}
	profile := srv.fishFightProfileFor(f)
	// fishFightPullFor 末尾四舍五入到万分位：牛数最大偏差 5e-5，折公斤力约 5.1e-6。
	const tol = 1e-4
	if got, want := srv.fishFightPullKgfFor(f, profile), 0.461*profile.ForceScale; math.Abs(got-want) > tol {
		t.Fatalf("公斤力拉力应等于体重kg×冲刺倍率 %.6f，实得 %.6f", want, got)
	}
	// 对照：f8 下发的牛数口径必须保持不变。
	if got, want := srv.fishFightPullFor(f, profile), 0.461*newtonPerKgf*profile.ForceScale; math.Abs(got-want) > tol {
		t.Fatalf("27.f8 拉力应保持牛 %.6f，实得 %.6f", want, got)
	}
}

// TestFightReelWearBoundaryUsesKgfNotNewton 用实机事故数字做账本边界正反对照
// （2026-11-09 01:37：461g 鱼、1.87kg 泄力、3.2kg 片、9 条 14/8 全部封顶打滑）：
// 把 f8 牛数当公斤入账的旧口径必打滑，公斤力口径零磨损。回滚后此测试变红。
func TestFightReelWearBoundaryUsesKgfNotNewton(t *testing.T) {
	srv := &Server{}
	f := &RolledFish{FishGUID: "fish0000000000000000000000000002", Species: "perch", WeightG: 461, Grade: 2}
	profile := srv.fishFightProfileFor(f)
	oldWay := newFightWearForTest(3.2, 1.87, 2.32, srv.fishFightPullFor(f, profile), 1, 1.175, false)
	if ticks, slip, _, _, _ := srv.fightReelTickWear(oldWay, defaultReelLoadTickSeconds); ticks != 1 || slip <= 0 {
		t.Fatalf("对照失效：牛当公斤的旧口径应判打滑，实得 ticks=%d slip=%v", ticks, slip)
	}
	newWay := newFightWearForTest(3.2, 1.87, 2.32, srv.fishFightPullKgfFor(f, profile), 1, 1.175, false)
	if ticks, slip, burn, burnSecs, _ := srv.fightReelTickWear(newWay, defaultReelLoadTickSeconds); ticks != 0 || slip != 0 || burn != 0 || burnSecs != 0 {
		t.Fatalf("461g 鱼对 1.87kg 泄力不应有任何磨损: ticks=%d slip=%v burn=%v secs=%v",
			ticks, slip, burn, burnSecs)
	}
}

// TestFightReelCachePullKgfNoSlipForSmallFish 过真实装配接线复现同一场事故：
// fightReelCache 是牛→公斤的换算点，461g 鱼入账后必须低于该轮泄力且整场零磨损。
// 正控断言 f8 牛数确实高于泄力——保证这条测试在旧代码下真的会红。
func TestFightReelCachePullKgfNoSlipForSmallFish(t *testing.T) {
	srv, _ := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatalf("造竿失败: %v", err)
	}
	reel, err := srv.CreateItem(ctx, uid, "bclp_5004", "backpack", 1)
	if err != nil {
		t.Fatalf("造轮失败: %v", err)
	}
	mountReelOnRod(t, srv, rod.GUID, reel.GUID)
	f := &RolledFish{FishGUID: "fish0000000000000000000000000003", Species: "perch", WeightG: 461, Grade: 2}
	cached := srv.fightReelCache(ctx, uid, rod.GUID, f)
	if !cached.valid || cached.ratedKg <= 0 || cached.dragKg <= 0 {
		t.Fatalf("应生成有效磨损账: %+v", cached)
	}
	if cached.pullKg >= cached.dragKg {
		t.Fatalf("461g 鱼的公斤力拉力 %.4f 不应达到泄力 %.2f", cached.pullKg, cached.dragKg)
	}
	if ticks, slip, burn, burnSecs, _ := srv.fightReelTickWear(cached, defaultReelLoadTickSeconds); ticks != 0 || slip != 0 || burn != 0 || burnSecs != 0 {
		t.Fatalf("拉力不超泄力应整场零磨损: ticks=%d slip=%v burn=%v secs=%v",
			ticks, slip, burn, burnSecs)
	}
	if pullN := srv.fishFightPullFor(f, srv.fishFightProfileFor(f)); pullN <= cached.dragKg {
		t.Fatalf("正控失效：f8 牛数 %.4f 应高于泄力 %.2f，否则测不出旧 bug", pullN, cached.dragKg)
	}
}

// TestFightWearRolloverChargesBaseOncePerCast 钉住"每竿基准一竿一次"：
// 嘬饵否决/鱼离场的重排是同一竿续段，取走第一段（带基准）后新段不得再带基准，
// 只有真正换竿才重扣。修复前重排续段重新打基准，客户端重抛的 14/7 又把它当
// "上一竿未落库的账"写掉——实测同秒双条 reel_overload，零件整晚双倍消耗。
func TestFightWearRolloverChargesBaseOncePerCast(t *testing.T) {
	const rodGUID = "rod0000000000000000000000000000001"
	st := &FishState{}
	// 抛竿：本竿基准入账（折扣系数 1 = 非路亚钓组）。
	st.reelWear.markRodUsedLocked(rodGUID, 1)
	// 嘬饵否决/鱼离场重排：同一竿续段。
	pending, has := rollFightWearLedgerLocked(st, rodGUID, false, 1)
	if !has || !pending.BaseCharged {
		t.Fatalf("第一段应携带每竿基准落库: has=%v base=%v", has, pending.BaseCharged)
	}
	if st.reelWear.BaseCharged {
		t.Fatal("重排续段不应再带每竿基准")
	}
	// 无战斗磨损的续段不产生落库账（修复前这里会再写一条同秒基准）。
	if _, has := rollFightWearLedgerLocked(st, rodGUID, false, 1); has {
		t.Fatal("无战斗磨损的续段不应产生落库账")
	}
	// 真正换竿重抛：重新扣基准。
	rollFightWearLedgerLocked(st, rodGUID, true, 1)
	if !st.reelWear.BaseCharged {
		t.Fatal("新竿应重新扣每竿基准")
	}
	// 续段里发生的战斗磨损仍要落库；这段是换竿后新竿的首段，基准随它一起落库。
	st.reelWear.SlipWear = 0.1
	pending, has = rollFightWearLedgerLocked(st, rodGUID, false, 1)
	if !has || pending.SlipWear != 0.1 || !pending.BaseCharged {
		t.Fatalf("新竿首段的战斗磨损应随基准一起落库: has=%v slip=%v base=%v",
			has, pending.SlipWear, pending.BaseCharged)
	}
	// 再往后重排的续段恢复"不带基准"。
	pending, has = rollFightWearLedgerLocked(st, rodGUID, false, 1)
	if has && pending.BaseCharged {
		t.Fatalf("第二段续段不应再带基准: has=%v base=%v", has, pending.BaseCharged)
	}
}

// TestFightWearCastBaseLureFactor 钉住路亚族每竿基准折扣：折扣只进"每竿 + 泡水"，
// 过载秒与打滑次数不缩水；账本没记折扣（旧账）按 1 结算。期望值由手算样例给出：
// 品质 50 轮油 Fq=0.625，路亚每竿 = (0.32+0.04)×0.25 / 0.625 = 14.4bp。
func TestFightWearCastBaseLureFactor(t *testing.T) {
	srv := &Server{}
	if got := srv.reelCastBaseFactor(1); got != 1 {
		t.Fatalf("非路亚钓组不应打折，实得 %v", got)
	}
	if got := srv.reelCastBaseFactor(5); got != reelCastBaseFactorLureDefault {
		t.Fatalf("路亚族（钓组 5）应按默认折扣 %.2f，实得 %v", reelCastBaseFactorLureDefault, got)
	}
	lure := fightReelWear{BaseCharged: true, castBaseFactor: reelCastBaseFactorLureDefault}
	c := lure.partCounts()
	if c.Rods != reelCastBaseFactorLureDefault || c.Soaks != reelCastBaseFactorLureDefault {
		t.Fatalf("路亚折扣应进每竿与泡水计数，实得 rods=%v soaks=%v", c.Rods, c.Soaks)
	}
	// 旧账没记折扣：按 1 结算。
	legacy := fightReelWear{BaseCharged: true}.partCounts()
	if legacy.Rods != 1 || legacy.Soaks != 1 {
		t.Fatalf("未记折扣的账本应按 1 结算，实得 rods=%v soaks=%v", legacy.Rods, legacy.Soaks)
	}
	// 手算样例：品质 50 轮油在路亚折扣下的每竿消耗（矩阵是百分点，返回寿命比例）。
	want := (0.32 + 0.04) * reelCastBaseFactorLureDefault / 0.625 / 100
	if got := reelPartWearFor(reelPartCatOil, 50, lure.partCounts()); math.Abs(got-want) > 1e-9 {
		t.Fatalf("品质 50 轮油路亚每竿应为 %.6f（14.4bp），实得 %.6f", want, got)
	}
	// 打滑/过载是搏鱼事件账，不随钓组折扣缩水。
	fight := fightReelWear{BaseCharged: true, castBaseFactor: reelCastBaseFactorLureDefault,
		SlipTicks: 4, BurnSecs: 10}
	c = fight.partCounts()
	if c.SlipTicks != 4 || c.OverloadSecs != 10 {
		t.Fatalf("战斗磨损计数不应打折，实得 slip=%d secs=%v", c.SlipTicks, c.OverloadSecs)
	}
}
