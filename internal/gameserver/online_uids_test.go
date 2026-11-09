package gameserver

import (
	"slices"
	"testing"

	"rf4server/internal/config"
	"rf4server/internal/logging"
)

// 在线 UID 去重升序；同一玩家的重连会话只算一个，未登录或已关闭会话不计入。
func TestOnlineUIDsDedupSortAndSkipAnonymous(t *testing.T) {
	srv := NewServer(config.Game{}, logging.Discard(), nil)
	for i, uid := range []int64{7358, 7357, 7357, 0, 7359} {
		sess := newSession(srv, uint64(i+1), "tok", uid, nil)
		if uid == 7359 {
			sess.closed.Store(true)
		}
		srv.sessions[sess.ID] = sess
	}
	if got, want := srv.OnlineUIDs(), []int64{7357, 7358}; !slices.Equal(got, want) {
		t.Fatalf("online uids = %v, want %v", got, want)
	}
}
