# 管理后台玩家在线状态灯

Status: done

## Problem Statement

管理后台「玩家管理」列表只显示账号状态（正常 / 封禁），看不到玩家此刻是否在线。运维想确认某个玩家是否还连着服务器时，只能去总览看一个在线会话总数，无法定位到具体玩家。

## Solution

玩家列表每行右侧新增状态灯：绿色表示在线，红色表示不在线，并与账号状态标签并排。搜索框右侧给出颜色图例。玩家管理视图每 8 秒静默刷新一次列表，停服、断线、登录后灯会自动跟上。

## User Stories

1. 作为管理员，我希望在玩家列表直接看出谁在线，以便判断是否要等玩家下线再改数据或发邮件。
2. 边界：同一玩家重连只算一个会话，灯仍为绿色。
3. 边界：游戏服尚未启动或尚未发布在线名单时，所有玩家显示红色，页面不报错。

## Implementation Decisions

- internal/gameserver：新增 `Server.OnlineUIDs()`，加锁遍历会话，按 `Session.Userid` 去重升序，跳过未登录会话（`Userid<=0`）。
- cmd/gameserver：沿用现有 5 秒 KV 发布循环，把 `updated_at + uids[]` 的 JSON 快照写入 `stats:online_uids`（与 `stats:sessions` 并列）。管理端只接受 30 秒内的快照，进程被强杀或异常退出后旧名单会自动失效。
- internal/admin：`GET /api/players` 读取该键，为每个玩家增加只读字段 `online`（bool）。键缺失、空串、JSON 损坏、时间戳无效或快照超过 30 秒时按全部离线处理，不返回 5xx。
- internal/admin/web/index.html：行右列增加 `.online-dot` 状态灯和工具栏图例；沿用总览的 8 秒轮询，在 players 视图调用 `loadPlayers(true)` 静默刷新（不改变已选玩家与搜索词）。
- 不改数据库结构、协议命令、鉴权与客户端；旧前端会忽略新增字段，回滚只需移除字段与前端标记。

## Testing Decisions

- 单元/API：internal/admin 用例覆盖键缺失、新鲜快照、损坏 JSON、过期快照和空串，断言 `online` 字段；internal/gameserver 用例覆盖去重、升序、跳过 `Userid<=0` 与已关闭会话。
- 期望值来自手写 UID 字面量，不由被测代码再算一遍。
- 回滚后两个新增用例变红；门禁为 `go test ./...`、`go vet ./...`、`go build ./...`。

## Out of Scope

- 不展示最近在线时间、IP、所在地图；不做踢人/封禁联动。
- 不改总览的在线会话数统计，不新增管理端接口。

## Further Notes

- [事实] gameserver 已每 5 秒把在线会话总数写入共享 KV `stats:sessions`，launcher 与独立 admin 进程都按 5 秒轮询该键。
- [事实] 会话真源是 gameserver 内存中的 `Server.sessions`，`Session.Userid` 即玩家 UID；admin 进程读不到该内存，只能通过共享 SQLite KV 取得。
- [推断] 在线灯最多滞后一个发布周期（5 秒）加一个后台刷新周期（8 秒），用于判断“玩家是否还连着”足够；30 秒有效期容忍连续漏发，同时避免异常退出后的陈旧名单长期生效。
