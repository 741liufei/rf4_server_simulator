# 登录恢复保留上次地图

Status: ready

## Problem Statement

1/13 空请求是登录恢复，服务端先从 `profile.last_level_key` 读回上次地图，但只要该地图在 `placeTripPrices` 里且玩家当前没有有效钓鱼游或买断，`tripLevelLocked` 就会把位置改回出生图。结果玩家每次重新登录都站在克马羚诺也湖，进度感丢失；日志里能看到成对的 `1/13 目标地图无有效钓鱼游，回出生地图` 与 `1/13 下发出生地图 level=level_001_lake`。

## Solution

把资格检查限定在真正的切图动作上：只有 1/13 携带地图 key（`charge == true`）时才执行 `tripLevelLocked` 回退；登录恢复（空请求）直接采用 `profile` 里的上次位置，资格问题留给钓鱼入口与 3/38 购票流程。

## User Stories

1. 作为玩家，上次在海图/20 图下线，重新登录后仍在同一张图，而不是被送回出生图。
2. 作为玩家，钓鱼游到期后重新登录，位置保留，但进入钓鱼流程时仍会被门票/钓鱼游规则拦下。
3. 作为运维，切图路径的防逃票行为不变：带 key 进入失效付费地图仍然回出生图。

## Implementation Decisions

- `hEnterMap`：`if srv.tripLevelLocked(...)` 改为 `if charge && srv.tripLevelLocked(...)`。
- 位置写回、旅费结算、装备回收逻辑不变；登录恢复仍然 `charge=false`，不扣旅费。
- 进程重启后的恢复路径（`currentLevel` 从 `player_location` 读）不变。

## Testing Decisions

- 新增 `TestLoginRestoreKeepsLastPaidMap`：把 `last_level_key` 设为 `level_012_north_sea`（无钓鱼游、无买断），1/13 空请求必须返回该地图，且 `currentLevel` 同步。
- 既有 `TestPlayerLocationFirstLoginTravelReconnectAndRestart`、`TestPlayerLocationInvalidMapFallsBackToSpawn`、`TestTravel*` 全部保持通过，确认首登、无效地图与切图扣费行为未回退。
- 回滚该改动后新用例会变红（返回 `level_001_lake`）。

## Out of Scope

- 不改变钓鱼游票价、有效期与购票流程。
- 不改变无效地图 key 回出生图的兜底。
- 不改变切图扣费与并发幂等。
