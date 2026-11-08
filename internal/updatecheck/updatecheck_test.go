package updatecheck

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// manifestBody 构造 version.json 响应体（手写字面量，期望值不来自被测代码）。
func manifestBody(schema int, stable, preview string) string {
	b := `{"schema":` + itoa(schema)
	if stable == "" {
		b += `,"stable":null`
	} else {
		b += `,"stable":` + stable
	}
	if preview == "" {
		b += `,"preview":null`
	} else {
		b += `,"preview":` + preview
	}
	return b + `}`
}

func itoa(n int) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}

// releaseBody 手写一条 Release JSON。
func releaseBody(version, buildTs string, extra ...string) string {
	b := `{"version":"` + version + `","build_ts":"` + buildTs + `"`
	for i := 0; i+1 < len(extra); i += 2 {
		b += `,"` + extra[i] + `":"` + extra[i+1] + `"`
	}
	return b + `}`
}

// testEnv 建一组假源 + 指向临时目录的 Checker。
type testEnv struct {
	checker *Checker
	hits    map[string]*atomic.Int64
	close   func()
}

func newTestEnv(t *testing.T, bodies ...string) *testEnv {
	t.Helper()
	env := &testEnv{hits: map[string]*atomic.Int64{}}
	urls := make([]string, 0, len(bodies))
	for _, body := range bodies {
		hits := &atomic.Int64{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}))
		env.hits[srv.URL] = hits
		urls = append(urls, srv.URL)
		t.Cleanup(srv.Close)
	}
	env.checker = New(urls, filepath.Join(t.TempDir(), "update_state.json"))
	env.checker.now = func() time.Time { return time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC) }
	return env
}

func TestCheckPreviewNewerAvailable(t *testing.T) {
	env := newTestEnv(t, manifestBody(1, releaseBody("v1.0.0", "20261101000000"), releaseBody("preview-20261107T110000Z-abc1234", "20261107110000")))
	res, err := env.checker.Check(context.Background(), "preview-20261106T100000Z-0000000", "20261106100000")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !res.Available || res.Channel != ChannelPreview {
		t.Fatalf("期望 preview 有更新，got %+v", res)
	}
	if res.LatestVersion != "preview-20261107T110000Z-abc1234" || res.CurrentVersion != "preview-20261106T100000Z-0000000" {
		t.Fatalf("版本字段不符: %+v", res)
	}
	if res.Forced {
		t.Fatal("preview 通道不应有 forced")
	}
}

func TestCheckPreviewNotNewer(t *testing.T) {
	for name, remote := range map[string]string{
		"相等": releaseBody("preview-20261106T100000Z-0000000", "20261106100000"),
		"更旧": releaseBody("preview-20261105T100000Z-0000000", "20261105100000"),
	} {
		t.Run(name, func(t *testing.T) {
			env := newTestEnv(t, manifestBody(1, releaseBody("v1.0.0", "20261101000000"), remote))
			res, err := env.checker.Check(context.Background(), "preview-20261106T100000Z-0000000", "20261106100000")
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if res.Available {
				t.Fatalf("不应提示更新: %+v", res)
			}
		})
	}
}

func TestCheckReportsLatestWhenAlreadyCurrent(t *testing.T) {
	env := newTestEnv(t, manifestBody(1,
		releaseBody("v1.0.0", "20261106120000",
			"notes_url", "https://github.com/glance522/rf4_server_simulator/releases/tag/v1.0.0",
			"download_url", "https://github.com/glance522/rf4_server_simulator/releases/latest",
			"download_mirror_url", "https://gh-proxy.com/https://github.com/glance522/rf4_server_simulator/releases/download/v1.0.0/a.zip"), ""))
	res, err := env.checker.Check(context.Background(), "v1.0.0", "20261107120000")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if res.Available {
		t.Fatalf("本地更新时不应提示更新: %+v", res)
	}
	if res.LatestVersion != "v1.0.0" || res.NotesURL == "" || res.DownloadURL == "" || res.DownloadMirrorURL == "" {
		t.Fatalf("检测成功时应始终带回目标通道最新版本与链接: %+v", res)
	}
}

func TestCheckStableNewerAvailable(t *testing.T) {
	env := newTestEnv(t, manifestBody(1,
		releaseBody("v1.2.0", "20261107120000",
			"notes_url", "https://github.com/glance522/rf4_server_simulator/releases/tag/v1.2.0",
			"download_url", "https://github.com/glance522/rf4_server_simulator/releases/latest",
			"download_mirror_url", "https://gh-proxy.com/https://github.com/glance522/rf4_server_simulator/releases/download/v1.2.0/rf4_server_simulator-v1.2.0-windows-amd64.zip"),
		releaseBody("preview-20261107T110000Z-abc1234", "20261107110000",
			"download_mirror_url", "https://gh-proxy.com/https://github.com/glance522/rf4_server_simulator/releases/download/preview-20261107T110000Z-abc1234/rf4_server_simulator-preview-20261107T110000Z-abc1234-windows-amd64.zip")))
	res, err := env.checker.Check(context.Background(), "v1.1.0", "20261101120000")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !res.Available || res.Channel != ChannelStable || res.LatestVersion != "v1.2.0" {
		t.Fatalf("期望 stable 有更新: %+v", res)
	}
	if res.NotesURL == "" || res.DownloadURL == "" {
		t.Fatalf("下载/说明链接未透传: %+v", res)
	}
	if res.DownloadMirrorURL == "" {
		t.Fatalf("加速直链未透传: %+v", res)
	}
	if res.PreviewVersion != "preview-20261107T110000Z-abc1234" || res.PreviewDownloadMirror == "" {
		t.Fatalf("preview 通道摘要未透传: %+v", res)
	}
	if res.Forced {
		t.Fatal("未设 min_build_ts 不应 forced")
	}
}

func TestCheckStableForcedIgnoresDismiss(t *testing.T) {
	env := newTestEnv(t, manifestBody(1, releaseBody("v2.0.0", "20261201120000", "min_build_ts", "20261201120000"), ""))
	local := "20261101120000"
	if _, err := env.checker.Check(context.Background(), "v1.0.0", local); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if err := env.checker.Dismiss("v2.0.0"); err != nil {
		t.Fatalf("Dismiss: %v", err)
	}
	res, err := env.checker.Check(context.Background(), "v1.0.0", local)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !res.Available || !res.Forced {
		t.Fatalf("forced 版本 dismiss 后仍应提示: %+v", res)
	}
}

func TestDismissHidesNonForcedAndPersists(t *testing.T) {
	env := newTestEnv(t, manifestBody(1, releaseBody("v1.2.0", "20261107120000"), ""))
	ctx := context.Background()
	if _, err := env.checker.Check(ctx, "v1.1.0", "20261101120000"); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if err := env.checker.Dismiss("v1.2.0"); err != nil {
		t.Fatalf("Dismiss: %v", err)
	}
	if res := env.checker.Result(); res.Available {
		t.Fatalf("dismiss 后 Result 应不可用: %+v", res)
	}
	// 重启（重建 Checker，同一状态文件）后仍然静默。
	reborn := New(env.checker.sources, env.checker.statePath)
	res, err := reborn.Check(ctx, "v1.1.0", "20261101120000")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if res.Available {
		t.Fatalf("状态文件应跨重启生效: %+v", res)
	}
	// 其他版本不受影响。
	if err := reborn.Dismiss(""); err == nil {
		t.Fatal("空 version 应报错")
	}
}

func TestSourceFallback(t *testing.T) {
	bad := []string{
		manifestBody(1, releaseBody("v0.0.1", "20200101000000"), ""), // 会被替换成各种坏源
		manifestBody(1, releaseBody("v1.2.0", "20261107120000"), ""),
		manifestBody(1, releaseBody("v9.9.9", "20991231235959"), ""),
	}
	// 源 1: HTTP 500；源 2: 非法 JSON；源 3: schema 不支持；源 4: 正常。
	urls := make([]string, 0, 4)
	hits := map[string]*atomic.Int64{}
	mk := func(status int, body string) string {
		h := &atomic.Int64{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			h.Add(1)
			if status != http.StatusOK {
				w.WriteHeader(status)
				return
			}
			_, _ = w.Write([]byte(body))
		}))
		url := srv.URL
		hits[url] = h
		t.Cleanup(srv.Close)
		urls = append(urls, url)
		return url
	}
	mk(500, "server error")
	mk(200, "{not json")
	mk(200, manifestBody(99, "", ""))
	mk(200, bad[1])
	c := New(urls, filepath.Join(t.TempDir(), "update_state.json"))
	res, err := c.Check(context.Background(), "v1.1.0", "20261101120000")
	if err != nil {
		t.Fatalf("前三个源坏、第四个应胜出: %v", err)
	}
	if res.LatestVersion != "v1.2.0" {
		t.Fatalf("应取第四个源: %+v", res)
	}
	for i, url := range urls[:3] {
		if hits[url].Load() != 1 {
			t.Fatalf("源 %d 应被请求 1 次，got %d", i+1, hits[url].Load())
		}
	}
}

func TestFirstSourceWins(t *testing.T) {
	first := manifestBody(1, releaseBody("v1.0.0", "20261107120000"), "")
	second := manifestBody(1, releaseBody("v2.0.0", "20261207120000"), "")
	env := newTestEnv(t, first, second)
	urls := env.checker.sources
	res, err := env.checker.Check(context.Background(), "v0.9.0", "20261101120000")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if res.LatestVersion != "v1.0.0" {
		t.Fatalf("应取第一个合法源: %+v", res)
	}
	want := map[string]int64{}
	for url := range env.hits {
		want[url] = 1
	}
	want[urls[1]] = 0 // 首个合法源胜出后，后续源不应被请求
	for url, h := range env.hits {
		if h.Load() != want[url] {
			t.Fatalf("源 %s 期望请求 %d 次，got %d", url, want[url], h.Load())
		}
	}
}

func TestUnknownLocalBuildTsRejected(t *testing.T) {
	env := newTestEnv(t, manifestBody(1, releaseBody("v1.0.0", "20261107120000"), ""))
	if _, err := env.checker.Check(context.Background(), "dev", "0"); err != ErrUnknownLocalBuildTs {
		t.Fatalf("期望 ErrUnknownLocalBuildTs，got %v", err)
	}
}

func TestRemoteBuildTsInvalidMeansNoUpdate(t *testing.T) {
	env := newTestEnv(t, manifestBody(1, releaseBody("v1.2.0", "not-a-number"), ""))
	res, err := env.checker.Check(context.Background(), "v1.1.0", "20261101120000")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if res.Available {
		t.Fatalf("远端 BuildTs 非法应视为不可比（0）: %+v", res)
	}
}

func TestRemoteChannelMissingMeansNoUpdate(t *testing.T) {
	env := newTestEnv(t, manifestBody(1, "", "")) // 双通道均为 null
	res, err := env.checker.Check(context.Background(), "v1.1.0", "20261101120000")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if res.Available || res.LatestVersion != "" {
		t.Fatalf("通道缺失不应提示: %+v", res)
	}
}

func TestAllSourcesDown(t *testing.T) {
	c := New([]string{"http://127.0.0.1:1/none", "http://127.0.0.1:2/none"}, filepath.Join(t.TempDir(), "update_state.json"))
	if _, err := c.Check(context.Background(), "v1.1.0", "20261101120000"); err == nil {
		t.Fatal("全部源失败应返回 error")
	}
	if res := c.Result(); res != nil {
		t.Fatalf("失败时不应有结论: %+v", res)
	}
}

func TestStateFileRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update_state.json")
	c := New(nil, path)
	if err := c.Dismiss("v1.2.0"); err != nil {
		t.Fatalf("Dismiss: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("状态文件应写出: %v", err)
	}
	var st state
	if err := json.Unmarshal(raw, &st); err != nil || st.DismissedVersion != "v1.2.0" {
		t.Fatalf("状态文件内容不符: %s (%v)", raw, err)
	}
}
