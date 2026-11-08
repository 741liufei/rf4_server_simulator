# 玩家健康（f4）动态：饥寒掉血与药品恢复

Status: done

<!-- 状态取值：draft | ready | in-progress | done | cancelled
     完成时原地改这里，不要归档、不要搬家、不要另开第二份。 -->

## Problem Statement

四条角色状态条的第四条（308.f4）在本仓长期是死值：`player-condition-core`
把 `f4` 建模为 `luck_penalty`，只落库与透传，客户端 HUD 显示 `1-f4`。
`player-condition-core` 的 Out of Scope 明确写着"f4 幸运/健康的全部动态：下降与恢复
触发无任何官服样本，留待后续 spec（需先自设计规则）"。

结果是对玩家可见的两件事都不成立：饿了、冻着不减员，吃药也没有任何效果。
玩家可以把饱食度/舒适度完全放空而没有任何后果，药品类物品（Sorbimax）在
4/8 使用通道里甚至不属于消耗品，用了等于白扔。

## Solution

第四条状态条变成"健康"：饱食度或舒适度掉到阈值以下就开始掉血（两个都低掉得更快），
脱离饥寒后缓慢自愈；吃 Sorbimax 立刻回一大截。掉血与自愈都按在线钟懒计算，
与既有饱食度/精力/舒适度同一条推进链路；数值全部走 tunables。

## User Stories

1. 作为玩家，我希望长时间不进食或被冻着时看到第四条状态条缓慢下降，以便"饿着/冻着"真的有代价。
2. 作为玩家，我希望离开饥寒条件后血条自己慢慢回来，而不是只能吃药。
3. 作为玩家，我希望吃 Sorbimax 立刻回血，并且药确实从背包扣掉。
4. 边界：饱食度与舒适度都低于阈值时，掉血速率是单压力源的两倍。
5. 边界：离线期间不掉血也不自愈（与既有饱食度/精力的离线口径一致）。
6. 边界：健康不会掉过头（f4 ≤ 1）、自愈不会溢出（f4 ≥ 0，即 HUD 不超过满格）。
7. 边界：总开关关闭时行为与改动前完全一致（f4 恒为落库值，药品重新变成"用了没效果"）。

## Implementation Decisions

- 模块：`internal/gameserver`（状态推进、消耗品分类与恢复、tunables）。
  `internal/store` 不改：`player_condition.luck_penalty`（迁移 00046）已经是 f4 的落库列，
  语义从"恒 0 透传"改为"健康损耗"，**无新迁移**。
- 字段语义：沿用上游命名 `luck_penalty`，含义是"健康的反向刻度"——`f4=0` 满血、
  `f4=1` 见底；客户端 HUD 显示 `1-f4`，因此不需要任何协议改动。
  308 构建（`buildPose308Env`）已经下发 `c.LuckPenalty`，本次一行不动。
- 推进规则（`advancePlayerCondition`，纯函数，在舒适度之后按同一 `elapsedH` 结算）：
  取推进后的饱食度与舒适度判定压力源——饱食度 < `condition_health_hunger_threshold`
  算一个，舒适度 < `condition_health_comfort_threshold` 算一个。
  有压力源时 `f4 += decay_per_hour × 压力源数 × elapsedH`，无压力源时
  `f4 -= regen_per_hour × elapsedH`；两次都钳到 [0,1]。
  压力判定用推进后的值，保证与同一拍下发的 f1/f3 自洽（不会出现"条已经掉到阈值下但这一拍还不算饿"）。
- 写回：`currentPlayerConditionP` 的"变化是否超过显示噪声阈值"判定补上 luck_penalty，
  否则健康变化永远不被持久化。
- 消耗品：`consumableRestoreForItem` 新增 `pill_pack → "medicine"`，
  `applyConsumableRestoreTx` 新增 medicine 分支（`f4 -= condition_medicine_health_restore`）。
  与扣物同事务，走既有 4/8 使用通道，成功后立即补推 2/2。
  分类依据是静态库属性而不是物品名猜测：`item_def` 里品类 26 共 247 条，
  按 `attrs_json.fopbhlmkcpe` 分为 154 食物(1) / 92 饮品(2) / **1 药品(3)**，
  药品唯一项就是 `pill_pack`（Sorbimax）。饵料香料与面团（`aroma_garlic`、
  `dough_garlic`、`ff_garlic` 等，品类 20/21）不是可食用物，不进表。
- 兼容：客户端基线 4.0.25034，无协议/表结构/迁移改动。
  回滚方式：把 `player_condition_enabled` 置 0，或把 `condition_health_decay_per_hour`
  与 `condition_health_regen_per_hour` 都置 0。
- tunables（tunables 表，30 秒缓存，改值免重启）：
  `condition_health_decay_per_hour`=0.10、`condition_health_regen_per_hour`=0.05、
  `condition_health_hunger_threshold`=0.10、`condition_health_comfort_threshold`=0.30、
  `condition_medicine_health_restore`=0.50。全部为自设计标定值，不是官服常量。

## Testing Decisions

- 单元（纯逻辑，`advancePlayerCondition`）：手算样例覆盖单压力源 1h 掉 0.10、双压力源
  0.5h 掉 0.10（含雨天舒适度漂到 0.25 仍算压力源）、无压力 2h 回升 0.10、
  上下限钳制、`updated_ts` 未前进时不推进。期望值来自手算，不用同一份代码回算。
- 存储（真实 SQLite）：健康变化必须写回 `luck_penalty` 列（验证写回阈值判定已覆盖 f4）。
- 行为（handler 级）：`pill_pack` 经 4/8 使用后 f4 从 0.60 掉到 0.10、只扣 1 个、
  其它三条状态条不动、补推 2/2；满血时吃药钳到 0；饵料类物品不进消耗品白名单。
- 既有回归：`TestKitchenEatRestoresHungerAndPushes`（f4=0 透传）、
  `TestAdvancePlayerConditionHandComputed`（comfort 透传、健康停在地板）、
  `TestBuildPose308DoesNotAdvanceCondition`（非 2/2 不推进）必须保持绿。
- 回滚后变红：健康组（掉血/自愈/钳制/写回/药品恢复）在健康参数置 0 后必然变红，属预期。

## Out of Scope

- 健康掉到 0 的惩罚（昏厥、强制回城、掉落）：无官服样本，也不在本次机制范围内。
- f4 对玩法数值的影响：客户端对 308 值不做任何玩法计算（`player-condition-core`
  已实测），因此本次只让状态条"活起来"，不新增任何数值修正。
- 衣物防寒/防热对舒适度（进而对健康）的修正：模拟器未建模服装穿戴。
- 疾病/中毒/受伤等其它掉血来源：无数据。
- 药品的冷却与耐受（连续吃药递减）：无数据。

## Further Notes

- [事实] `f4` 的落库列是 `player_condition.luck_penalty`（迁移 00046），
  `buildPose308Env` 直接下发 `proto.F32(float32(c.LuckPenalty))`；
  `player-condition-core` 记录的客户端行为是 HUD 显示 `clamp(1-f4,0,1)`。
  因此"健康"不需要新增列，也不需要新迁移。
- [事实] 静态库 `item_def`（`server_data/static.sqlite3`）品类 26 的
  `attrs_json.fopbhlmkcpe` 分布为 1:154 / 2:92 / 3:1，值 3 的唯一项是
  `pill_pack`（`kind=sorbimax`，`ljnnpokapig=5`）。官方没有第二种药品条目。
- [事实] 上游在 `specs/changes/player-condition-core` 里把 f4 动态明确列为 Out of Scope，
  理由是"无任何官服样本，需先自设计规则"——本 spec 就是那份自设计规则。
- [推断] 阈值取"饱食度 10% / 舒适度 30% 以下才算压力源"，是让"饿到快见底"和
  "冻得明显不舒服"才付代价；掉血 0.10/h 比自愈 0.05/h 快一倍，让饥寒有累积性。
  全部为设计取值，无官服锚定。
- [未验证] 官服是否存在健康条下降/恢复的机制与速率（本改动不复刻官服，只提供一套自洽规则）。
- [未验证] 客户端是否允许对 `pill_pack` 发起 4/8 使用请求（品类 26 + 类型 128 与其它
  消耗品一致，服务端按同一路径处理；若客户端有独立交互，需实机再核）。
