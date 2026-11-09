package gameserver

import (
	"context"
	"testing"
	"time"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

// 入护落 catch_log 后必须补推 2/1 新档案。14/4 结算卡 f7 的档案快照早于
// catch_log 落库（鱼还在 Pending 里），f16 不含本尾；客户端又从不本地自增
// 星号（唯一写入点在档案应用路径），不补推就要等重登/换图才刷新。
func TestFishingKeepPushesFreshProfileForStarCount(t *testing.T) {
	srv, db := newDetachTestServer(t)
	connection, peer := cleanupTestTunnel(t, "keep-push-test")
	sess := registerBiteTestSession(t, srv, 501, connection)
	ctx := context.Background()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}

	const fishGUID = "22222222222222222222222222222222"
	st := srv.stateFor(uid)
	st.mu.Lock()
	st.Pending = &RolledFish{FishGUID: fishGUID, Species: "perch", Grade: 3,
		WeightG: 900, LengthM: .30, XP: 100, Silver: 10, LocLevel: "level_001_lake"}
	st.Hooked = true
	st.mu.Unlock()

	if before, err := db.RareSpeciesCount(ctx, uid, 3); err != nil || before != 0 {
		t.Fatalf("前置奖杯鱼种数应为 0: %d %v", before, err)
	}

	received := receiveTestPush(peer, 2, 1)
	body, handled := hFishingKeep(&Ctx{Sess: sess, Main: 14, Sub: 5, ExpectResponse: true, DecodeOK: true,
		Tree: proto.Args524(proto.GuidArr([]string{fishGUID})), Log: logging.Discard()})
	if !handled || len(body) == 0 {
		t.Fatal("入护未正常返回")
	}

	after, err := db.RareSpeciesCount(ctx, uid, 3)
	if err != nil || after != 1 {
		t.Fatalf("入护后奖杯鱼种数应为 1: %d %v", after, err)
	}

	select {
	case env := <-received:
		if env == nil || env.Main != 2 || env.Sub != 1 {
			t.Fatalf("缺少 2/1 档案推送: %+v", env)
		}
		value, _, err := proto.DecodeValue(env.Body, 0, srv.Table())
		if err != nil {
			t.Fatal(err)
		}
		if len(value.Items) == 0 {
			t.Fatal("2/1 推送缺少 args 内容")
		}
		if got := u32Of(value.Items[0].Fields[16].Num); got != 1 {
			t.Fatalf("推送档案 f16 应为 1（含本尾新奖杯鱼种），实得 %d", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("入护未推送 2/1 档案")
	}
}
