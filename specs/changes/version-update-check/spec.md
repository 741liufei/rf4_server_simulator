# 版本更新检测与提示（launcher 控制中心 + 管理后台）

Status: done

<!-- 状态取值：draft | ready | in-progress | done | cancelled
     完成时原地改这里，不要归档、不要搬家、不要另开第二份。 -->

## Problem Statement

用户从 GitHub Releases 下载 zip 后，之后所有新版本（正式 Release 与
preview 自动构建）都不会有任何提示——旧包用户无感知，出问题只能靠
issue 里问"你用的哪个版本"。GitHub Releases 页在国内直连不稳定，
用户更不会主动去刷；launcher 是用户唯一会长期开着的东西，但它目前
对"自己是什么版本、有没有新版"一无所知。

## Solution

发版时 CI 把最新版本信息回写到仓库根的 `version.json`（唯一真源），
并尽力刷新 CDN 缓存。用户的 launcher 启动后延迟数秒做一次检测、之后
每 2 小时复查一次：从多个镜像（jsDelivr → fastly.jsdelivr →
raw.githubusercontent，均为同一仓库文件的只读镜像）拉取 version.json，
与本地构建时间戳按通道（stable / preview）比较，发现新版时在控制中心
和内嵌管理后台显示更新横幅：stable 新版附弹层提示，preview 只出弱化
横幅。横幅提供"加速下载（gh-proxy 直链）/ 官方下载 / 最新预览版 /
更新说明 / 本版本不再提醒"。运行控制的实时日志下方另设常驻版本卡片，
显示当前运行版本、目标通道最新版本，以及 gh-proxy / 官方 Releases
入口；检测成功显示绿色连接灯，无网或全部镜像失败显示红色连接灯并保留
上次成功结果。检测失败只影响这张卡片的提示，绝不影响启动、seed、
服务启停、日志和其他功能。

## User Stories

1. 作为玩家，我打开控制中心时能看到"发现新版本 vX.Y.Z"，点链接去看
   更新说明或下载新版；点了"本版本不再提醒"后这个版本不再骚扰我。
2. 作为追 main 的 preview 用户，我以横幅（不弹窗）方式得知有更新的
   preview 构建，知道自己落后了几个构建。
3. 边界：断网 / 镜像全挂 → 版本卡片显示红色“连接失败”，本地服务
   操作照常；若曾成功检测则继续展示上次最新版本。dev 构建（无 BuildTs
   注入）不做联网检测，卡片同样显示无法检测；远端 JSON 格式不合法或
   schema 不认识 → 当作本次检测失败，换下一个源；检测期间不做任何
   下载或自动安装，永远只展示信息。

## Implementation Decisions

- **可比版本键**：`internal/buildinfo` 新增 `BuildTs`（UTC
  `yyyyMMddHHmmss`，默认 `"0"`）。三处构建统一注入：
  `build.ps1`、`Makefile`、`.github/scripts/build-preview.sh`。
  不比较版本字符串；`"0"` 表示未知（dev 构建），检测直接跳过。
- **真源与镜像**：仓库根 `version.json`（初始
  `{"schema":1,"stable":null,"preview":null}`）。源列表（包内常量，
  顺序即优先级；2026-11-07 维护者国内网络实测后定为 gh-proxy →
  fastly → cdn → raw）：
  1. `https://gh-proxy.com/https://raw.githubusercontent.com/glance522/rf4_server_simulator/main/version.json`
  2. `https://fastly.jsdelivr.net/gh/glance522/rf4_server_simulator@main/version.json`
  3. `https://cdn.jsdelivr.net/gh/glance522/rf4_server_simulator@main/version.json`
  4. `https://raw.githubusercontent.com/glance522/rf4_server_simulator/main/version.json`
  单源 4s 超时；第一个返回合法 JSON（`schema==1` 且可解码）的源胜出；
  其余源不请求。实测：gh-proxy（CF 香港边缘）`s-maxage=60`，内容与
  仓库一致；jsDelivr `@main` 引用
  `Cache-Control: public, max-age=604800, s-maxage=43200`（共享缓存
  12h）；`purge.jsdelivr.net` 清缓存实测可用，CI 回写后 best-effort
  调用（允许失败）。
- **新包 `internal/updatecheck`**：
  - `Manifest{Schema, Stable *Release, Preview *Release}`；
    `Release{Version, BuildTs, ReleasedAt?, NotesURL?, DownloadURL?,
    DownloadMirrorURL?, MinBuildTs?}`——`DownloadMirrorURL` 是 CI 回写时
    按精确 tag/附件名生成的国内加速直链（gh-proxy 包裹 release 附件）。
  - `Check(ctx, currentVersion, currentBuildTs)`：通道由
    `strings.HasPrefix(currentVersion, "preview-")` 判定；preview 只跟
    `Manifest.Preview.BuildTs` 比，stable 只跟 `Manifest.Stable.BuildTs`
    比；`available = 远端 BuildTs > 本地 BuildTs`（数字比较，非法当 0）。
    结果额外携带 `PreviewVersion/PreviewDownloadMirror`（远端 preview
    通道摘要），供 stable 用户横幅跳转"最新预览版"。
  - `forced`（仅 stable）：远端 `MinBuildTs > 本地 BuildTs`，此时
    dismiss 无效。
  - `Dismiss(version)`：写入工作目录 `update_state.json`
    （`{dismissed_version}`）；`Result()` 对已 dismiss 的非 forced 版本
    返回 `available=false`。文件读写失败只记日志不报错。
  - 结果结构体 JSON tag 直接按 `/api/status.update` 的下发展示。
- **launcher 接入**（`cmd/launcher/main.go`）：
  - `/api/status` 始终返回 `current_version` 与 `update_status`
    （`checking/connected/error/checked_at`）；检测仍在后台 goroutine，
    状态只读，不进入启停或日志请求链路。
  - 控制中心实时日志下方新增版本卡片，固定提供
    `https://gh-proxy.com/https://github.com/glance522/rf4_server_simulator/releases`
    与官方 Releases 链接；检测失败时显示红色指示灯和“不影响本地功能”
    的说明。
  - `app` 挂 `updates *updatecheck.Checker`；HTTP 面板起来后
    `go app.runUpdateChecks()`：延迟 5s 首查 → 每 2h 复查，走
    `app.ctx`，退出即停；单次失败记 warn 并保留上一次结果。
  - `/api/status` 响应新增 `update` 字段（无结果时为 `null`）。
  - 新增 `POST /api/update/dismiss`（body `{version}`）。
  - 检测成功后把结果映射进 `admin.Server.SetUpdateInfo`
    （`initAdmin` 完成时也补推一次既有结果）。
- **管理后台**（`internal/admin/api.go`）：新增 `UpdateInfo` 结构与
  `SetUpdateInfo`（`atomic.Pointer` 存储，仿 `sessionCount`；admin 不
  import updatecheck，映射在 launcher 做）。`/api/me` 与
  `/api/dashboard` 响应附带 `update` 字段（nil 则省略）。管理后台的
  更新横幅只在登录后出现，可接受。
- **前端**：
  - `cmd/launcher/panel.html`：`renderStatus` 读取 `st.update`，在
    运行控制视图顶部渲染横幅；stable 新版额外弹一次提示层（本会话
    一次），preview 只横幅。文案一律 `textContent`；链接先校验前缀
    （`https://github.com/` 或 `https://gh-proxy.com/https://github.com/`）
    再赋 `href`。横幅按钮组：**加速下载**（本通道 release 附件
    gh-proxy 直链，主按钮）/ **官方下载**（GitHub releases）/
    **最新预览版**（远端 preview 通道加速直链，仅 stable 用户显示）/
    更新说明 / 本版本不再提醒。
  - `internal/admin/web/index.html`：登录后从 `/api/me` /
    `/api/dashboard` 读 `update`，在顶栏下渲染横幅（按钮组同上）；
    dismiss 调 `fetch('/api/update/dismiss')`（launcher 同源 9500
    端点，绝对路径）。
- **CI 回写**：
  - `preview.yml` 发布成功后：把 `preview` 字段（含按精确附件名生成的
    `download_mirror_url` 加速直链）合并进当前 `version.json`，以 bot
    身份提交回 `main`（commit message 带 `[skip ci]` 防回环），随后
    best-effort purge jsDelivr。
  - 版本回写使用专用 write deploy key（Actions secret
    `VERSION_WRITEBACK_DEPLOY_KEY`），SSH 走 `ssh.github.com:443`；`main`
    的 required-check ruleset 仅允许该 deploy key 作为 bypass actor，
    不让普通 Actions `GITHUB_TOKEN` 直接绕过保护。
  - 新增 `release.yml`（push tag `v*`）：新增
    `.github/scripts/build-release.sh`（与 preview 同款打包，版本号取
    tag、prerelease=false），发布 Release 后同样回写 `stable` 字段。
  - 两个 workflow 调 `update-version-json.sh <channel> <version>
    <build_ts> <asset_name>`，asset_name 来自构建步骤输出。
- **隐私**：检测只是对 CDN 的匿名 GET，不带任何机器标识。
- 数据与迁移：无表结构改动；`update_state.json` 加进 `.gitignore`。
- 兼容与回滚：不改任何协议命令与现有 API 字段（只新增字段/端点）；
  回滚 = revert 本分支提交，旧包无新字段照常工作。

## Testing Decisions

- 单元（纯逻辑 + httptest 假源）`internal/updatecheck/updatecheck_test.go`：
  - 比较矩阵（手写期望值，非同码再算）：preview 本地 20261107000000 vs
    远端 {更大/相等/更小}；stable 同理；`MinBuildTs` 触发 forced；
    本地 BuildTs="0"/非法 → 不检测或当 0；远端 BuildTs 非法 → 当 0。
  - 多源：源 1 返回 500/非法 JSON/schema=99 → 落到源 2；源 1 合法 →
    源 2 不被请求（计数断言）。
  - dismiss：dismiss 后 `available=false`；forced 时 dismiss 无效。
  - 回滚变红点：删除 updatecheck 实现则本文件全红。
- `internal/admin/api_test.go`：`SetUpdateInfo` 后 `/api/me` 与
  `/api/dashboard` 带 `update` 字段（沿用既有认证/请求测试接缝）。
- `cmd/launcher/main_test.go`：`handleStatus` 携带 `update`、
  `current_version`、`update_status` 字段；dismiss 端点写入状态文件；
  假源返回 503 时检测状态标红但 `/api/status` 仍 200（CI 不跑
  cmd/launcher，本地跑）。
- UI（惯例接缝）`internal/admin/update_banner_ui_test.cjs`：mock
  `/api/me` 返回带 `update` → 断言横幅出现；mock `available=false` →
  断言不出现。沿用现有 `.cjs` 的 route/前缀约定。
- 门禁：`go build ./... && go vet ./... && go test ./...`（Windows 本地
  全跑；CI 的 test job 不含 cmd/launcher，与本仓现状一致）。

## Out of Scope

- 自动下载、校验与安装新包（只提示，不自更新）。
- 游戏客户端（RF4 本体）内提示、托盘气泡通知。
- "手动检查更新"按钮、检查结果详情页。
- Gitee 镜像源（镜像仓库尚不存在；将来加进源列表即可，不改结构）。
- version.json 历史版本档案（只维护"当前最新"）。

## Further Notes

- `[事实]` 版本注入现状：`build.ps1` 只注入 `buildinfo.Version`
  （本地=时间戳；preview=`preview-<UTC>-<sha>`，见
  `.github/scripts/build-preview.sh`）；`BuildTime/GitCommit` 未被注入。
- `[事实]` CI test job 明确排除 `./cmd/launcher`
  （`.github/workflows/ci.yml` 注释：托盘依赖 cgo/gtk）。
- `[事实]` 管理后台页面在面板中以 iframe 挂在 `/admin/`，与
  `/api/status` 同源（9500），且管理后台 UI 测试对 `/admin` 前缀下的
  每个请求断言必须以 `/admin/api/` 开头——因此 admin 页面不直接
  `fetch('/api/status')`，改为服务端把 update 信息并入 `/api/me` /
  `/api/dashboard`。
- `[事实]` 版本注入现状：`build.ps1` 只注入 `buildinfo.Version`
  （本地=时间戳；preview=`preview-<UTC>-<sha>`，见
  `.github/scripts/build-preview.sh`）；`BuildTime/GitCommit` 未被注入。
- `[事实]` 实现期 UI 测试逮住并修复的真实缺陷：横幅样式
  `.update-banner{display:flex}` 会覆盖 `hidden` 属性的 UA 样式
  （`[hidden]{display:none}`），导致横幅"隐藏"后仍然可见——两个页面
  均补 `.update-banner[hidden]{display:none}`。
- `[事实]` `admin.Server.Start()` 的 errCh 只在异常退出时发值，正常
  Shutdown 后既不发也不关；测试里 `<-errCh` 会把整个测试包挂到超时
  （TestUpdateInfoInjection 首版踩中，已按 TestAdminAPI 的约定去掉等待）。
- `[事实]` 本机 UI 测试中 `happy_hour_ui_test.cjs` 在未改动 main 上
  同样失败，属预存环境问题，与本改动无关；fishing_rules 复跑通过
  （首跑为偶发）。
- `[事实]` 2026-11-07 维护者国内网络实测（curl 直连，与 Go 客户端同
  条件）：`gh-proxy.com` 前缀代理 raw 路径 200 且内容与仓库逐字节
  一致（s-maxage=60）；`fastly.jsdelivr.net` 200 且内容一致
  （s-maxage=43200）；`cdn.jsdelivr.net` 与 `raw.githubusercontent.com`
  均在 TLS 握手阶段被连接重置（curl exit 35）；`purge.jsdelivr.net`
  清缓存返回 pending 成功。gh-proxy 为社区代理，长期可用性未知，
  故只作首位（失败自动 fallthrough），不删 jsDelivr 链路。单点网络
  样本，不同 ISP 可能有差异——这正是多源 fallback 的意义。
- `[假设]` 用户群主要在国内，GitHub 域名直连不可靠——依据是文档全
  中文、CN 区启动参数为默认；未做抽样验证。
