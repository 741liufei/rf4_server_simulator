# 登录恢复保留上次地图

Status: done

## Problem Statement

1/13 空请求是登录恢复，服务端先从 `profile.last_level_key` 读回上次地图，但旧逻辑只要该地图在 `placeTripPrices` 里且玩家当前没有有效钓鱼游或买断，就用 `tripLevelLocked` 把位置改回出生图。结果是银币票进入后停在付费地图的玩家每次重登都站在出生图；同时 1/13 带 key 的银币票进入也会在 `settleTravel` 扣费前被误拦。

## Solution

登录恢复直接采用 `profile` 里的上次位置，不扣门票。只有该地点存在已到期钓鱼游记录、玩家没有买断/房屋/有效钓鱼游，且到期后也没有新的 `place_travel` 银币票流水时，才回出生图。

1/13 带 key 的真正进入不再用 `tripLevelLocked` 前置拦截，统一交给 `settleTravel`：金币买断、有效钓鱼游、房屋免票，否则扣银币门票；余额不足整体回滚。

## User Stories

1. 作为银币票玩家，下线时停在付费地图，重登后仍在原图且不重复扣票。
2. 作为已金币买断地图的玩家，重登后仍在原图。
3. 作为钓鱼游仍在期限内的玩家，重登后仍在原图。
4. 作为仅依赖钓鱼游进入的玩家，游到期后重登回出生图，不继续保留已失效地图。
5. 作为运维，带 key 进入付费地图的银币票路径不中断，买断/钓鱼游/房屋的免票行为不变。

## Implementation Decisions

- `hEnterMap`：移除 `charge && tripLevelLocked(...)` 前置回退；带 key 的进入由 `settleTravel` 统一结算。
- `trip.go`：新增 `loginRestoreTripExpired`，只在空请求恢复时识别“仅钓鱼游且已到期”的地图。银币票停留没有 `place_access` 行，因此不能直接用 `tripLevelLocked` 判断。
- `loginRestoreTripExpired` 同时检查 `txn.note='place_travel'`：钓鱼游到期后用银币票重新进入过的地图仍可恢复。
- 位置写回、旅费结算、装备回收逻辑不变；登录恢复仍然 `charge=false`，不扣旅费。
- 进程重启后的恢复路径（`currentLevel` 从 `player_location` 读）不变。

## Testing Decisions

- `TestEnterMapKeyedPaidMapChargesSilver`：1/13 带 key 进入北海，无买断/钓鱼游但有足够银币，必须扣票并落图。
- `TestLoginRestoreKeepsPaidMapByAccessMode`：银币票停留、金币买断、有效钓鱼游三种入口重登都保留地图，且不重复扣费。
- `TestLoginRestoreExpiredTripFallsBackToSpawn`：仅钓鱼游且已到期，空请求回出生图。
- `TestLoginRestoreExpiredTripWithLaterTicketKeepsMap`：到期后用银币票重新进入并下线，重登仍保留地图。
- 既有 `TestPlayerLocationFirstLoginTravelReconnectAndRestart`、`TestPlayerLocationInvalidMapFallsBackToSpawn`、`TestTravel*` 全部保持通过。

## Out of Scope

- 不改变钓鱼游票价、有效期与购票流程。
- 不改变无效地图 key 回出生图的兜底。
- 不改变切图扣费与并发幂等。
