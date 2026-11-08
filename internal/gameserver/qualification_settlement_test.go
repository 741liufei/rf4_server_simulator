package gameserver

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

// 咬钩快照必须发生在 14/11 扣饵之前：天然饵子实例被 consumeHookedBait 删除后，
// 14/4 不能再按 BaitGUIDs 反查（旧实现会得到零值，天然饵/饵类/味型/尺寸全不计数）。
func TestQualificationBiteSnapshotSurvivesBaitConsumption(t *testing.T) {
	s, db := newRigTestServer(t)
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopFishingRecoveryTimers(s) })
	sess, _ := newTunnelProbe(t, s)
	s.sessions[sess.ID] = sess
	uid := sess.Userid
	if _, err := db.EnsureProfile(t.Context(), uid); err != nil {
		t.Fatal(err)
	}

	rod, err := s.CreateItem(t.Context(), uid, "tele_10012_tl16", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	hook := mountHookForBaitTest(t, s, rod.GUID, 0)
	_, baitChild := baitTestMount(t, s, rod.GUID, "worm_01", 5, 350)
	if gotRodType, _, _ := s.qualificationRod(t.Context(), rod.GUID); gotRodType != rodTypeTelescope {
		t.Fatalf("测试竿应为手竿(%d)，实得 %d", rodTypeTelescope, gotRodType)
	}

	fish := &RolledFish{
		FishGUID: newFishGUID(), Species: "crucian", Grade: 1,
		WeightG: 500, LengthM: .25, HookGUID: hook,
	}
	st := s.fishingStateForRod(uid, rod.GUID)
	st.mu.Lock()
	st.RigType, st.CastActive, st.InWater = 1, true, true
	st.CastTs = time.Now().Add(-20 * time.Second)
	st.CastM = 28.5
	st.Armed = fish
	st.persist.sessionID = sess.ID
	ticket := fishingTimerTicket{st.castGeneration, st.biteTicket}
	st.mu.Unlock()

	// 走真实咬钩路径产生 pending 鱼，验证生产代码在扣饵前写入快照。
	s.onRodBiteTimerTicket(uid, st, ticket)
	st.mu.Lock()
	pending := st.Pending
	st.mu.Unlock()
	if pending != fish {
		t.Fatalf("咬钩未生成 pending 鱼: %+v", pending)
	}
	if pending.QualBait.ID != "worm_01" || pending.QualBait.Cat != 27 {
		t.Fatalf("咬钩饵快照 = %+v，期望 worm_01/27", pending.QualBait)
	}
	if pending.CastM != 28.5 {
		t.Fatalf("咬钩抛投距离 = %g，期望本杆实际 28.5", pending.CastM)
	}
	nowNight := worldIsNight(time.Now().Unix())
	if pending.IsNight != nowNight {
		t.Fatalf("咬钩夜间标记 = %v，期望 %v", pending.IsNight, nowNight)
	}

	// 14/11 中鱼会消耗并删除挂载的天然饵子实例。
	hFishingHooked(miscCtx(sess, 14, 11, rigRequestTree(t, proto.Guid(rod.GUID))))
	if row, _ := db.GetItemByGUID(t.Context(), baitChild); row != nil {
		t.Fatalf("14/11 后天然饵子实例应已删除: %+v", row)
	}

	// 14/4 结算仍必须能按快照累计「使用天然饵」任务 4003。
	ctx14 := &Ctx{Sess: sess, Main: 14, Sub: 4, ExpectResponse: true, Log: logging.Discard()}
	if body, ok := s.fishingEndSettle(ctx14); !ok || len(body) == 0 {
		t.Fatalf("14/4 未处理: ok=%v bytes=%d", ok, len(body))
	}
	states, err := db.QualificationStates(t.Context(), uid)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(states[4003].Counted); got != 1 {
		t.Fatalf("4003 天然饵计数 = %d，期望 1（旧实现扣饵后反查 GUID 会得到 0）", got)
	}
}

func mustQualificationRod(t *testing.T, s *Server, rodGUID string) (int64, string, int64) {
	t.Helper()
	rodType, _, testMax := s.qualificationRod(t.Context(), rodGUID)
	return rodType, "", int64(testMax)
}

// 14/2 记录的是本杆实际落点，不是钓具理论最大距离。
func TestCastSnapshotsRequestedDistance(t *testing.T) {
	srv, db := newRigTestServer(t)
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(t.Context(), uid); err != nil {
		t.Fatal(err)
	}
	rod, err := srv.CreateItem(t.Context(), uid, "tele_10012_tl16", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	fields := make([]proto.Field, 8)
	for i := range fields {
		fields[i] = proto.Field{Op: "U32", Num: ptrU64(0)}
	}
	fields[0] = proto.Field{Op: "U32", Num: ptrU64(uint64(proto.F32Bits(5)))}
	tree := proto.Args524(proto.Guid(rod.GUID), proto.Obj("olieodeoaof", 0, fields...))
	c := &Ctx{Sess: sess, Main: 14, Sub: 2, Tree: tree,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard()}
	if body, handled := hCast(c); !handled || len(body) == 0 {
		t.Fatalf("14/2 未处理: handled=%v len=%d", handled, len(body))
	}
	st := srv.stateFor(uid)
	st.mu.Lock()
	castM := st.CastM
	st.mu.Unlock()
	if castM != 5 {
		t.Fatalf("14/2 后 CastM = %g，期望本次实际落点 5（旧实现用钓具最大值）", castM)
	}
}

func TestWorldIsNightBoundaries(t *testing.T) {
	atHour := func(h int64) int64 { return worldDayEpochUnix + h*realSecPerGameHour }
	cases := map[int64]bool{0: true, 5: true, 6: false, 18: false, 19: true, 23: true}
	for hour, want := range cases {
		if got := worldIsNight(atHour(hour)); got != want {
			t.Fatalf("%02d:00 夜间 = %v，期望 %v", hour, got, want)
		}
	}
}

// 资格判定与经验/回执同事务：资格表写失败必须回滚整笔结算；重放从回执补发
// 本尾鱼的资格变化，但不会重复计数。
func TestQualificationSettlementAtomicAndReplayable(t *testing.T) {
	s, db := newDetachTestServer(t)
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	fish := &RolledFish{
		FishGUID: newFishGUID(), Species: "perch", Grade: 1, WeightG: 500, LengthM: .2,
		QualBait: qualificationBait{ID: "worm_01", Cat: 27},
	}
	ev := qualificationEvent{Kind: qevFishLanded, Species: fish.Species, WeightG: fish.WeightG, RodType: 1}

	// 资格表不可用时，整笔结算（含经验与回执）必须回滚。
	if _, err := db.SQL.ExecContext(ctx, `ALTER TABLE player_qualifications RENAME TO player_qualifications_bak`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.settleCatchOnceWithQualification(ctx, uid, fish, 1, &ev); err == nil {
		t.Fatal("资格写入失败时结算应返回错误")
	}
	if _, err := db.SQL.ExecContext(ctx, `ALTER TABLE player_qualifications_bak RENAME TO player_qualifications`); err != nil {
		t.Fatal(err)
	}
	key := catchSettlementKey(uid, fish.FishGUID)
	if _, _, err := db.KVGet(ctx, key); err == nil {
		t.Fatal("资格写入失败后不应留下结算回执")
	}
	profile, err := db.EnsureProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if profile.XP != 0 {
		t.Fatalf("资格写入失败后经验已入账: xp=%d", profile.XP)
	}

	// 重试成功后资格与回执同时提交。
	first, err := s.settleCatchOnceWithQualification(ctx, uid, fish, 1, &ev)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.qualChanges[1001].Counted) != 1 {
		t.Fatalf("首轮结算应写入 1001 进度，实得 %+v", first.qualChanges)
	}
	states, err := db.QualificationStates(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(states[1001].Counted); got != 1 {
		t.Fatalf("1001 进度 = %d，期望 1", got)
	}

	// 新进程重放同一尾鱼：返回回执里的资格变化，但计数不增加。
	fresh := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "replay"}}, logging.Discard(), nil)
	fresh.SetDB(db)
	replay, err := fresh.settleCatchOnceWithQualification(ctx, uid, fish, 1, &ev)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.replayed {
		t.Fatal("同一尾鱼第二次结算应走重放")
	}
	if len(replay.qualChanges[1001].Counted) != 1 {
		t.Fatalf("重放应从回执补发 1001 变化，实得 %+v", replay.qualChanges)
	}
	states, err = db.QualificationStates(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(states[1001].Counted); got != 1 {
		t.Fatalf("重放后 1001 进度 = %d，期望仍为 1", got)
	}
}

func catchSettlementKey(uid int64, fishGUID string) string {
	return fmt.Sprintf("catch-settlement:%d:%s", uid, strings.ToLower(fishGUID))
}
