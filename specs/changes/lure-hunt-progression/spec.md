# 路亚独立的大鱼狩猎进度

Status: done

## Problem Statement

淡水鱼猎的发窝判定先检查窝料账本。路亚竿即使持续抛投、收线和命中拟饵，只要没有大窝就会停在日常阶段，导致路亚玩家无法通过自己的钓法积累大鱼机会。

## Solution

路亚族使用独立的 `fishHuntModeLure` 狩猎进度，不再读取 `groundbaitSpotAt`。进度由持续搜索时间、有效咬口、拟饵与水层/鱼种匹配、收线动作质量共同建立；从窝料钓法切换到路亚时清空共享进度，避免把窝料发窝度直接继承成路亚搜索度。

## Implementation Decisions

- `internal/gameserver/fish_hunt.go`：`fishHuntReadiness` 先区分海图、路亚和淡水窝料三条链路；路亚族走 `fishLureHuntReadiness`。
- `internal/gameserver/fish_hunt.go`：新增 `fishHuntModeLure`。路亚进度复用 `fish_hunt_state` 的时间、有效咬口、保底和补偿字段，不新增迁移。
- `internal/gameserver/fish_hunt.go`：路亚的阶段由搜索质量 `Q` 决定，不要求 `groundbait.units > 0`；拟饵动作权重由 `lureActionTypeWeight` 参与，路亚持续搜索可在无窝料时进入聚鱼和巨物阶段。
- `internal/gameserver/fish_hunt.go`：窝料钓法切回时仍使用原有窝料强度、死窝和鱼种亲和；从路亚切回窝料不继承路亚搜索进度。
- `internal/gameserver/fish_hunt_test.go`：覆盖无窝料路亚持续搜索进入巨物阶段、刚换路亚不直接进入巨物、窝料/路亚模式切换清空共享进度。

## Testing Decisions

- 纯逻辑测试：无窝料、1800 秒搜索和 30 次有效咬口可达到巨物阶段。
- 边界测试：刚换路亚不会直接进入巨物阶段。
- 状态切换测试：从淡水窝料模式切到路亚模式会清空 `ActiveSeconds` 和 `ValidBites`。
- 回归门禁：`go test ./... -count=1 -parallel 1`、`go vet ./...`、`go build ./...`、`git diff --check`。

## Out of Scope

- 不改变窝料钓法的窝点强度、死窝阈值和鱼种亲和公式。
- 不改变海图声呐狩猎链路。
- 不新增客户端协议字段或数据库列。
