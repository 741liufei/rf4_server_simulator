package gameserver

import (
	"context"
	"testing"
	"time"

	"rf4server/internal/logging"
)

// TestTripCaptureSimulation 模拟客户端视角：登录 capture（f13 基址）→
// 购票即启表 → 进图 capture 1/14 档案 → 用客户端公式算剩余。
func TestTripCaptureSimulation(t *testing.T) {
	srv, _ := newEconomyTxTestServer(t)
	ctx0 := context.Background()
	uid := int64(3910)
	place := "level_018_moskitnoe"
	if _, err := srv.DB.EnsureProfile(ctx0, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.DB.SQL.Exec(`UPDATE profile SET silver=50000 WHERE uid=?`, uid); err != nil {
		t.Fatal(err)
	}
	sess := &Session{ID: 1, Userid: uid, Srv: srv, log: logging.Discard()}
	sess.closed.Store(true)
	sess.startedAt = time.Now().Add(-10 * time.Minute) // 模拟已在线 10 现实分钟

	// 登录 capture：f13.f0 基址。
	p0, err := srv.BuildProfile307W(ctx0, uid, worldTotalGameMin(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	wBase := u64Of(p0.Fields[13].Val.Fields[0].Num) // f13.f0 日内分钟
	t.Logf("登录: f13.f0基址=%d sessionGameMin=%d", wBase, worldTotalGameMin(time.Now()))

	// 购票即启表。
	if err := srv.DB.KVSet(ctx0, tripDurationKVKey, "120"); err != nil {
		t.Fatal(err)
	}
	ctx := tripTestCtx(srv, uid, tripArgs(place, false, 29800), false)
	if body, handled := hPlaceTripTx(ctx); !handled || body == nil {
		t.Fatal("购票失败")
	}

	// 购票即启表；回拨 10 秒模拟在线消耗 10 秒。
	if _, err := srv.DB.SQL.Exec(
		`UPDATE trip_state SET last_consume_ts=last_consume_ts-10 WHERE uid=? AND place=?`, uid, place); err != nil {
		t.Fatal(err)
	}
	_ = sess.consumeTripTimes(time.Now()) // 消 10 秒 → 剩余 110s

	p1, err := srv.BuildProfile307W(ctx0, uid, worldTotalGameMin(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	g13 := u64Of(p1.Fields[13].Val.Fields[0].Num)
	f23 := p1.Fields[23].Val.Items
	if len(f23) == 0 {
		t.Fatal("f23 空")
	}
	started := u64Of(f23[0].Fields[3].Num)
	start := int32(uint32(u64Of(f23[0].Fields[4].Num)))
	dur := u64Of(f23[0].Fields[2].Num)
	t.Logf("进图: f13.f0=%d f23{dur=%d started=%d start=%d} 服务端W=%d",
		g13, dur, started, start, worldTotalGameMin(time.Now()))

	// 客户端视角（反编译 pioghgjeelp）：剩余 = start(到期锚点) − W。
	wClient := worldTotalGameMin(time.Now()) // 服务端同源近似
	remain := int64(start) - wClient
	t.Logf("客户端公式剩余=%d 游戏分钟 (%.1f 现实分钟)", remain, float64(remain)/24)
	if started == 1 && remain <= 0 {
		t.Fatalf("客户端会判过期: remain=%d", remain)
	}
	if started != 1 {
		t.Fatalf("启表后 started 应为 1: %d", started)
	}
}
