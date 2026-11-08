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
	wantExpiry := clientNow + float64(defaultCampfireDurationMin)
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
	if state.Loc != "level_001_lake" || state.Spot != 222 || state.DurationMin != defaultCampfireDurationMin || state.ExpiresFT <= int64(proto.WindowsFiletimeNow()) {
		t.Fatalf("篝火落库状态不符: %+v", state)
	}
	remainingRealSec := float64(state.ExpiresFT-int64(proto.WindowsFiletimeNow())) / 10_000_000
	if math.Abs(remainingRealSec-gameMinutesToRealDuration(defaultCampfireDurationMin).Seconds()) > 3 {
		t.Fatalf("篝火现实剩余秒=%.2f，期望约=%.2f", remainingRealSec, gameMinutesToRealDuration(defaultCampfireDurationMin).Seconds())
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
