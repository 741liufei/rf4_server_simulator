# 管理后台关于页与发布说明页

Status: done

## Problem Statement

管理后台目前只能通过更新横幅跳转到外部链接查看项目说明和版本更新内容。运维人员在本机或离线环境中无法直接查看 README 与发布包内的发布说明，排查版本差异时需要离开管理后台。

## Solution

管理后台新增「关于」和「发布说明」两个菜单。关于页优先读取本地 README.md，缺失时使用编译期内嵌副本；发布说明页读取发布包根目录的 发布说明.md。两页均使用内置 Markdown 渲染，不依赖外部 CDN 或 GitHub API。

## User Stories

1. 作为管理员，我希望在后台直接查看项目 README，以便快速了解项目定位、启动方式和边界。
2. 作为管理员，我希望在后台查看当前发布包自带的发布说明，以便确认版本、修复内容和最近提交。
3. 边界：源码开发环境没有 发布说明.md 时，页面提示从完整发布包启动；README 本地读取失败时回退到编译期内嵌副本。

## Implementation Decisions

- 修改 internal/admin：新增 GET /api/about 与 GET /api/release-notes，均沿用现有 JWT 鉴权。
- 修改 internal/admin/web/index.html：新增「关于」「发布说明」菜单、页面布局和轻量 Markdown 渲染器。
- README.md 通过仓库根包的 go:embed 编译进二进制；运行时优先读取发布包本地 README.md。
- 发布说明由现有 .github/scripts/build-preview.sh / build-release.sh 在打包阶段生成并复制为发布包根目录的 发布说明.md；运行时按工作目录、可执行文件目录及其父目录查找。
- 不修改数据库、协议命令或玩家数据；回滚仅需移除新增页面和接口。

## Testing Decisions

- 单元/API：internal/admin 测试覆盖 README 内嵌回退、本地 README 优先、发布说明本地文件读取、缺失文件 404、未认证请求 401。
- 浏览器：about_ui_test.cjs 与 release_notes_ui_test.cjs 使用 Playwright fixture 验证菜单、Markdown 渲染、链接和页面无 JS 错误。
- 回滚后上述新增测试会变红；全量门禁为 go test ./...、go vet ./...、go build ./...。

## Out of Scope

- 不实现 GitHub Releases API 代理、发布历史列表或在线下载。
- 不修改发布脚本、发布包结构、version.json 或客户端协议。

## Further Notes

- [事实] build-preview.sh 与 build-release.sh 均执行 cp "${RELEASE_NOTES}" "${STAGE}/发布说明.md"。
- [事实] tools/stage_preview.py 允许打包 README.md、LICENSE、docs 等静态资源。
- [推断] 发布包通过「启动服务器.cmd」从包根启动 launcher，因此工作目录查找可命中 发布说明.md；同时保留可执行文件父目录查找以兼容直接启动 bin/launcher.exe。
