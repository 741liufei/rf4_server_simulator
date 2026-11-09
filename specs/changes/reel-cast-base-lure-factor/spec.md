# 路亚族钓组的每竿基准折扣

Status: done

## Problem Statement

渔轮磨损账修正（拉力单位、重排双扣）后，路亚的零件消耗主体只剩"每竿基准 + 泡水"。
但消耗矩阵的"一竿"按底钓节奏标定：路亚一小时几十到几百竿，同一只轮的基准账
被抛投频率放大 5~10 倍——实机 2026-11-09 02:08~02:09 的 79 秒里 9~10 竿，
品质 50 轮油每竿 57.6bp，一小时内必然见底。

## Solution

每竿基准（每竿 + 泡水两项）按钓组类型打折：路亚族（`rigmeta.go:rigTypeLureFamily`，
钓组 5~11、19、20、22、50~54）乘 0.25（tunable `reel_cast_base_factor_lure`），
底钓与其它钓组不打折。战斗磨损（锁轮过载秒、泄力打滑、断件冲击）是搏鱼事件账，
不随抛投频率放大，不打折。

折扣后体感：品质 50 轮油每竿 14.4bp（原 57.6bp），q100 ≈ 9bp；品质 10 轴承
2.9bp（原 11.4bp）。底钓经济完全不变。

## User Stories

1. 作为路亚玩家，我希望高频抛投不会把保养品按底钓价格烧穿，以便路亚养成可持续。
2. 作为玩家，我希望搏鱼本身的磨损（过载/打滑/断件）保持原样，以便搏大鱼的风险不减。
3. 边界：旧账本没记折扣（castBaseFactor≤0）按 1 结算，旧档兼容；折扣随竿走，
   换鱼、换账段都不丢。

## Implementation Decisions

- `fightReelWear` 新增 `castBaseFactor` 字段：抛竿标记（`markRodUsedLocked`）时按
  `s.reelCastBaseFactor(st.RigType)` 记入；`recordFightReelLoad` 换鱼换账时随
  Used/BaseCharged 一起保留。
- `reelPartCounts.Rods/Soaks` 改 float64，承接 0~1 的折扣计数；
  `reelPartWearFor` 数学不变（乘出来的就是折扣后的点数）。
- 折扣系数 tunable `reel_cast_base_factor_lure`，默认 0.25，≤0 回落默认。
- 回滚方式：tunable 设 1 即等效回滚；代码还原只涉及 `scheduleBiteState` 一处调用。

## Testing Decisions

- 单元：`TestFightWearCastBaseLureFactor` —— 非路亚不打折、路亚默认 0.25、
  折扣只进 Rods/Soaks、旧账按 1、手算样例（q50 油 = 14.4bp）、战斗计数不打折。
- 既有 `TestFightWearRolloverChargesBaseOncePerCast` 随签名更新（传折扣 1），
  语义不变。回滚后两测试变红。
- 门禁：build/vet/聚焦测试；gameserver 全包仅余两个既有抖动
  （TestCafeNativeFlowPaysAndPushesRemoval / TestFishMarketFlow，基线同红）。

## Out of Scope

- 不改消耗矩阵数值本身（`reelPartWearMatrix`）。
- 不按钓组细分多档折扣（单系数 + tunable，调参走配置不走代码）。
- 不动战斗磨损（过载/打滑/断件）与摩擦片烧穿曲线。

## Further Notes

- `[事实]` 路亚抛投节奏：实机 79 秒 9~10 竿（`item_damage_log` 时间戳序列）。
- `[事实]` 每竿基准与品质系数精确对账：油 q50=57.6bp、轴承 q10=11.43bp、
  齿轮 q10=8.57bp（日志 part_wear_bp 与矩阵手算一致）。
- `[推断]` 0.25 折扣使路亚一小时的油消耗回到底钓同量级（q50 ≈ 50%/小时
  重度抛投、q100 ≈ 31%/小时）；体感仍偏高则调 tunable，不再改代码。
