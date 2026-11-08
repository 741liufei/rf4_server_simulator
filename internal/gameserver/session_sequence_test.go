package gameserver

import (
	"testing"

	"rf4server/internal/config"
	"rf4server/internal/logging"
)

func TestCallIDContinuesAcrossSessionAndRestart(t *testing.T) {
	t.Parallel()
	srv, db := newDetachTestServer(t)
	if a, b := srv.nextCallID("token-x"), srv.nextCallID("token-x"); a != 1 || b != 2 {
		t.Fatalf("同 token 序号=%d,%d，期望1,2", a, b)
	}
	// 模拟进程重启：新 Server 接同一个 DB，必须从持久化峰值继续。
	srv2 := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "seq"}}, logging.Discard(), nil)
	srv2.SetDB(db)
	srv2.restoreCallSeq("token-x")
	if got := srv2.nextCallID("token-x"); got != 3 {
		t.Fatalf("重启后续号=%d，期望3", got)
	}
	if got := srv2.nextCallID("token-y"); got != 1 {
		t.Fatalf("不同 token 应独立从1开始，实得%d", got)
	}
}
