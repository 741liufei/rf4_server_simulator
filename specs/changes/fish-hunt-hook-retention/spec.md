# 大鱼狩猎的钩型入口、保底阶段与脱钩补偿

Status: done

## Problem Statement

大鱼狩猎上线后存在几处规则不一致：重复的中钩确认会再次抽取钩型保持率并可能丢掉已经中钩的鱼；蓝冠虽然不改原始权重，但归一化后的最终概率仍会随钩号变化；日常阶段的小鱼会提前累计奖杯保底；大奖脱钩后的三尾补偿只会过期，不会按有效鱼逐尾消耗；海图的 14/9 确认绕过了普通钓法使用的嘬饵入口判定。文档还保留了已被二维圆形窝区替换的旧相邻格规则，PR 也缺少这类结算与迁移改动的规格记录。

## Solution

中钩确认改为严格幂等：同一尾鱼只有第一次确认会抽取保持率，后续 14/11 或 14/9 不改变中钩状态。钩型可以调整普通鱼与奖杯鱼的相对入口权重，但蓝冠的最终归一化概率在所有钩型、软保底、硬保底和脱钩补偿状态下都保持一致。奖杯保底只由聚鱼及以上阶段发鱼并结算的普通鱼累计，日常鱼不推进。大奖脱钩后的 3 尾（蓝冠为 4 尾）回访补偿会按每次有效结算逐尾递减，时限到达或次数耗尽即清空。海钓 14/9 与普通 14/11 使用同一套嘬饵、蓝冠天命保护和首次确认规则。

## User Stories

1. 作为玩家，我希望已经中钩的鱼不会因为客户端重复上报确认而重新判定或消失。
2. 作为玩家，我希望蓝冠的最终概率完全由狩猎阶段决定，不因使用小钩或大钩而暗中变化。
3. 作为玩家，我希望日常阶段不积攒奖杯保底，只有真正进入聚鱼后的连续有效渔获才推进保底。
4. 作为玩家，我希望大奖脱钩补偿明确作用于接下来 3 尾有效鱼，而不是在 10 分钟内无限生效。
5. 作为海图玩家，我希望 14/9 抬竿与普通 14/11 一样遵守嘬饵规则，同时蓝冠天命鱼仍必定中钩。

## Implementation Decisions

- `internal/gameserver/handlers_game.go`：14/9 与 14/11 共用 `hookRetentionFails` 和 `finishNibbleHook`；已经完成首次确认的同一尾鱼再次收到确认时只恢复必要状态，不重抽、不重扣饵。
- `internal/gameserver/fish_hunt.go`：钩型偏置后同比缩放 `w4`，保持最终蓝冠概率；硬保底跳过钩型偏置，保持奖杯/蓝冠比例。
- `internal/gameserver/fish_hunt.go`：饵效倍率只在已选档位的重量上下界内移动，最终档位沿用阶段抽样结果，不再按放大后的重量二次判档；fallback 只应用一次倍率。
- `internal/gameserver/fishing.go`、`fishing_persist.go`、`internal/store/repos_fishing_state.go`：待结算鱼持久化首次确认标记 `HookConfirmed` 与发鱼阶段 `HuntStage`/`HuntStageKnown`。这是现有钓鱼 JSON 快照内的兼容字段，不新增列。
- `internal/gameserver/fish_hunt.go`：`recordHuntCatch` 只对聚鱼及以上阶段的普通鱼累计 Pity；每次有效结算调用 `consumeFishHuntCompensation` 递减 CompFish。
- `internal/gameserver/handlers_game.go`：只有真正已中钩的 14/12 才记录脱钩补偿；蓝冠保护只拦截 1/2/8 钩型跑鱼帧，f0=7 无钓组显式终态直接清理。
- `docs/gameplay.md`：删除旧的 5 米相邻格说明，统一为二维圆形距离；补充确认幂等与最终蓝冠概率口径。
- 数据与迁移：本改动集已有的 `00044_groundbait_ledger.sql`、`00045_groundbait_materials.sql`、`00049_fish_hunt.sql` 建立窝料账本与 `fish_hunt_state`，默认值兼容旧档；旧测试分支的 `00046_fish_hunt.sql` 会在迁移器中重映射到 00049，避开上游 `00046_player_condition`、`00047_qualification_progress` 与 `00048_fishing_state_cast_m`。

## Testing Decisions

- 单元/行为测试：`internal/gameserver/fish_hunt_test.go` 验证蓝冠归一化概率、状态持久化、日常不累计 Pity、补偿逐尾消耗/超时失效。
- Handler 行为测试：同一文件验证重复 14/11 不会清掉已中钩鱼，海钓 14/9 遇到 nibble 不会进入 Hooked。
- Handler 行为测试：验证未中钩的未知 14/12 不产生补偿，蓝冠 f0=7 不重推并清空中钩态；纯逻辑测试验证饵效倍率不越过当前档位边界。
- 回归门禁：`go test ./... -count=1`、`go vet ./...`、`go build ./...`。
- 期望值来自规则表与持久化后的实际计数；蓝冠测试比较 `w4/(w1+w2+w3+w4)`，不再只比较原始 `w4`。
- 实机联调：真实客户端已确认首发 14/11、随后 14/9、淡水嘬饵和海钓嘬饵路径；正常 UI 不自然发送重复 14/11，精确重复帧由回归测试覆盖，Frida 托管方法二次调用被 Unity 运行时拒绝。

## Out of Scope

- 不调整四阶段的基准概率表。
- 不新增客户端协议字段或命令响应形状。
- 不改变已有鱼类体型分布、窝料配方或海图声呐显示。
- 不把正常客户端操作无法产生的重复 14/11 包装成实机验证；该路径继续由确定性测试保障。

## Further Notes

- `[事实]` 审查复现显示小钩与发窝阶段大钩的蓝冠归一化概率分别为 `0.019666457` 与 `0.020654329`。
- `[事实]` 现有 14/11 路径在重复确认时会再次抽样 `HookHoldP`，海钓 14/9 则完全跳过保持率。
- `[事实]` `recordHuntCatch` 原先对所有非奖杯鱼累计 Pity，`recordHuntEscape` 设置的 CompFish 原先没有递减点。
- `[推断]` 将发鱼阶段随鱼快照持久化即可覆盖断线恢复与跨请求结算，不需要新增玩家级临时状态。
