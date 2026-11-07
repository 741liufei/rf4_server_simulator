package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"rf4server/internal/config"
	"rf4server/internal/logging"
)

// TestUpdateInfoInjection：launcher 注入检测结果后，/api/me 与
// /api/dashboard 应携带 update 字段；未注入/清空时不得出现该键。
func TestUpdateInfoInjection(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	_ = db.EnsureAdmin(context.Background(), "admin", "admin123")
	srv := NewServer(config.Admin{HTTPAddr: "127.0.0.1:0", JWTSecret: "test"}, db, logging.Discard(), nil)
	// 不读 errCh：Start 只在异常退出时发值，正常 Shutdown 后它不会关闭，
	// 阻塞等待会让整个测试包挂到超时（TestAdminAPI 同款约定：进程退出即回收）。
	_, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + srv.Addr()
	client := &http.Client{Timeout: 5 * time.Second}

	call := func(path, token string) (int, string) {
		r, _ := http.NewRequest(http.MethodGet, base+path, bytes.NewReader(nil))
		r.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return resp.StatusCode, buf.String()
	}

	// 登录取 token
	r, _ := http.NewRequest(http.MethodPost, base+"/api/login", strings.NewReader(`{"username":"admin","password":"admin123"}`))
	r.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	var lr struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&lr)
	resp.Body.Close()
	if lr.Token == "" {
		t.Fatal("登录失败")
	}
	token := lr.Token

	// 未注入：两个端点都不得出现 update 键（nil 用 omitempty 语义省略）
	for _, path := range []string{"/api/me", "/api/dashboard"} {
		code, body := call(path, token)
		if code != 200 || strings.Contains(body, `"update"`) {
			t.Fatalf("未注入时 %s 应无 update 键: code=%d body=%s", path, code, body)
		}
	}

	// 注入后：两端点都携带 update 字段
	srv.SetUpdateInfo(&UpdateInfo{
		Available:             true,
		Channel:               "stable",
		CurrentVersion:        "v1.1.0",
		LatestVersion:         "v1.2.0",
		NotesURL:              "https://github.com/glance522/rf4_server_simulator/releases/tag/v1.2.0",
		DownloadURL:           "https://github.com/glance522/rf4_server_simulator/releases/latest",
		DownloadMirrorURL:     "https://gh-proxy.com/https://github.com/glance522/rf4_server_simulator/releases/download/v1.2.0/a.zip",
		PreviewVersion:        "preview-20261107T110000Z-abc1234",
		PreviewDownloadMirror: "https://gh-proxy.com/https://github.com/glance522/rf4_server_simulator/releases/download/preview-20261107T110000Z-abc1234/b.zip",
	})
	for _, path := range []string{"/api/me", "/api/dashboard"} {
		code, body := call(path, token)
		if code != 200 || !strings.Contains(body, `"latest_version":"v1.2.0"`) {
			t.Fatalf("注入后 %s 应携带 update: code=%d body=%s", path, code, body)
		}
		if !strings.Contains(body, `"download_mirror_url"`) || !strings.Contains(body, `"preview_version":"preview-20261107T110000Z-abc1234"`) {
			t.Fatalf("加速直链/preview 摘要缺失: %s body=%s", path, body)
		}
	}

	// nil 清空后恢复省略
	srv.SetUpdateInfo(nil)
	code, body := call("/api/me", token)
	if code != 200 || strings.Contains(body, `"update"`) {
		t.Fatalf("清空后应恢复省略: code=%d body=%s", code, body)
	}
}
