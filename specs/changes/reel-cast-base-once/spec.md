# 渔轮每竿基准一竿一次（重排续段不得重扣）

Status: done

## Problem Statement

单位修复（reel-wear-pull-unit）后实机复测：600g 鱼的搏鱼账已归零，但一分钟内
同一竿、同一只轮出现**同秒成对**的 `reel_overload`，每对都各扣一份每竿基准
（轮油 57.6bp + 轴承 11.43bp + 齿轮/摇柄 8.57bp + 线杯 2.29bp），零件消耗仍是
预期的一倍左右。

`[事实]` 根因（2026-11-09 02:08~02:09 日志，rod 1abe6fdc / reel cf85df58）：
`scheduleBiteState` 是抛竿与三类"重排"共用的入口，它在 claim→reset 后无条件
`markRodUsedLocked` 重打每竿基准。嘬饵否决（14/11 否决分支）、鱼离场（14/12）、
补排咬钩都是**同一竿续段**的重排（resetCast=false），重排打上的新基准会被客户端
随即重抛的 14/7 当作"上一竿未落库的账"取走落库——同一竿的基准被扣两次。

## Solution

把账段滚动抽成 `rollFightWearLedgerLocked`：取走未落库部分后 reset，
只有 `resetCast=true`（真正换新的一竿）才重新打每竿基准与泡水；重排续段
不带基准，续段里发生的战斗磨损（打滑/过载/断件）照常落库。

## User Stories

1. 作为玩家，我希望一竿只扣一次每竿基准，以便嘬饵频繁的路亚钓法不双倍消耗零件。
2. 边界：续段内发生的锁轮过载/打滑/断件冲击仍正常入账；新竿重新扣基准。

## Implementation Decisions

- `reel_fight_wear.go` 新增 `rollFightWearLedgerLocked(st, rodGUID, newCast)`，
  `fishing.go` 的 `scheduleBiteState` 改调它，`if resetCast` 才 `markRodUsedLocked`。
- 四个重排调用点（嘬饵否决、14/12 鱼离场、两处补排咬钩）无需改动，语义由
  resetCast 参数表达。
- 回滚方式：还原 `scheduleBiteState` 三行即可。

## Testing Decisions

- 单元（账段语义）：`TestFightWearRolloverChargesBaseOncePerCast` ——
  抛竿标记 → 重排（第一段带基准、新段不带）→ 无磨损续段不产生落库账 →
  换竿重扣 → 续段战斗磨损落库且不带基准。回滚后变红。
- 门禁：build/vet/聚焦测试全绿；gameserver 全包跑仅剩两个既有抖动
  （TestCafeNativeFlowPaysAndPushesRemoval / TestFishMarketFlow，基线同红）。

## Out of Scope

- 不调零件消耗矩阵数值（每竿基准 × 路亚抛竿频率的量级标定另行决策）。
- 不改 14/15 推送与客户端重抛行为。

## Further Notes

- `[事实]` 同秒成对日志：02:09:04/:11/:23/:28 各两条，同 rod 同 reel，均为
  rod_base=1、零战斗磨损（`item_damage_log`）。
- `[推断]` 去重后路亚一小时几百竿的抛投频率下，每竿基准仍是消耗主体
  （品质 50 油 ≈0.58%/竿、品质 10 轴承 ≈0.11%/竿），是否按钓法调标定另行讨论。
