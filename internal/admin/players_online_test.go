package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"rf4server/internal/config"
	"rf4server/internal/logging"
)

// 玩家列表的在线灯来自 gameserver 发布的带时间戳 stats:online_uids 快照；缺失、损坏或过期按离线处理。
func TestPlayersListOnlineFlag(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	onlineUID, offlineUID := int64(1000001), int64(1000002)
	for _, uid := range []int64{onlineUID, offlineUID} {
		if _, err := db.EnsureProfile(ctx, uid); err != nil {
			t.Fatal(err)
		}
	}
	srv := NewServer(config.Admin{JWTSecret: "test-online"}, db, logging.Discard(), WebFS())
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{
		Username:         "online-tester",
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))},
	}).SignedString(srv.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	onlineByUID := func() map[int64]bool {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/players", nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("玩家列表应 200, 实得 %d: %s", w.Code, w.Body.String())
		}
		var list []playerRow
		if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
			t.Fatalf("解析玩家列表: %v", err)
		}
		out := make(map[int64]bool, len(list))
		for _, p := range list {
			out[p.UID] = p.Online
		}
		return out
	}

	// 游戏服尚未发布名单：不报错，全部离线。
	if got := onlineByUID(); got[onlineUID] || got[offlineUID] {
		t.Fatalf("未发布名单时应全部离线: %v", got)
	}

	writeSnapshot := func(updatedAt time.Time, uids ...int64) {
		t.Helper()
		raw, err := json.Marshal(onlineUIDsSnapshot{UpdatedAt: updatedAt.Unix(), UIDs: uids})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.KVSet(ctx, "stats:online_uids", string(raw)); err != nil {
			t.Fatal(err)
		}
	}

	// 新鲜快照中的合法 UID 应在线；非法 UID 忽略。
	writeSnapshot(time.Now(), onlineUID, 0, -1)
	if got := onlineByUID(); !got[onlineUID] || got[offlineUID] {
		t.Fatalf("在线快照解析错误: %v", got)
	}

	// 非 JSON 或旧格式不再兼容，按全部离线处理。
	if err := db.KVSet(ctx, "stats:online_uids", " 1000001 ,oops"); err != nil {
		t.Fatal(err)
	}
	if got := onlineByUID(); got[onlineUID] || got[offlineUID] {
		t.Fatalf("损坏快照应按全部离线: %v", got)
	}

	// 超过发布有效期的快照按全部离线处理，覆盖进程异常退出后残留旧名单的场景。
	writeSnapshot(time.Now().Add(-onlineUIDsMaxAge-time.Second), onlineUID)
	if got := onlineByUID(); got[onlineUID] || got[offlineUID] {
		t.Fatalf("过期快照应按全部离线: %v", got)
	}

	// 空串表示无人在线。
	if err := db.KVSet(ctx, "stats:online_uids", ""); err != nil {
		t.Fatal(err)
	}
	if got := onlineByUID(); got[onlineUID] || got[offlineUID] {
		t.Fatalf("空名单时应全部离线: %v", got)
	}
}
