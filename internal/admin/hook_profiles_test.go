package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/store"
)

func TestHookProfileAPI(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := db.EnsureAdmin(ctx, "admin", "hook-test"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []store.HookBiteProfile{
		{
			ItemID: "triple_20686", NumericID: 1, Size: 25, LengthMM: 18,
			StrengthKg: 150, Quality: 100, Family: "treble", Brand: "ultra",
		},
		{
			ItemID: "clas_20042", NumericID: 2, Size: 2, LengthMM: 2,
			StrengthKg: 3, Quality: 25, Family: "classic", Brand: "syb",
		},
	} {
		if err := db.UpsertHookBiteProfile(ctx, p); err != nil {
			t.Fatal(err)
		}
	}

	s := NewServer(config.Admin{JWTSecret: "hook-test"}, db, logging.Discard(), WebFS())
	call := func(method, path, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, r)
		return w
	}

	page := call(http.MethodGet, "/", "", "")
	if page.Code != http.StatusOK ||
		!strings.Contains(page.Body.String(), "钩子手感") ||
		!strings.Contains(page.Body.String(), "view-hooks") {
		t.Fatalf("后台钩子页面不可用: %d", page.Code)
	}
	if got := call(http.MethodGet, "/api/hooks/profiles", "", "").Code; got != http.StatusUnauthorized {
		t.Fatalf("未认证读取应 401，实得 %d", got)
	}

	login := call(http.MethodPost, "/api/login", "",
		`{"username":"admin","password":"hook-test"}`)
	var auth struct {
		Token string `json:"token"`
	}
	if login.Code != http.StatusOK || json.Unmarshal(login.Body.Bytes(), &auth) != nil || auth.Token == "" {
		t.Fatalf("登录失败: %d %s", login.Code, login.Body)
	}

	list := call(http.MethodGet,
		"/api/hooks/profiles?q=ultra&size=25&family=treble&limit=500",
		auth.Token, "")
	if list.Code != http.StatusOK {
		t.Fatalf("读取型号列表失败: %d %s", list.Code, list.Body)
	}
	var got struct {
		Profiles []hookProfileRow `json:"profiles"`
		Total    int              `json:"total"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 1 || len(got.Profiles) != 1 ||
		got.Profiles[0].ItemID != "triple_20686" ||
		got.Profiles[0].EntryCapacityG <= 0 {
		t.Fatalf("筛选结果错误: %+v", got)
	}

	invalid := bytes.NewBufferString(`{"item_id":"triple_20686","solid_scale":4,"hold_scale":1}`)
	if resp := call(http.MethodPost, "/api/hooks/profiles", auth.Token, invalid.String()); resp.Code != http.StatusBadRequest {
		t.Fatalf("越界倍率应 400，实得 %d", resp.Code)
	}
	if resp := call(http.MethodPost, "/api/hooks/profiles", auth.Token,
		`{"item_id":"missing_hook","solid_scale":1,"hold_scale":1}`); resp.Code != http.StatusNotFound {
		t.Fatalf("不存在的型号应 404，实得 %d", resp.Code)
	}

	save := `{"item_id":"triple_20686","solid_scale":1.25,"hold_scale":0.8,"small_fish_floor":0.35,"over_capacity_hold_floor":0.6}`
	if resp := call(http.MethodPost, "/api/hooks/profiles", auth.Token, save); resp.Code != http.StatusOK {
		t.Fatalf("保存调参失败: %d %s", resp.Code, resp.Body)
	}
	p, err := db.GetHookBiteProfile(ctx, "triple_20686")
	if err != nil || p == nil || p.SolidScale != 1.25 || p.HoldScale != 0.8 ||
		p.SmallFishFloor != 0.35 || p.OverCapacityHoldFloor != 0.6 {
		t.Fatalf("调参未写回: profile=%+v err=%v", p, err)
	}
	var audits int
	if err := db.SQL.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_log WHERE action='hook_profile' AND target='triple_20686'`).Scan(&audits); err != nil ||
		audits != 1 {
		t.Fatalf("审计数=%d err=%v", audits, err)
	}
	assertQueueContains(t, db, "reload_tunables")
}
