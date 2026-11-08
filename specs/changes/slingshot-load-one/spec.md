# 弹弓抛饵只装 1 颗 + 抛后自动补填

Status: in-progress

<!-- 状态取值：draft | ready | in-progress | done | cancelled
     代码与门禁已完成，待少爷实机确认画面后原地改 done。 -->

## Problem Statement

用弹弓（wh_pc_gb）抛窝料时，游戏画面一次飞出一大堆饵团（数量约等于背包里这袋饵的总数），
但背包真实数量每次只减 1。玩家看到的是「抛一颗撒了一大把」，与官服「一次一颗」的画面不符，
也无法按落点精确做窝。

## Solution

弹弓弹药格只装 1 颗 ready 球，其余留在背包。每抛一颗，服务端自动从背包同种饵补 1 颗进弹药格。
画面变成一次飞出 1 颗、背包逐颗减、可以连续抛；背包没料时弹药格清空，需要玩家重新装填。

## User Stories

1. 作为钓手，我希望弹弓一次只抛出 1 颗饵团，以便按落点精确做窝、画面与官服一致。
2. 作为钓手，我希望抛完一颗后弹药格自动补好下一颗，以便按住连抛不用手动反复装填。
3. 边界：背包同种饵用尽后，最后一颗抛完弹药格清空（不凭空生成饵）；换装另一种饵时旧饵退回背包。
4. 边界：客户端表现若变差，运维可把 `handitem_load_one` 调回 0 原样退回整堆装填，无需重启。

## Implementation Decisions

- 模块 `internal/gameserver`：
  - `rigops.go` `materializeHanditemChild`：缺省只搬 1 份进新子实例（`handitem_load_one=1`），
    来源堆数量减 1 后留在背包；仅当整堆搬走（开关 0，或来源堆只剩 1 份）才删除来源堆。
    子实例不写来源堆引用（f2），换弹时旧子实例仍按普通物品退回背包，不被 `refundableSplitChild` 回收。
  - `rigops.go` `rigSetComponent` 的 `HanditemFullStack` 分支：只要生成了新子实例就标 `materialized`
    （进快照 f0，客户端才建立父子归属）；来源堆留背包时经 `res.SourceGUID/SourceTree` 暴露减 1 后的新树。
  - `protocol_dynamic_handlers.go` `consumeHanditemLoadedForThrow`：用尽后调新增的
    `reloadHanditemAfterThrow`，按 item_id 找背包最早的同种来源堆，复用 `rigSetComponent` 装填 wiring
    补 1 颗；新子实例进 805.f3、用尽的旧子实例进 f5。
- 命令 / 字段：不改响应形状。`tools/proto.ps1 class lmbomkliaad` 确认 805 仍是
  f0 距离 / f1 散射 / f2 姿态块 / f3 物品实例数组 / f4 抛出物数组 / f5 Guid 移除数组；
  本次只是 f3 里多 carry 新补的子实例与来源堆树、f5 多 carry 用尽的旧子实例。13/1 仍回 800 三段增量。
- 数据与迁移：不改表、不需要旧库升级。只改写玩家库 item 行的数量与父子引用。
- 兼容：客户端基线 4.0.25034。旧档里已整堆装填的弹弓（子实例 qty>1）抛投时仍按旧逻辑逐份扣减，
  用尽后才进入补填；不强制迁移。回滚：`handitem_load_one=0`（30s 缓存，免重启）。

## Testing Decisions

- 测哪一层：行为 + 存储（真实 SQLite，`newRigTestServer`）+ 协议编解码回环（`throwOnce` 解码 805
  并过 `checkRegisteredResponse(13,5)` 合同守卫）。接缝复用现有 `hHanditemSetComp` / `hDynamicHanditemThrow`。
- 期望值来自哪：手算样例（5 颗堆装填后背包剩 4、连抛 4 颗背包 4→3→2→1→0、第 5 抛清空），
  以及反编译/Frida 证实的「飞行球数=装填数量」因果（见 Further Notes），不是用同样代码再算一遍。
- 新增 `handitem_load_one_test.go`：
  - `TestHanditemLoadOneAndAutoReload`：装 1 剩 4、连抛自动补 1、用尽清空、新子实例进 f3 / 旧子实例进 f5。
  - `TestHanditemLoadOneDisabledKeepsWholeStack`：开关 0 回退整堆装填、抛一颗扣 1 不补填。
- 回滚后变红：把 `materializeHanditemChild` 改回整堆、或删掉 `reloadHanditemAfterThrow`，
  `TestHanditemLoadOneAndAutoReload` 立即变红。既有 `TestHanditemSwitch*` / `TestHanditemThrow*` 不改自绿。

## Out of Scope

- 抛投方向/落点坐标（805 无独立坐标字段，另见 `docs/slingshot-throw-visual.md`，本次不动）。
- 散射百分比、姿态块、抛出物实例开关（沿用既有 tunable，不改缺省）。
- 钓竿挂饵 / 自动补饵链路（`splitMountableChild` / `reloadBaitForCast` 不动）。
- 装填时背包计数的主通道刷新：来源堆减 1 后的新树已进 800.f1 与 4/22 推送；客户端若忽略同 guid
  数量更新，紧随抛投的 13/6 全量快照会兜底对齐。未单独加「移除+重加」以免改动既有换弹测试锁定的推送时序。

## Further Notes

证据与来源：

- [事实] 客户端反编译中的 Slingshot_spawn_loop（RVA 0xdf2db0）：
  刷新皮兜时 `count = vcall(item,+0x338)`（取数量），`while(count>i){ FUN_1809fefb0(item,0); List_Add; i++; }`
  按数量逐颗建 FeedLoad 进皮兜 List。
- [事实] 客户端反编译中的 Slingshot_throw_loop（RVA 0xdf5230）：
  抛投遍历 `param_1+0x38`（皮兜 List）每一颗调 `FUN_1809fdc70`（FeedLoad_method_805，RVA 0x9fdc70，
  内部 `FUN_1809fefb0` 造一颗并发射）。故飞行球数 = 装填数量。
- [事实] Frida 实机探针：装填后每次抛投触发 FEEDLOAD_FACTORY×55–57
  （≈装填数 55），调用栈 `0x9fe480 <- 0xdf5638 <- 0x75de64 ...`，与「按装填数逐颗抛」吻合。
- [事实] 运行库检查：弹弓弹药格子实例 qty=55（rig），背包 feed_dry qty=59；
  整堆装填把 55 颗搬上钩 → 一次抛 55 颗、只扣 1，正是玩家所见「一大堆」。
- [事实] `tools/proto.ps1 class lmbomkliaad`：805 字段形状如上，本次未改形状；回归测试过合同守卫。
- [推断] 官服弹弓 likewise 只装少量 ready 球、抛后补填（玩家描述官服一次一颗）；未抓官服报文，
  以反编译客户端渲染逻辑为准。
