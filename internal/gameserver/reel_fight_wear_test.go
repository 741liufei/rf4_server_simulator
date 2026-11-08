package gameserver

// 搏鱼渔轮磨损回归：通道一（泄力打滑）只在未锁轮且鱼力超过当前泄力时计；通道二
// （锁轮过载）按发热曲线烧，且过载段之间不累积（解锁即段作废，下段从曲线开头重爬）。
// 摩擦片剩余寿命必须同步压低泄力与锁轮承力，结算每竿一次、重发不重复扣。
// 口径见 specs/changes/reel-friction-life-model/spec.md。

import (
	"context"
	"math"
	"testing"

	"rf4server/internal/logging"
)

// newFightWearForTest 造一条搏鱼磨损账：ratedKg 是摩擦片额定，drag/locked 是此刻
// 泄力与锁轮承力，pull 是鱼拉力。oilBurn/oilWear 是轮油对烧穿时长与打滑的系数。
func newFightWearForTest(rated, drag, locked, pull, oilBurn, oilWear float64, lockedState bool) fightReelWear {
	return fightReelWear{
		valid: true, fishGUID: "fish0000000000000000000000000001",
		rodGUID: "rod0000000000000000000000000000001", reelGUID: "reel000000000000000000000000000001",
		locked: lockedState, dragKg: drag, lockedKg: locked, ratedKg: rated,
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
	// 超泄力 1.5 倍：基准 0.5% 再乘超出比例。
	w = newFightWearForTest(7.7, 5, 9, 7.5, 1, 1, false)
	ticks, slip, burn, _, _ := srv.fightReelTickWear(w, tick)
	if ticks != 1 || burn != 0 {
		t.Fatalf("超泄力应记一次打滑且不按曲线烧，实得 ticks=%d burn=%v", ticks, burn)
	}
	if want := reelSlipWear * 1.5; math.Abs(slip-want) > 1e-12 {
		t.Fatalf("单次打滑磨损应为 %.6f，实得 %.6f", want, slip)
	}
	// 超出比例封顶 2 倍：鱼再猛也不能一次磨穿一套片子。
	w = newFightWearForTest(7.7, 1, 9, 50, 1, 1, false)
	if _, slip, _, _, _ := srv.fightReelTickWear(w, tick); math.Abs(slip-reelSlipWear*2) > 1e-12 {
		t.Fatalf("打滑磨损应封顶 2 倍基准，实得 %.6f", slip)
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
	if math.Abs(best-reelSlipWear*1.5*0.8) > 1e-12 || math.Abs(worst-reelSlipWear*1.5*1.2) > 1e-12 {
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
