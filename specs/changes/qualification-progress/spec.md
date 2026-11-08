# 资格任务判定与进度下发

Status: done

## Problem Statement

资格面板此前只有分支树和静态进度分母，没有服务端判定，进度恒为 0。实装后仍有三处
判定时机错误：

1. 天然饵在 14/11 中鱼时被 `consumeHookedBait` 删除，14/4 结算再按饵 GUID 反查只能
   得到零值，天然饵类、饵尺寸、味型与具体饵计数全部不计。
2. 抛投距离条件取 `castDistanceForRig` 的理论最大距离，短抛也会满足「抛投 ≥30m」。
3. 夜间条件取 14/4 结算时刻，搏鱼跨过 06:00/19:00 后按错误时段判定。

另外资格进度原先在结算事务外单独写库，失败只记日志；重放又因 `res.replayed`
完全跳过判定，回执丢失时进度无法补记。

## Solution

- 咬钩时把**饵属性、本杆实际落点距离、是否夜间**快照到 `RolledFish`；14/4 只用快照
  装配资格事件，不再按已删除的饵 GUID 反查。
- 资格判定移入 `settleCatchOnce` 的同一事务：经验、磨损、回执与资格进度要么一起提交，
  要么一起回滚。
- 回执（`catchReceipt`）保存本尾鱼带来的资格变化；重放时从回执还原 801.f3 增量，
  能补发丢失的响应，同时去重键保证不会重复计数。
- 新增 `specs/changes` 说明与本文件对应的测试，回滚依据见 Testing Decisions。

## User Stories

1. 作为玩家，用手竿挂天然饵钓上鱼，即使 14/11 已吃掉钩上饵，结算仍会累计「使用天然饵」
   任务。
2. 作为玩家，只抛出 5m 不会完成「抛投 ≥30m」；抛到 30m 以上才计数。
3. 作为玩家，夜间咬钩后搏鱼到天亮，完成情况仍按咬钩时刻的夜间判定。
4. 作为玩家，14/4 响应丢失后客户端重发同一请求，能够补回本尾鱼的资格进度，且不会
   重复弹完成窗。

## Implementation Decisions

- `RolledFish` 新增 `QualBait`（`qualificationBait`）、`CastM`、`IsNight` 三个咬钩快照
  字段，并随 `fishing_state.pending_json` 持久化；原有 `DepthCM/OnBoat/Drift` 也补入
  `FishingFish` 映射，重启后不缺条件。
- `FishState.CastM` 在 14/2 由 `throwDistance(ctx.Tree)` 写入，并持久化到
  `fishing_state.cast_m`（迁移 `00048_fishing_state_cast_m.sql`）；咬钩时复制到
  `RolledFish`，不再使用 `castDistanceForRig(...).DistanceM` 作为实际距离。
- `worldIsNight(unix)` 统一夜间口径（19:00–06:00），咬钩与结算共用。
- `settleCatchOnceWithQualification` 在结算事务内调用 `evaluateQualificationsTx`；
  任一条资格写入失败返回错误，整笔结算回滚并提示重试。
- `catchReceipt.Qualification` 保存 `map[int64]store.QualificationState`；重放时由
  `catchReceipt.result(true)` 还原到 `settleXpResult.qualChanges`。
- `qualificationEventForCatch` 从原竿 `f.GearGUID` 读取竿型/线材质，避免中途换竿把新竿
  条件算到旧鱼上。

## Testing Decisions

- `TestQualificationBiteSnapshotSurvivesBaitConsumption`：真实咬钩 → 14/11 消耗并删除
  天然饵 → 14/4 结算，断言 4003 进度为 1。旧实现在扣饵后反查 GUID，进度为 0。
- `TestCastSnapshotsRequestedDistance`：14/2 上报 5m 后断言 `FishState.CastM == 5`；
  旧实现写入钓具最大值。
- `TestWorldIsNightBoundaries`：覆盖 00/05/06/18/19/23 点的夜间边界。
- `TestQualificationSettlementAtomicAndReplayable`：资格表不可用时断言经验与回执一起
  回滚；恢复后首次结算写入 1001；新进程重放返回回执中的资格变化且计数仍为 1。
- `TestFishingStateRestorePendingNoReroll`：搏鱼中重启后，pending 的 `DepthCM/OnBoat/Drift`、
  `QualBait/CastM/IsNight` 与鱼本体一起原样恢复。
- `TestFishingStateCastMSurvivesRestartBeforeBite`：14/2 抛投后、咬钩前重启，断言
  `fishing_state.cast_m` 恢复为本杆实际距离。
- 以上用例在回滚到旧实现时分别因饵快照缺失、距离取最大值、跨时段判定、事务外写入和
  重启丢快照而变红。

## Out of Scope

- 不改 5003/5004/5005 的 wire 形状或 args code；新增 00048 只给 `fishing_state` 补
  `cast_m` 列，不改资格进度表结构。
- 不改资格任务文案、目标分母与规则表数值。
- 不处理客户端本地化文案缺项；不引入活动分支（19/20）判定。
