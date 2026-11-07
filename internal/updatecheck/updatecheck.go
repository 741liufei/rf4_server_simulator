// Package updatecheck —— launcher 的版本更新检测。
//
// 从多个只读镜像按序拉取仓库根 version.json（schema=1，双通道
// stable/preview），与本地构建时间戳（buildinfo.BuildTs，UTC
// yyyyMMddHHmmss）按通道做数字比较，产出 /api/status.update 与管理
// 后台横幅所需的结果。任何失败（无网、镜像全挂、格式不认识）都以
// error 返回，由调用方决定只记日志——检测绝不影响启动与游戏。
package updatecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 通道名（Result.Channel 取值）。
const (
	ChannelStable  = "stable"
	ChannelPreview = "preview"
)

// DefaultSources 按优先级排列的 version.json 只读镜像（同一仓库文件）。
// 2026-11-07 国内实测（curl 直连，与 Go 客户端同条件）：
//   - gh-proxy.com：200，内容与仓库一致，s-maxage=60（最鲜），社区代理
//     长期稳定性未知——放首位吃新鲜度，失败自动落下一个源；
//   - fastly.jsdelivr.net：200，s-maxage=43200（12h），正规 CDN 作次选；
//   - cdn.jsdelivr.net 与 raw.githubusercontent.com：TLS 握手被连接重置，
//     仅作其他网络环境的备选。
var DefaultSources = []string{
	"https://gh-proxy.com/https://raw.githubusercontent.com/glance522/rf4_server_simulator/main/version.json",
	"https://fastly.jsdelivr.net/gh/glance522/rf4_server_simulator@main/version.json",
	"https://cdn.jsdelivr.net/gh/glance522/rf4_server_simulator@main/version.json",
	"https://raw.githubusercontent.com/glance522/rf4_server_simulator/main/version.json",
}

// ErrUnknownLocalBuildTs 表示本地构建没有可比键（dev 构建），不应检测。
var ErrUnknownLocalBuildTs = errors.New("本地构建缺少 BuildTs，跳过更新检测")

// maxManifestBytes 防御性限制远端响应体积。
const maxManifestBytes = 1 << 20

// sourceTimeout 单个镜像的请求超时；三个源全超时最多拖 12s，
// 发生在后台 goroutine，不影响启动。
const sourceTimeout = 4 * time.Second

// Release 是 version.json 中单通道的最新版本条目。
type Release struct {
	Version     string `json:"version"`
	BuildTs     string `json:"build_ts"`
	ReleasedAt  string `json:"released_at,omitempty"`
	NotesURL    string `json:"notes_url,omitempty"`
	DownloadURL string `json:"download_url,omitempty"`
	// DownloadMirrorURL 国内加速直链（gh-proxy 包裹的本仓库 release 附件），
	// 由 CI 回写时按精确 tag/附件名生成；官方下载链接始终并存。
	DownloadMirrorURL string `json:"download_mirror_url,omitempty"`
	MinBuildTs        string `json:"min_build_ts,omitempty"`
}

// Manifest 是仓库根 version.json 的形状。
type Manifest struct {
	Schema  int      `json:"schema"`
	Stable  *Release `json:"stable"`
	Preview *Release `json:"preview"`
}

// Result 是一次检测的结论，JSON 形状即 /api/status.update 下发内容。
type Result struct {
	Available      bool   `json:"available"`
	Channel        string `json:"channel"` // stable | preview
	CurrentVersion string `json:"current_version"`
	LatestVersion  string `json:"latest_version"`
	NotesURL       string `json:"notes_url,omitempty"`
	DownloadURL    string `json:"download_url,omitempty"`
	// DownloadMirrorURL 国内加速直链，随 DownloadURL 一并透传给前端。
	DownloadMirrorURL string `json:"download_mirror_url,omitempty"`
	// PreviewVersion / PreviewDownloadMirror 摘自远端 preview 通道，
	// 供 stable 用户的横幅跳转"最新预览版"；preview 用户自身即是
	// preview，前端应隐藏该入口。
	PreviewVersion        string    `json:"preview_version,omitempty"`
	PreviewDownloadMirror string    `json:"preview_download_mirror,omitempty"`
	Forced                bool      `json:"forced,omitempty"`
	CheckedAt             time.Time `json:"checked_at"`
}

// state 是 update_state.json 的形状（用户选择"本版本不再提醒"的版本）。
type state struct {
	DismissedVersion string `json:"dismissed_version"`
}

// Checker 周期性拉取 version.json 并维护最近一次检测结论。
type Checker struct {
	sources   []string
	hc        *http.Client
	statePath string
	now       func() time.Time

	mu        sync.Mutex
	last      *Result
	dismissed string
}

// New 构建检测器并加载本地"不再提醒"状态；状态文件读不到按无状态处理。
func New(sources []string, statePath string) *Checker {
	c := &Checker{
		sources:   sources,
		hc:        &http.Client{Timeout: sourceTimeout},
		statePath: statePath,
		now:       time.Now,
	}
	if raw, err := os.ReadFile(statePath); err == nil {
		var st state
		if json.Unmarshal(raw, &st) == nil {
			c.dismissed = st.DismissedVersion
		}
	}
	return c
}

// Result 返回最近一次检测结论的副本；尚未成功检测过时为 nil。
func (c *Checker) Result() *Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last == nil {
		return nil
	}
	res := *c.last
	return &res
}

// Dismiss 记住"本版本不再提醒"；forced 版本不受影响（Check 时判定）。
// 状态写入 update_state.json，重启后仍然生效。
func (c *Checker) Dismiss(version string) error {
	version = strings.TrimSpace(version)
	if version == "" {
		return errors.New("version 不能为空")
	}
	if err := c.saveState(version); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dismissed = version
	if c.last != nil && c.last.LatestVersion == version && !c.last.Forced {
		res := *c.last
		res.Available = false
		c.last = &res
	}
	return nil
}

func (c *Checker) saveState(version string) error {
	raw, err := json.Marshal(state{DismissedVersion: version})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.statePath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(c.statePath, raw, 0o644)
}

// Check 拉取 version.json 并与本地版本比较；全部源失败时返回 error。
// 检测结论会记住（Result 可随时读取），并发安全。
func (c *Checker) Check(ctx context.Context, currentVersion, currentBuildTs string) (*Result, error) {
	local := parseBuildTs(currentBuildTs)
	if local == 0 {
		return nil, ErrUnknownLocalBuildTs
	}
	channel := ChannelStable
	if strings.HasPrefix(currentVersion, "preview-") {
		channel = ChannelPreview
	}
	manifest, err := c.fetchManifest(ctx)
	if err != nil {
		return nil, err
	}
	res := Result{
		Channel:        channel,
		CurrentVersion: currentVersion,
		CheckedAt:      c.now(),
	}
	var rel *Release
	if channel == ChannelPreview {
		rel = manifest.Preview
	} else {
		rel = manifest.Stable
	}
	if rel != nil {
		// 即使当前已是最新，也把目标通道的最新版本与链接带回界面；
		// Available 只表示“是否比本地更新”，不能再用它决定能否展示。
		res.LatestVersion = rel.Version
		res.NotesURL = rel.NotesURL
		res.DownloadURL = rel.DownloadURL
		res.DownloadMirrorURL = rel.DownloadMirrorURL
		if parseBuildTs(rel.BuildTs) > local {
			res.Available = true
			if channel == ChannelStable && parseBuildTs(rel.MinBuildTs) > local {
				res.Forced = true
			}
		}
	}
	if manifest.Preview != nil {
		res.PreviewVersion = manifest.Preview.Version
		res.PreviewDownloadMirror = manifest.Preview.DownloadMirrorURL
	}
	c.mu.Lock()
	if res.Available && !res.Forced && c.dismissed != "" && c.dismissed == res.LatestVersion {
		res.Available = false
	}
	c.last = &res
	c.mu.Unlock()
	return &res, nil
}

// fetchManifest 按序尝试各镜像，第一个返回合法 manifest 的源胜出。
func (c *Checker) fetchManifest(ctx context.Context) (*Manifest, error) {
	var errs []error
	for _, src := range c.sources {
		manifest, err := c.fetchOne(ctx, src)
		if err == nil {
			return manifest, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", src, err))
	}
	return nil, errors.Join(errs...)
}

func (c *Checker) fetchOne(ctx context.Context, src string) (*Manifest, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestBytes))
	if err != nil {
		return nil, err
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("JSON 解析失败: %w", err)
	}
	if manifest.Schema != 1 {
		return nil, fmt.Errorf("不支持的 schema=%d", manifest.Schema)
	}
	return &manifest, nil
}

// parseBuildTs 解析 yyyyMMddHHmmss 数字时间戳；空/非法一律 0（不可比）。
func parseBuildTs(s string) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return v
}
