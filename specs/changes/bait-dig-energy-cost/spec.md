# 挖饵消耗精力与精力门槛

Status: done

<!-- 状态取值：draft | ready | in-progress | done | cancelled
     完成时原地改这里，不要归档、不要搬家、不要另开第二份。 -->

## Problem Statement

挖饵（9/3 的铲子/红虫长柄勺两条路径）是纯收益动作：只要有工具，就能无限点，
不消耗任何状态、也没有任何前置门槛。而精力系统（`player-condition-core`）
已经给出"动作消耗精力"的完整通路（`spendEnergy`）和"精力不足结果码 84"的拒绝范式，
挖饵却完全没接——`internal/gameserver/baitdig.go` 里没有任何精力相关代码。

对玩家可见的后果：体力条对挖饵玩家毫无意义，可以一边饿着冻着一边无限挖饵。

## Solution

挖饵按次消耗精力，剩余精力不够一次挖饵时拒绝（客户端表现为挖饵失败）。
消耗与饵入包在同一事务里结算，不会出现"挖到了饵但没扣精力"或反之；
扣完立刻补推一条 2/2，玩家能马上看到体力条下去一格。

## User Stories

1. 作为玩家，我希望每次挖饵消耗一点精力，以便体力对挖饵也有意义。
2. 作为玩家，我希望精力见底时挖饵被拒绝，而不是继续白拿饵。
3. 边界：精力不足导致的拒绝不产生任何饵、也不扣精力（整体回滚）。
4. 边界：精力刚好等于一次消耗时放行（扣到 0），差一点就拒绝。
5. 边界：连点同一条请求不会重复扣精力（幂等由既有经济事务与请求回执保证）。
6. 边界：运维把消耗量配成 0 时，挖饵退回改动前的行为（不扣、不拒）。

## Implementation Decisions

- 模块：`internal/gameserver`。新增 `player_condition_energy.go`（事务内扣减与 tunable），
  接入点 `baitdig.go`（`tryBaitDig`，覆盖铲子/长柄勺/新年铲等多工具分支）与
  `craft_ops.go`（`digBaitWithShovel`，9/3 单 GUID 铲子路径）。
- 为什么不复用 `spendEnergy`：它走独立的读→推进→写回，不在调用方事务里。
  挖饵必须"扣精力"与"饵入包"同生共死，因此新增 `spendEnergyTx(ctx, tx, uid, p, amount)`，
  在调用方已有的 `beginEconomyTx` 事务内完成：`EnsurePlayerConditionTx` 读 →
  `advancePlayerCondition` 按在线钟推进（空环境，与 `spendEnergy` 同口径）→ 判定 → 写回。
- 门槛口径：**不引入独立的绝对门槛常量**，门槛与消耗同源——剩余精力必须够一次挖饵的消耗。
  精力上限是 `condition_energy_max`=0.85 且可被装备加成抬高（`condition_gear_energy_max_cap`），
  写死绝对门槛（例如 0.75）会随上限变化失真，极端情况下满状态玩家也挖不了。
  不足时返回包内错误 `errEnergyNotEnough`，调用方回滚事务并回既有失败响应
  （`replyBaitDigFailed` / `replyCraftResult(false, ...)`）。
- tunables 读取必须发生在 `beginEconomyTx` 之前：`tunableF` 首次装载要查 tunables 表
  （2 秒超时），在持写锁的事务内首次调用会超时并静默回退默认值。
  因此 `conditionParamsForUID` 与消耗量都在开事务前取好并传参。
- 推送：成功扣精力后 `ctx.AfterResponse(pushKeepalive)` 立即补推 2/2（体力条不在 9/3 应答里）。
- 兼容：客户端基线 4.0.25034，无协议/表结构/迁移改动，响应形状不变（失败仍是 803 f0=0）。
  回滚方式：`bait_dig_energy_cost` 置 0，或 `player_condition_enabled` 置 0。
- tunables：`bait_dig_energy_cost`=0.15（刻度与 308.f0 一致，整条 = 1.0，钳到 [0,1]）。
  标定值，不是官服常量。

## Testing Decisions

- 单元/存储（真实 SQLite）：`spendEnergyTx` 在总开关关闭时是纯 no-op（不写库）；
  事务回滚后精力复原（证明扣减真的在调用方事务里）；边界值 0.15 放行落 0、0.1499 拒绝。
- 行为（handler 级）：精力 0.80 挖一次后落库约 0.65；精力 0.10 时挖饵返回 803 f0=0、
  背包物品数不变、精力不变；`bait_dig_energy_cost`=0 时照常挖成功且不扣精力。
- 行为（真实回环）：同一 9/3 请求、同一 call_id 连发两次时，第二次由统一 `dispatchRequest` 回执重放；
  响应字节一致，饵数量与精力均不再变化。
- 期望值来自手算（0.80−0.15=0.65）与字面量，不用同一份代码回算。
- 回滚后变红：挖饵扣精力/拒绝/边界的三个行为用例在开关置 0 后变红，属预期；
  no-op 与回滚用例应保持绿。

## Out of Scope

- 挖饵的技能/熟练度与掉落表：不动。
- 其它动作（抛投、搏鱼）的精力消耗：已由 `player-condition-core` 接入，本次不改。
- 挖饵失败时给客户端显示"精力不足"专门文案：9/3 应答没有结果码字段，
  服务端只能回 803 f0=0（与其它失败同形状）。官方是否有专门文案未知，等有样本再做。
- 精力恢复速率、上限、饱食因子：不动。

## Further Notes

- [事实] `internal/gameserver/baitdig.go` 与 `craft_ops.go` 的挖饵路径在改动前
  不含任何 energy 相关代码（`grep -n "Energy\|energy"` 无命中）。
- [事实] `conditionParamsForUID` 会叠加当前已装配竿/轮的精力上限与恢复加成，
  事务内扣减沿用该结果，避免基础上限 0.85 把装备加成截断。
- [推断] 单次消耗 0.15（满 0.85 约连挖 5~6 次见底）是让挖饵"有代价但不劝退"的标定值；
  官方真实消耗未知。
- [未验证] 官服挖饵是否消耗精力、以及精力不足时客户端弹什么提示（本改动只保证
  服务端不再无限放行）。
