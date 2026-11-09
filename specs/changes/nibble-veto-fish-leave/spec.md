# nibble 否决时向客户端推送鱼离场（14/15）

Status: in-progress

<!-- 实现+门禁已完成（2026-11-09），待用户实机回归路亚嘬饵表现后改 done。 -->

## Problem Statement

路亚/海钓嘬饵（服务端预掷 `BiteQuality=nibble`）被服务端否决时，客户端对整个否决过程
一无所知：14/11 的客户端语义是"鱼实体已进搏鱼态"（`FishingSet.cs:2080`），客户端此后
持续本地搏鱼，玩家把鱼拖到岸边收竿（14/4）时服务端已无此鱼，只能回空 801——鱼在玩家
眼前凭空消失，或被误挂到重排后的新鱼头上补一条"咬钩未上钩"跑鱼提示。2026-11-08 晚
kirzah 河会话实证两次：dace 130g（23:24:48 否决 → 23:25:01 收竿落空）、wa_bream 180g
（23:26:39 否决 → 23:26:45 空 801）。

## Solution

服务端在 nibble 否决瞬间向客户端推送一条"鱼离场"消息（14/15，args=[竿GUID, 鱼GUID]），
客户端将其映射到与自然脱钩完全相同的离场状态机：鱼立即脱钩游走（自然嘬饵同款动画），
不产生幽灵搏鱼，之后照常接下一条 14/14。

## User Stories

1. 作为路亚玩家，当我刺中一尾被判嘬饵的鱼时，我希望鱼立刻脱钩游走（和自然嘬饵一样），
   以便不再出现拖到岸边凭空消失的"幽灵鱼"。
2. 边界：客户端若尚未接受鱼实体（此前上报过 f0=3/5 拒收），离场推送是无效查找，无副作用。
3. 边界：否决后客户端回 14/12 f0=5 离场回执，服务端此时已无在途鱼——不得清掉刚重排的
   咬钩 timer，也不得把本竿标记为终局（Resolved）。

## Implementation Decisions

- 模块：`internal/gameserver`
- `finishNibbleHook`（14/11 与 14/9 两条嘬饵路径共用）：清状态前推送
  `Session.Push14_15(gear, pending.FishGUID)`；nibble 状态转移、扣饵、重排时序不变。
- `hFishingLeft` 的 f0=3/5 分支：`st.Pending == nil` 时按良性回执早退（只记日志）；
  `Pending != nil` 的"拒收重推"语义原样保留。
- `Push14_15`/`marineHookConfirm` 注释纠偏为客户端实证语义（14/15 = 鱼离场推送；
  命令 14 分发器还处理 2/14，其它未定义子号会报错）。
- 数据与迁移：无表变更、无迁移。
- 兼容：推送形状与既有 `Push14_15` 完全一致（契约校验已覆盖），客户端版本基线
  4.0.25034 不受影响；回滚 = revert 两处代码改动，无状态残留。

## Testing Decisions

- 层次：行为测试（真实回环隧道 `newTunnelProbe` + `awaitPushEnvelope`），
  复用 `fishing_notify_test.go` / `fish_hunt_test.go` 既有接缝。
- 新增：
  - `TestNibbleVetoPushesFishLeave`：nibble 鱼 + 14/11 → probe 收到 (14,15,[竿,鱼GUID])，
    原状态断言（Pending nil / !Hooked / biteTimer armed）保持；
  - `TestMarineNibbleVetoPushesFishLeave`：14/9 路径同断言；
  - `TestNibbleVetoLeaveAckBenign`：否决后喂 14/12 f0=5 → biteTimer 仍在、
    Resolved==false、无任何推送。
- 期望值来源：推送命令号/参数顺序按客户端反编译分发器（gdpondeemjf.cs 分支C）逐字对照，
  不是"用同样代码再算一遍"。
- 回滚后变红的测试：上面三条新测试。

## Out of Scope

- `hookSolidHold` 小鱼端概率参数重标定（等官服对照样本，另行处理）。
- 否决时发聊天跑鱼提示（与自然嘬饵离场的官方静默保持一致；将来要加是
  `notifyFishEscape` 一行）。
- `marine_hook_confirm` 行为变更（当前默认关；只纠注释并加警告，不改行为）。
- 14/4 未刺中收竿路径（progress_ops.go）不加离场推送——咬饵未刺中的鱼客户端
  本就未进搏鱼态，保持现状。

## Further Notes

- `[事实]` 客户端命令 14 分发器显式处理子号 2、14、15；其中 14/15 进入"鱼离场"分支：
  `gdpondeemjf.pagenilfpij`（gdpondeemjf.cs:22-103），`iebkfdalalb.ncnnmbljdmd`
  取 `args[1]` 为鱼 GUID → `deghjmihlcf.aglphhgicmi` → `Fish.lennnlocboa()`
  （Fish.cs:2046-2054，状态 1/2 → 4）。本次会话已人工复读核对；其它子号会抛
  `Unknown command`，不能概括为统一的离场语义。
- `[事实]` 鱼进入离场状态机后回 14/12 f0=5 并清竿→鱼映射：
  `FishingSet.hmkikjmbnop`（FishingSet.cs:5733-5754）、`hnffdebalii.geoplfemoon`
  （hnffdebalii.cs:58-79）、`deghjmihlcf.hiahggbbadk`（deghjmihlcf.cs:1501-1516）。
- `[事实]` 14/11 客户端语义"鱼实体进搏鱼态时发送"：docs/analysis-fishing-chain-2026-11-07.md:22。
- `[事实]` 模拟器旧注释"14/15=海钓挂钩确认"为误诊：14/15 在统一命令 14 分发器中走
  分支C，海钓与非海钓均适用；`marine_hook_confirm` 实测验证不在本改动范围。
- `[未验证]` 仍需实机确认 14/15 后客户端回 14/12 f0=5 且新咬钩 timer 不受影响。
