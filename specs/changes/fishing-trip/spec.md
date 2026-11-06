# 钓鱼游：购票、计时、装备包生命周期与资格门禁

Status: done

## Problem Statement

凭证水域（钓鱼游地图）服务端无资格概念：3/38 购票按客户端报价成交
（gold=true price=1 曾按 1 金币成交）；1/14 切图无校验，构造请求即可
绕过购票/买断/房契直入凭证地图；钓鱼游五小时限时、进图发放官方租用
装备包、到期回收全部缺失；到期清理部分失败后资格永久残留，已到期玩家
仍可进图。

## Solution

购票走完整固定票价表（未收录地点/币种拒绝）；资格判断（1/13、1/14 共用）
= 有效钓鱼游（access 行 + 计时未清零）/买断/房契；购票后档案 f23 携带
凭证条目（客户端徽章倒计时、等级锁豁免）；进图发放、离图/下线/到期回收
官方租用装备包（f12=16|256 官方标志）；到期清零与资格回收同事务。

## User Stories

1. 作为玩家，我按面板价购买钓鱼游，进图后领到该水域租用装备（低等级
   可用高等级竿），徽章显示剩余时间；离图/下线装备回收，到期资格失效。
2. 边界：无凭证请求凭证地图 → 业务失败且位置不变；票价不符/未收录币种 →
   拒绝且不扣款；到期清理部分失败 → 补偿分支重试回收，资格不残留；
   装备登记失败 → 已发放物品补偿删除。

## Implementation Decisions

- `internal/gameserver/world.go`：3/38 票价整段走 `placeTripPrices`，
  移除"未收录按客户端报价"两条路径。
- `internal/gameserver/handlers_game.go`：hTravel 前置 `tripLevelLocked`
  （与 hEnterMap 共用），锁定地图拒绝且不改写位置。
- `internal/gameserver/trip.go`：资格 = place_access JOIN trip_state
  `remaining_sec>0`；到期清零 + 删 access 在 `ExpireTripAccessTx` 同事务
  （DELETE 带 remaining≤0 守卫防误删重购），补偿分支
  `RevokeExpiredTripAccess` 重试删除残留。
- `internal/gameserver/trip_equip.go`：发放失败（AddTripGrants 出错）时
  补偿删除已发放物品并推 4/21。
- `internal/gameserver/profilebuild.go`：f23 凭证条目（剩余游戏分钟、
  启表锚点 f4=W+3）；容器 code 定案 181（实机全链路回归可用；f4c375b
  曾按反编译误读断言 124，2026-11-06 审查复核后保留 181 并对齐注释与测试）。
- `internal/store/repos2.go`：PlaceAccessList 恢复 buyout-only（f22 只承载
  买断，房屋只走 HouseAccessList→f20），placeOwned 买断判断不再被房屋污染。
- `internal/gameserver/contract.go`：isItemTypeID 由 categoryWireClass 派生
  （与标本 PR 一致，消除 4/22 收窄冲突）。
- 数据与迁移：迁移 00035/00039（trip_state、trip_grant；00039 系避开上游 00036_fish_ecology 的重编号）；客户端基线
  4.0.25034；回滚 = revert 分支提交。

## Testing Decisions

- 行为层（真实 SQLite）：
  - `trip_review_test.go`：1/14 无资格拒绝且位置不变 → 购票后放行；
    票价四连（未收录地图/未收录币种/价格不符/正确价成功）；到期残留
    access 补偿回收；tripLevelLocked 五态矩阵；f22 买断-only。
  - `trip_test.go` / `trip_equip_test.go` / `trip_integration_test.go`：
    购票计时、离线对表、到期回收、装备发放去重、1/14 档案捕获。
- 门禁：`go build ./... && go vet ./... && go test ./internal/...`（CI 同款）。

## Out of Scope

- PR 173 侧 placeOwned 对有效 trip 的免费入场判断（该 PR 自身需另行识别
  `trip_state.remaining_sec>0`，审查已留跨 PR 备注）。
- 钓鱼游限时之外的票种（日票/月票）。

## Further Notes

- `[事实]` gold=true price=1 成交：审查临时回归复现。
- `[事实]` 1/14 无票进凭证图：审查临时回归复现。
- `[事实]` f23 code 181 实机可用：购票→进图→抛竿→到期回收全链路回归
  通过（抛竿受 FishingSceneVoucherWathDog 门控）。
- `[推断]` f4c375b 的 124 断言系误读：其"125=bncmdhbgahe"邻位说法与
  在库 wire 表矛盾（bncmdhbgahe=600）。
