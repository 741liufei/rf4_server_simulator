package gameserver

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

func TestTripResponseHex(t *testing.T) {
	srv, _ := newEconomyTxTestServer(t)
	ctx0 := context.Background()
	uid := int64(3911)
	place := "level_018_moskitnoe"
	if _, err := srv.DB.EnsureProfile(ctx0, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.DB.SQL.Exec(`UPDATE profile SET silver=50000 WHERE uid=?`, uid); err != nil {
		t.Fatal(err)
	}
	if err := srv.DB.KVSet(ctx0, tripDurationKVKey, "120"); err != nil {
		t.Fatal(err)
	}
	sess := &Session{ID: 1, Userid: uid, Srv: srv, log: logging.Discard()}
	sess.closed.Store(true)
	sess.startedAt = time.Now().Add(-time.Minute)
	ctx := tripTestCtx(srv, uid, tripArgs(place, false, 29800), false)
	body, handled := hPlaceTripTx(ctx)
	if !handled || body == nil {
		t.Fatal("购票失败")
	}
	// 单独编码 f23 条目看字节
	p, err := srv.BuildProfile307W(ctx0, uid, worldTotalGameMin(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	entry := p.Fields[23].Val.Items[0]
	eb, err := proto.EncodeValue(entry, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("f23 entry hex=%s", hex.EncodeToString(eb))
	t.Logf("f3(OPT) op=%v num=%v", entry.Fields[3].Op, entry.Fields[3].Num)
	// f23 容器 code 契约（2026-11-06 审查定案：保留 181，见
	// profilebuild.go BuildProfile307W f23 注释的实机验证依据）。
	if code := p.Fields[23].Val.ArgCodeStr(); code != "181" {
		t.Fatalf("307.f23 容器 code 应 181，实得 %q", code)
	}
	// 单独编码 307 全量（太长，只看 f23 段落定位）
	full, err := proto.EncodeValue(p, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	h := hex.EncodeToString(full)
	i := indexAll(h, "0303313831") // args("181")
	if i < 0 {
		t.Fatal("307 编码里找不到 args(\"181\") 段")
	}
	t.Logf("307.f23 段落=%s", h[i:i+160])
}

func indexAll(h, needle string) int {
	for i := 0; i+len(needle) <= len(h); i++ {
		if h[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
