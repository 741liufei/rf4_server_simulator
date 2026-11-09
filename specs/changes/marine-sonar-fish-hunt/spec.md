# 海图声呐鱼群与狩猎状态联动

Status: done

## Problem Statement

海图声呐此前只有静态声呐档案或空网格，探鱼器无法反映当前水域名册、鱼种水层和狩猎阶段；即使生成回声，图标重量位、鱼群连续性、深度带与真实鱼口也没有共用同一套数据。海图狩猎的搜索进度还缺少声呐区号和动作条件的稳定状态。

## Solution

海图声呐从当前海域真实鱼种名册生成 2-4 组连续椭圆鱼群。每组鱼群选择实际鱼种，按生态水层决定主线深度，按 `settleStatics` 与当前海图狩猎阶段生成回波重量；客户端据此显示对应大小图标。声呐只读取饵、生态、声呐区和狩猎状态，不推进咬口、保底或真实待结算鱼。海图狩猎以声呐区号、Jig/拖钓/漂流动作、水深和鱼群习性计算独立阶段，区号变化会重置搜索进度。

## User Stories

1. 作为海图玩家，我希望探鱼器显示当前海域可能出现的真实鱼种鱼群，而不是随机占位鱼。
2. 作为玩家，我希望同一鱼群连续成带，能看到大致深度和大小级别，以便调整放线深度。
3. 作为玩家，我希望声呐鱼群受当前海图狩猎阶段影响，但查看声呐本身不会消耗鱼口或推进保底。
4. 边界：未知水域或无声呐网格返回客户端可解析的空 `args(496)`，不得断线。
5. 边界：旧海图狩猎分支迁移编号会自动重映射，不影响现有测试库升级。

## Implementation Decisions

- `internal/gameserver/sonar.go`：把回波对象 f0 明确为 kg 重量，使用客户端 3 kg / 10 kg 图标阈值；用椭圆鱼群代替离散热点；重量采样使用与真实判档相同的 `settleStatics`，同一鱼群回波围绕学校主深度连续分布。
- `internal/gameserver/fish_hunt.go`：新增海图狩猎模式、声呐区号、搜索动作、鱼群习性和区号变化重置；淡水仍使用窝料狩猎。
- `internal/gameserver/fish_hunt.go`：未知位置只降低本次判定置信度，不抹掉上一次已知区号；下一次已知区号相同则续进度，不同才重置。
- `internal/gameserver/protocol_dynamic_handlers.go`：1/10 声呐请求优先使用真实抛竿落点 `CastPos`，并与真实咬口共享当前竿位、鱼饵、钩型、技能、动作、水深、船况、天气和欢乐时光加成。
- `internal/gameserver/fish_bite_environment.go`、`fish_roll_context.go`：把 `loc12_*` 声呐系统键带入统一判鱼上下文的只读快照。
- `internal/store/migrations/00050_marine_hunt.sql`：为 `fish_hunt_state` 增加 `hunt_mode`、`sea_zone`、`sea_zone_known`；旧分支的 `00046_fish_hunt` 和 `00047_marine_hunt` 会按文件名依次重映射到 `00049/00050`。
- 客户端协议仍为 1/10、496、497；不新增命令，不改变其它响应形状。

## Testing Decisions

- `sonar_test.go`：验证鱼群连续性、重量/深度范围、名册鱼种、阶段权重、空网格形状、图标阈值，以及声呐重量线与真实判档线一致。
- `fish_hunt_test.go`：验证海图声呐区可建立狩猎状态且不依赖窝料。
- `fish_hunt_test.go`：验证已知区号 → 未知位置 → 已知区号时，相同区号保留进度、不同区号才重置。
- `protocol_dynamic_handlers_test.go`：验证声呐上下文优先使用 `CastPos` 并继承当前竿位与技能加成。
- `protocol_dynamic_handlers_test.go`：验证 1/10 空回波使用客户端可解析的 `args(496)`。
- 存储测试：验证旧 `00046_fish_hunt` / `00047_marine_hunt` 迁移号能升级到主线 `00049/00050`。
- 门禁：`go test ./... -count=1`、`go vet ./...`、`go build ./...`。

## Out of Scope

- 不显示真实个体鱼种名称或精确到单尾的咬口结果。
- 不改变海图鱼价、鱼种名册或经济结算。
- 不新增客户端协议字段。
- 声呐鱼群是概率态势，不代表查看后一定立即咬钩。

## Further Notes

- `[事实]` 客户端 `DEEPVIEW` 已实机显示连续鱼群和深度回波。
- `[事实]` 实机日志出现 `loc12_14`、`loc12_20` 等真实鱼种与深度组合。
- `[事实]` 实机日志 `loc12_14` 出现 `lh_kambala@11.5m`、`zoarces_v@11.7m`、`poutassou@11.4m`、`piksha@6.6m`，`loc12_20` 出现 `chimaera@49.3m`、`thynnus_t@47.5m`。
- `[事实]` 客户端 497 回波 f0 需按 kg 重量解释，3 kg / 10 kg 为大小图标分档。
- `[推断]` 海图鱼群习性是现有生态字段的代理值，后续可由专门的社会性/伏击参数替换。
