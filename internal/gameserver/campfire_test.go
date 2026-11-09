package gameserver

import (
	"math"
	"testing"
	"time"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

func seedCampfireMaterials(t *testing.T, srv *Server, woodQty, matchQty int64) (woodGUID, matchGUID string) {
	t.Helper()
	if _, err := srv.DB.SQL.Exec(`INSERT INTO item_def(numeric_id,item_id,category,attrs_json)
		VALUES(920001,'woodstack',42,'{}'),(920002,'matches',42,'{}')`); err != nil {
		t.Fatal(err)
	}
	woodItem := drinkUseTestItem(t, srv, "woodstack", woodQty, 0)
	matchItem := drinkUseTestItem(t, srv, "matches", matchQty, 0)
	return woodItem.GUID, matchItem.GUID
}

func TestCampfireStartReturnsActiveFireAndPersists(t *testing.T) {
	srv, db := newRigTestServer(t)
	sess := newProbeSess(srv)
	st := srv.stateFor(sess.Userid)
	st.mu.Lock()
	st.Loc = "level_001_lake"
	st.Spot = 222
	st.mu.Unlock()
	woodGUID, matchGUID := seedCampfireMaterials(t, srv, 5, 50)

	ctx := &Ctx{Sess: sess, Main: 3, Sub: 11, DecodeOK: true, Log: logging.Discard()}
	body, ok := hCampfireStart(ctx)
	if !ok || len(body) == 0 {
		t.Fatal("3/11 篝火点火未处理")
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil || dec.Class != "ihaaofcmopp" {
		t.Fatalf("3/11 应回 802 ihaaofcmopp，实得 %v err=%v", dec, err)
	}
	// 到期分钟与 307.f13.f0 同时间线（世界总游戏分钟）。
	clientNow := float64(worldTotalGameMin(time.Now()))
	wantExpiry := clientNow + float64(defCampfireDurationGameMin)
	gotExpiry := float64(dec.Fields[0].U32())
	if math.Abs(gotExpiry-wantExpiry) > 2 {
		t.Fatalf("篝火结束游戏分钟=%v，当前=%.2f，期望约=%.2f", gotExpiry, clientNow, wantExpiry)
	}
	if f1 := dec.Fields[1].Val; f1 == nil || f1.Kind != proto.KindArgs || f1.ArgCodeStr() != "61" || f1.ArgCount() != 1 {
		t.Fatalf("802.f1 应携带扣减后的木柴实例，实得 %v", f1)
	}
	if f2 := dec.Fields[2].Val; f2 == nil || f2.Kind != proto.KindArgs || f2.ArgCodeStr() != "61" || f2.ArgCount() != 1 {
		t.Fatalf("802.f2 应携带扣减后的火柴实例，实得 %v", f2)
	}
	if f3 := dec.Fields[3].Val; f3 == nil || f3.Kind != proto.KindGuidArr || len(f3.Items) != 0 {
		t.Fatalf("802.f3 应为空 GUID 数组，实得 %v", f3)
	}
	drinkUseTestQty(t, srv, woodGUID, 4, 0)
	drinkUseTestQty(t, srv, matchGUID, 49, 0)

	state, err := db.GetCampfireState(t.Context(), sess.Userid)
	if err != nil || state == nil {
		t.Fatalf("篝火状态未落库: %+v err=%v", state, err)
	}
	if state.Loc != "level_001_lake" || state.Spot != 222 || state.DurationMin != defCampfireDurationGameMin || state.ExpiresFT <= int64(proto.WindowsFiletimeNow()) {
		t.Fatalf("篝火落库状态不符: %+v", state)
	}
	remainingRealSec := float64(state.ExpiresFT-int64(proto.WindowsFiletimeNow())) / 10_000_000
	if math.Abs(remainingRealSec-gameMinutesToRealDuration(defCampfireDurationGameMin).Seconds()) > 3 {
		t.Fatalf("篝火现实剩余秒=%.2f，期望约=%.2f", remainingRealSec, gameMinutesToRealDuration(defCampfireDurationGameMin).Seconds())
	}
	expiry := state.ExpiresFT
	firstClientExpiry := dec.Fields[0].U32()

	// 再次按 Y 不应重置/延长，也不应重复扣材料。
	ctx2 := &Ctx{Sess: sess, Main: 3, Sub: 11, DecodeOK: true, Log: logging.Discard()}
	body2, ok := hCampfireStart(ctx2)
	if !ok || len(body2) == 0 {
		t.Fatal("重复点火未处理")
	}
	dec2, _, err := proto.DecodeValue(body2, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	if dec2.Fields[0].U32() != firstClientExpiry {
		t.Fatalf("重复点火不应改变客户端结束分钟: first=%d second=%d", firstClientExpiry, dec2.Fields[0].U32())
	}
	drinkUseTestQty(t, srv, woodGUID, 4, 0)
	drinkUseTestQty(t, srv, matchGUID, 49, 0)
	state2, err := db.GetCampfireState(t.Context(), sess.Userid)
	if err != nil || state2 == nil || state2.ExpiresFT != expiry {
		t.Fatalf("重复点火不应改变到期时间: first=%d second=%+v err=%v", expiry, state2, err)
	}
}

// 燃烧时长走 tunable：改成 480 游戏分钟后，新点燃的篝火按 480 落库，
// 802.f0 也按 480 回报（与 307.f13.f0 同一时间线）。
func TestCampfireDurationTunable(t *testing.T) {
	srv, db := newRigTestServer(t)
	srv.tunCache = map[string]float64{tunableCampfireDurationGameMin: 480}
	srv.tunAt = time.Now()
	sess := newProbeSess(srv)
	seedCampfireMaterials(t, srv, 5, 5)

	ctx := &Ctx{Sess: sess, Main: 3, Sub: 11, CallID: 74, ExpectResponse: true,
		Log: logging.Discard(), replyCapture: &ctxReplyCapture{}}
	body, handled := hCampfireStart(ctx)
	if !handled || len(body) == 0 {
		t.Fatalf("3/11 应答: handled=%v body=%d", handled, len(body))
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	clientNow := float64(worldTotalGameMin(time.Now()))
	if got, want := float64(dec.Fields[0].U32()), clientNow+480; math.Abs(got-want) > 2 {
		t.Fatalf("802.f0=%v，期望约 %.2f（480 游戏分钟）", got, want)
	}
	state, err := db.GetCampfireState(t.Context(), sess.Userid)
	if err != nil || state == nil || state.DurationMin != 480 {
		t.Fatalf("篝火落库时长应为 480 游戏分钟: %+v err=%v", state, err)
	}
	remainingRealSec := float64(state.ExpiresFT-int64(proto.WindowsFiletimeNow())) / 10_000_000
	if math.Abs(remainingRealSec-gameMinutesToRealDuration(480).Seconds()) > 3 {
		t.Fatalf("篝火现实剩余秒=%.2f，期望约=%.2f", remainingRealSec,
			gameMinutesToRealDuration(480).Seconds())
	}
}

// 误填（0、负数、超过 1 游戏日）必须被钳制到合法区间，不能产生异常到期值。
func TestCampfireDurationTunableClamped(t *testing.T) {
	cases := []struct {
		name string
		in   float64
		want int64
	}{
		{"zero", 0, minCampfireDurationGameMin},
		{"negative", -30, minCampfireDurationGameMin},
		{"too-large", 100000, maxCampfireDurationGameMin},
		{"unset-falls-back-to-default", 0, defCampfireDurationGameMin},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newRigTestServer(t)
			srv.tunCache = map[string]float64{tunableCampfireDurationGameMin: tc.in}
			srv.tunAt = time.Now()
			if tc.name == "unset-falls-back-to-default" {
				srv.tunCache = map[string]float64{}
			}
			if got := srv.campfireDurationGameMin(); got != tc.want {
				t.Fatalf("燃烧时长=%d，期望 %d", got, tc.want)
			}
		})
	}
}

// 运维把燃烧时长调短后，正在燃烧的旧篝火仍应被复用：玩家不能为一堆
// 已经点着的火重复付材料。窗口上限必须按记录自身的 duration_min 判定。
func TestCampfireReuseSurvivesDurationShrink(t *testing.T) {
	srv, db := newRigTestServer(t)
	srv.tunCache = map[string]float64{tunableCampfireDurationGameMin: 480}
	srv.tunAt = time.Now()
	sess := newProbeSess(srv)
	woodGUID, matchGUID := seedCampfireMaterials(t, srv, 5, 5)

	ctx := &Ctx{Sess: sess, Main: 3, Sub: 11, CallID: 75, ExpectResponse: true,
		Log: logging.Discard(), replyCapture: &ctxReplyCapture{}}
	body, handled := hCampfireStart(ctx)
	if !handled || len(body) == 0 {
		t.Fatalf("首次点火应成功: handled=%v body=%d", handled, len(body))
	}
	first, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	before, err := db.GetCampfireState(t.Context(), sess.Userid)
	if err != nil || before == nil {
		t.Fatalf("首次点火未落库: %+v err=%v", before, err)
	}
	drinkUseTestQty(t, srv, woodGUID, 4, 0)
	drinkUseTestQty(t, srv, matchGUID, 4, 0)

	// 调短到 60 游戏分钟后再点一次：仍在燃烧，必须复用且不扣材料。
	srv.tunCache = map[string]float64{tunableCampfireDurationGameMin: 60}
	srv.tunAt = time.Now()
	ctx2 := &Ctx{Sess: sess, Main: 3, Sub: 11, CallID: 76, ExpectResponse: true,
		Log: logging.Discard(), replyCapture: &ctxReplyCapture{}}
	body2, handled := hCampfireStart(ctx2)
	if !handled || len(body2) == 0 {
		t.Fatalf("调短后重复点火应复用: handled=%v body=%d", handled, len(body2))
	}
	second, _, err := proto.DecodeValue(body2, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	if second.Fields[0].U32() != first.Fields[0].U32() {
		t.Fatalf("复用不应改变结束分钟: first=%d second=%d",
			first.Fields[0].U32(), second.Fields[0].U32())
	}
	drinkUseTestQty(t, srv, woodGUID, 4, 0)
	drinkUseTestQty(t, srv, matchGUID, 4, 0)
	after, err := db.GetCampfireState(t.Context(), sess.Userid)
	if err != nil || after == nil || after.ExpiresFT != before.ExpiresFT || after.DurationMin != 480 {
		t.Fatalf("复用不应改写篝火状态: before=%+v after=%+v err=%v", before, after, err)
	}
}

func TestCampfireConsumesMaterials(t *testing.T) {
	srv, _ := newRigTestServer(t)
	woodGUID, matchGUID := seedCampfireMaterials(t, srv, 5, 50)
	sess := newProbeSess(srv)
	ctx := &Ctx{Sess: sess, Main: 3, Sub: 11, CallID: 70, ExpectResponse: true,
		Log: logging.Discard(), replyCapture: &ctxReplyCapture{}}
	body, handled := hCampfireStart(ctx)
	if !handled || len(body) == 0 {
		t.Fatalf("3/11 应答: handled=%v body=%d", handled, len(body))
	}
	drinkUseTestQty(t, srv, woodGUID, 4, 0)
	drinkUseTestQty(t, srv, matchGUID, 49, 0)
	if len(ctx.afterResponse) == 0 {
		t.Fatal("材料变更后应有背包推送")
	}
}

func TestCampfireRequiresBothMaterials(t *testing.T) {
	t.Run("missing both", func(t *testing.T) {
		srv, db := newRigTestServer(t)
		sess := newProbeSess(srv)
		ctx := &Ctx{Sess: sess, Main: 3, Sub: 11, CallID: 71, ExpectResponse: true,
			Log: logging.Discard(), replyCapture: &ctxReplyCapture{}}
		body, handled := hCampfireStart(ctx)
		if !handled || body != nil {
			t.Fatalf("无材料应失败: handled=%v body=%v", handled, body)
		}
		_, present, _, failure := ctx.replyCapture.snapshot()
		if !present || !failure {
			t.Fatal("无材料应回失败")
		}
		if state, err := db.GetCampfireState(t.Context(), sess.Userid); err != nil || state != nil {
			t.Fatalf("无材料不应落篝火状态: %+v %v", state, err)
		}
	})

	t.Run("state write failure rolls back materials", func(t *testing.T) {
		srv, db := newRigTestServer(t)
		woodGUID, matchGUID := seedCampfireMaterials(t, srv, 5, 5)
		if _, err := db.SQL.Exec(`CREATE TRIGGER fail_campfire_state BEFORE INSERT ON campfire_state
			BEGIN SELECT RAISE(ABORT,'campfire state failed'); END`); err != nil {
			t.Fatal(err)
		}
		sess := newProbeSess(srv)
		ctx := &Ctx{Sess: sess, Main: 3, Sub: 11, CallID: 73, ExpectResponse: true,
			Log: logging.Discard(), replyCapture: &ctxReplyCapture{}}
		body, handled := hCampfireStart(ctx)
		if !handled || body != nil {
			t.Fatalf("篝火状态写入失败应整体失败: handled=%v body=%v", handled, body)
		}
		drinkUseTestQty(t, srv, woodGUID, 5, 0)
		drinkUseTestQty(t, srv, matchGUID, 5, 0)
		if state, err := db.GetCampfireState(t.Context(), sess.Userid); err != nil || state != nil {
			t.Fatalf("事务失败不应落篝火状态: %+v %v", state, err)
		}
	})

	t.Run("missing matches keeps wood", func(t *testing.T) {
		srv, db := newRigTestServer(t)
		if _, err := srv.DB.SQL.Exec(`INSERT INTO item_def(numeric_id,item_id,category,attrs_json)
			VALUES(920001,'woodstack',42,'{}')`); err != nil {
			t.Fatal(err)
		}
		wood := drinkUseTestItem(t, srv, "woodstack", 5, 0)
		sess := newProbeSess(srv)
		ctx := &Ctx{Sess: sess, Main: 3, Sub: 11, CallID: 72, ExpectResponse: true,
			Log: logging.Discard(), replyCapture: &ctxReplyCapture{}}
		body, handled := hCampfireStart(ctx)
		if !handled || body != nil {
			t.Fatalf("缺火柴应失败: handled=%v body=%v", handled, body)
		}
		drinkUseTestQty(t, srv, wood.GUID, 5, 0)
		if state, err := db.GetCampfireState(t.Context(), sess.Userid); err != nil || state != nil {
			t.Fatalf("缺火柴不应落篝火状态: %+v %v", state, err)
		}
	})
}
