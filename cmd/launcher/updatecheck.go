// 版本更新检测：launcher 后台周期拉取 version.json 并把结论喂给
// 控制中心 /api/status 与管理后台横幅。任何失败只记日志，绝不影响
// 启动、seed 与游戏服务。
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"rf4server/internal/admin"
	"rf4server/internal/buildinfo"
	"rf4server/internal/updatecheck"
)

// updateStateFile "本版本不再提醒"的本地状态文件（工作目录根）。
const updateStateFile = "update_state.json"

// updateCheckInterval 周期复查间隔；检测只是对 CDN 的匿名 GET，2h 一次
// 对镜像与用户带宽都毫无压力。
const updateCheckInterval = 2 * time.Hour

// runUpdateChecks 后台检测循环：延迟 5s 首查（避开启动高峰），之后每
// 2h 复查，直到 launcher 退出。dev 构建（无 BuildTs 注入）不检测。
func (a *app) runUpdateChecks() {
	if buildinfo.BuildTs == "0" {
		return
	}
	select {
	case <-a.ctx.Done():
		return
	case <-time.After(5 * time.Second):
	}
	a.checkUpdates()
	ticker := time.NewTicker(updateCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			a.checkUpdates()
		}
	}
}

func (a *app) checkUpdates() {
	res, err := a.updates.Check(a.ctx, buildinfo.Version, buildinfo.BuildTs)
	if err != nil {
		// 静默降级：保留上一次结论（如有），日志留痕便于排查。
		a.log.Warn("检查更新失败（将按周期重试）: " + err.Error())
		return
	}
	if res.Available {
		a.log.Info("发现新版本 " + res.LatestVersion + "（当前 " + res.CurrentVersion + "），详见控制中心横幅")
	}
	a.pushUpdateInfo(res)
}

// pushUpdateInfo 把最新结论同步给管理后台（admin 不做网络请求）。
func (a *app) pushUpdateInfo(res *updatecheck.Result) {
	a.adminMu.RLock()
	srv := a.adminSrv
	a.adminMu.RUnlock()
	if srv != nil {
		srv.SetUpdateInfo(adminUpdateInfo(res))
	}
}

// adminUpdateInfo 把 updatecheck.Result 映射为 admin.UpdateInfo；
// admin 包不反向依赖 updatecheck，映射放在消费方。
func adminUpdateInfo(res *updatecheck.Result) *admin.UpdateInfo {
	if res == nil {
		return nil
	}
	return &admin.UpdateInfo{
		Available:             res.Available,
		Channel:               res.Channel,
		CurrentVersion:        res.CurrentVersion,
		LatestVersion:         res.LatestVersion,
		NotesURL:              res.NotesURL,
		DownloadURL:           res.DownloadURL,
		DownloadMirrorURL:     res.DownloadMirrorURL,
		PreviewVersion:        res.PreviewVersion,
		PreviewDownloadMirror: res.PreviewDownloadMirror,
		Forced:                res.Forced,
	}
}

// handleUpdateDismiss POST /api/update/dismiss（body {version}）：
// 记住"本版本不再提醒"，写入 update_state.json，重启后仍生效。
func (a *app) handleUpdateDismiss(w http.ResponseWriter, r *http.Request) {
	if a.updates == nil {
		httpError(w, "更新检测未初始化")
		return
	}
	var body struct {
		Version string `json:"version"`
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	if err != nil || json.Unmarshal(raw, &body) != nil || strings.TrimSpace(body.Version) == "" {
		httpError(w, "version 不能为空")
		return
	}
	if err := a.updates.Dismiss(strings.TrimSpace(body.Version)); err != nil {
		httpError(w, "保存不再提醒状态失败: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
