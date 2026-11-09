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

// 玩家列表的在线灯来自 gameserver 发布的 stats:online_uids；键缺失或含脏值按离线处理。
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

	// 名单含空格和非数字项时仍取其中的合法 UID。
	if err := db.KVSet(ctx, "stats:online_uids", " 1000001 ,oops"); err != nil {
		t.Fatal(err)
	}
	if got := onlineByUID(); !got[onlineUID] || got[offlineUID] {
		t.Fatalf("在线名单解析错误: %v", got)
	}

	// 空串表示无人在线。
	if err := db.KVSet(ctx, "stats:online_uids", ""); err != nil {
		t.Fatal(err)
	}
	if got := onlineByUID(); got[onlineUID] || got[offlineUID] {
		t.Fatalf("空名单时应全部离线: %v", got)
	}
}
