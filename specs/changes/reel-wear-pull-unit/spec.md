# 搏鱼磨损账的鱼拉力单位对齐（牛 → 公斤力）

Status: done

## Problem Statement

玩家路亚搏鱼时，渔轮摩擦片与轮油一场掉百分之十几，一晚烧掉近半，且掉的鱼普遍只有几百克。
实机损伤日志（2026-11-09 01:37）显示：461g 的鱼被磨损账记成 `pull_kg 6.57`，
对 1.87kg 的泄力按封顶 2 倍每条 14/8 打滑，一场烧掉摩擦片 10.6%、轮油 1.24%。

根因：27.f8 鱼拉力的量纲是**牛**（官方样本严格 = 体重kg×g，见
`docs/analysis-fish-fight-physics-2026-11-07.md` §三），而渔轮泄力/锁轮承力以**公斤**计。
`fightReelCache` 把 f8 牛数直接灌进磨损账的 `pullKg`（字段注释却写"公斤力"），
两边相差 9.80665 倍，任何 ≥ 约 0.3kg 的鱼都被判成超泄力/超锁轮承力，全程打滑或全程过载。

## Solution

磨损账入账前把 f8 拉力换算成公斤力（÷9.80665）。换算后鱼拉力回到
"体重kg × 冲刺倍率"的公斤数量级：小鱼对低泄力不再打滑，只有真正拉力超过
泄力/锁轮承力的鱼才磨损摩擦片与轮内零件。27.f8 报文本身保持牛，客户端不受影响。

## User Stories

1. 作为玩家，我希望几百克的小鱼搏鱼一整场几乎不磨摩擦片，以便保养品消耗与鱼的大小相称。
2. 作为玩家，我希望锁轮搏大鱼时磨损仍按发热曲线结算，以便烧片风险保留。
3. 边界：拉力恰好等于泄力时仍不算打滑（原有边界不变）；`fishFightPullFor` 的
   牛数口径（27.f8、战斗曲线）完全不变。

## Implementation Decisions

- 换算系数 `newtonPerKgf = verifiedForcePerGram × 1000`，与 f8 的每克系数**同源导出**，
  放在 `fishing.go` 常量区；禁止在调用点写死 9.80665 字面量。
- 新增 `Server.fishFightPullKgfFor`（紧跟 `fishFightPullFor`），只在
  `fightReelCache` 组账时使用；`fishFightPullFor` 及 27.f8 下发不动。
- 损伤日志 `pull_kg` 字段从此为真公斤力；历史日志保持旧口径，不做迁移。
- 回滚方式：还原 `fightReelCache` 一行调用即可，无数据迁移。

## Testing Decisions

- 单元（纯逻辑）：`TestFishFightPullKgfIsBodyWeight` —— 断言换算后拉力
  = 体重kg × 冲刺倍率（期望值由量纲恒等式手算，非同代码复算），同时钉住
  f8 牛数 = 体重kg × newtonPerKgf × 冲刺倍率。
- 单元（账本边界）：`TestFightReelWearBoundaryUsesKgfNotNewton` —— 用实机事故
  数字（461g 鱼、1.87kg 泄力、3.2kg 片）做正反对照：旧口径（牛当公斤）必打滑，
  新口径零磨损。回滚后此测试变红。
- 行为（真实装配接线）：`TestFightReelCachePullKgfNoSlipForSmallFish` ——
  造竿 + bclp_5004 + 461g 鱼过 `fightReelCache`，断言 `pullKg < dragKg` 且
  整场 14/8 零磨损；并正控断言 f8 牛数确实高于该轮泄力（保证测试测得出旧 bug）。

## Out of Scope

- 不调 `reel_locked_burn_base_s`、零件消耗矩阵等标定数值（单位修正后单独按体感再议）。
- 不改 14/8 探针（`fightLoadProbe`）与客户端协议字段。
- 不迁移历史损伤日志的 `pull_kg` 口径。

## Further Notes

- `[事实]` 27.f8 = 体重kg×g（牛）：官方 53g→0.5198、63g→0.6178 两样本
  （`fishing.go` verifiedForcePerGram 注释）与客户端反编译分析一致。
- `[事实]` 实机事故：461g 鱼 `pull_kg 6.57` vs `drag_kg 1.87`，9 条 14/8 全打滑，
  `slip_wear 1057.5bp`；同晚 5 场合计约 45% 摩擦片寿命（`item_damage_log`）。
- `[事实]` 14/8 探针同场实测线上张力 max ≈ 2.0（被 1.87kg 泄力钳住），
  证明真实张力从未超过泄力。
- `[推断]` 修复后轮油一晚消耗回落到"每竿基准"主导（品质 15 约 1%/竿），
  体感仍偏快但属 reel-friction-life-model 的设计口径，数值另议。
