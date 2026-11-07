package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rf4server/internal/buildinfo"
	"rf4server/internal/config"
	"rf4server/internal/updatecheck"
)

func TestHandleStatusIncludesUpdate(t *testing.T) {
	manifest := `{"schema":1,"stable":{"version":"v1.2.0","build_ts":"20261107120000","notes_url":"https://github.com/glance522/rf4_server_simulator/releases/tag/v1.2.0","download_url":"https://github.com/glance522/rf4_server_simulator/releases/latest","download_mirror_url":"https://gh-proxy.com/https://github.com/glance522/rf4_server_simulator/releases/download/v1.2.0/a.zip"},"preview":{"version":"preview-20261107T110000Z-abc1234","build_ts":"20261107110000","download_mirror_url":"https://gh-proxy.com/https://github.com/glance522/rf4_server_simulator/releases/download/preview-20261107T110000Z-abc1234/b.zip"}}`
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(manifest))
	}))
	defer src.Close()

	a := &app{
		cfg:     &config.Config{},
		updates: updatecheck.New([]string{src.URL}, filepath.Join(t.TempDir(), "update_state.json")),
	}
	if _, err := a.updates.Check(context.Background(), "v1.1.0", "20261101120000"); err != nil {
		t.Fatalf("Check: %v", err)
	}
	rr := httptest.NewRecorder()
	a.handleStatus(rr, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%q", rr.Code, rr.Body.String())
	}
	var st struct {
		CurrentVersion string              `json:"current_version"`
		Update         *updatecheck.Result `json:"update"`
		UpdateStatus   updateHealth        `json:"update_status"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &st); err != nil {
		t.Fatalf("解析 /api/status: %v", err)
	}
	if st.Update == nil || !st.Update.Available || st.Update.LatestVersion != "v1.2.0" {
		t.Fatalf("update 字段不符: %+v", st.Update)
	}
	if st.Update.DownloadMirrorURL == "" || st.Update.PreviewVersion != "preview-20261107T110000Z-abc1234" || st.Update.PreviewDownloadMirror == "" {
		t.Fatalf("加速直链/preview 摘要未透传: %+v", st.Update)
	}
	if st.CurrentVersion == "" {
		t.Fatal("status 应始终携带当前运行版本")
	}
}

func TestCheckUpdatesTracksConnectionFailureWithoutBlockingStatus(t *testing.T) {
	oldBuildTs := buildinfo.BuildTs
	buildinfo.BuildTs = "20261101120000"
	t.Cleanup(func() { buildinfo.BuildTs = oldBuildTs })
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer src.Close()
	a := &app{
		cfg:     &config.Config{},
		ctx:     context.Background(),
		log:     testLauncherLogger{},
		updates: updatecheck.New([]string{src.URL}, filepath.Join(t.TempDir(), "update_state.json")),
	}
	a.checkUpdates()
	health := a.updateHealthSnapshot()
	if health.Checking || health.Connected || !strings.Contains(health.Error, "HTTP 503") || health.CheckedAt == "" {
		t.Fatalf("检测失败状态不符: %+v", health)
	}

	rr := httptest.NewRecorder()
	a.handleStatus(rr, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("更新服务器失败不能影响 status: code=%d body=%q", rr.Code, rr.Body.String())
	}
	var st struct {
		Procs        []any        `json:"procs"`
		UpdateStatus updateHealth `json:"update_status"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &st); err != nil {
		t.Fatalf("解析 /api/status: %v", err)
	}
	if st.UpdateStatus.Connected || st.UpdateStatus.Error == "" {
		t.Fatalf("status 应透传连接失败状态: %+v", st.UpdateStatus)
	}
}

func TestCheckUpdatesMarksConnected(t *testing.T) {
	oldBuildTs := buildinfo.BuildTs
	buildinfo.BuildTs = "20261101120000"
	t.Cleanup(func() { buildinfo.BuildTs = oldBuildTs })
	manifest := `{"schema":1,"stable":{"version":"v1.2.0","build_ts":"20261107120000"},"preview":null}`
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(manifest))
	}))
	defer src.Close()
	a := &app{
		ctx:     context.Background(),
		log:     testLauncherLogger{},
		updates: updatecheck.New([]string{src.URL}, filepath.Join(t.TempDir(), "update_state.json")),
	}
	a.checkUpdates()
	health := a.updateHealthSnapshot()
	if health.Checking || !health.Connected || health.Error != "" || health.CheckedAt == "" {
		t.Fatalf("检测成功状态不符: %+v", health)
	}
}

func TestHandleStatusWithoutUpdateChecker(t *testing.T) {
	a := &app{cfg: &config.Config{}}
	rr := httptest.NewRecorder()
	a.handleStatus(rr, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"update":null`) {
		t.Fatalf("updates 为 nil 时应下发 null: %s", rr.Body.String())
	}
}

func TestHandleUpdateDismiss(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "update_state.json")
	a := &app{updates: updatecheck.New(nil, statePath)}

	rr := httptest.NewRecorder()
	a.handleUpdateDismiss(rr, httptest.NewRequest(http.MethodPost, "/api/update/dismiss", strings.NewReader(`{"version":"v1.2.0"}`)))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("dismiss 应 204, 实得 %d body=%q", rr.Code, rr.Body.String())
	}
	raw, err := os.ReadFile(statePath)
	if err != nil || !strings.Contains(string(raw), "v1.2.0") {
		t.Fatalf("状态文件应记录 dismissed 版本: %s (%v)", raw, err)
	}

	// 空 version → 400/500 错误响应
	rr = httptest.NewRecorder()
	a.handleUpdateDismiss(rr, httptest.NewRequest(http.MethodPost, "/api/update/dismiss", strings.NewReader(`{}`)))
	if rr.Code == http.StatusNoContent {
		t.Fatal("空 version 不应成功")
	}

	// updates 未初始化 → 错误响应
	rr = httptest.NewRecorder()
	(&app{}).handleUpdateDismiss(rr, httptest.NewRequest(http.MethodPost, "/api/update/dismiss", strings.NewReader(`{"version":"v1"}`)))
	if rr.Code == http.StatusNoContent {
		t.Fatal("未初始化 checker 不应成功")
	}
}

func TestAdminUpdateInfoMapping(t *testing.T) {
	if adminUpdateInfo(nil) != nil {
		t.Fatal("nil 结果应映射为 nil（响应省略 update 键）")
	}
	m := adminUpdateInfo(&updatecheck.Result{
		Available: true, Channel: updatecheck.ChannelStable,
		CurrentVersion: "v1.0.0", LatestVersion: "v1.2.0", Forced: true,
	})
	if m == nil || !m.Available || m.Channel != "stable" || m.LatestVersion != "v1.2.0" || !m.Forced {
		t.Fatalf("映射字段不符: %+v", m)
	}
}

type testLauncherLogger struct{}

func (testLauncherLogger) Info(string)  {}
func (testLauncherLogger) Warn(string)  {}
func (testLauncherLogger) Error(string) {}
