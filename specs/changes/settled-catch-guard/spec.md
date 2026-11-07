# 已结算渔获的终态保护：14/12 与 17/9 不得清掉待入护的鱼

Status: done

<!-- 状态取值：draft | ready | in-progress | done | cancelled
     完成时原地改这里，不要归档、不要搬家、不要另开第二份。 -->

## Problem Statement

玩家把鱼拉到岸边、结算弹窗（801）已经出现之后，在点「入护/放生」之前，鱼可能凭空消失：经验已入账，但护网没有这尾鱼、catch_log 没有记录，结算卡上的鱼无处安放。护网已满时这个窗口会停留很久（玩家要先腾位置），风险最大。

背景诊断见 `docs/analysis-fishing-chain-2026-11-07.md` H1（2026-11-07，含日志与数据库证据）。

## Solution

从「14/4 已结算」到「14/5 入护 / 14/6 放生」之间的窗口内，客户端迟到的鱼状态上报（14/12）与断件上报（17/9）一律不得改变这尾鱼的存在与归属：鱼保留，玩家始终能入护或放生。窗口之外（搏鱼中、已放生/入护后）现有行为不变。14/12 额外按请求携带的鱼 GUID 过滤跨杆的陈旧上报，使其只能作用于它所报告的那条鱼。

## User Stories

1. 作为玩家，我希望结算弹窗出现后无论发生什么（迟到的跑鱼上报、误触断件上报），这尾鱼都还在，入护/放生始终可用，以便我的渔获不丢。
2. 作为玩家，我希望护网满时鱼同样保留，等我腾出位置后仍能入护，以便不被系统偷偷吞鱼。
3. 边界：搏鱼中的跑鱼播报、断线/断竿/断钩、路亚收线中的连续咬钩，行为与现状完全一致。
4. 边界：上一杆鱼的迟到 14/12 不得影响下一杆正在搏斗的鱼（GUID 不一致即忽略）。
5. 边界：鱼已入护/放生（Resolved）后再到的 14/12/17/9 保持现有 no-op 语义，不得 panic 或误清新状态。

## Implementation Decisions

- `internal/gameserver/fishing_recovery.go`：复用既有 `completedCatchPending`（Pending && Hooked && !CastActive && !Resolved）作为守门谓词，不新增状态字段、不新增表。
- `internal/gameserver/handlers_game.go` `hFishingLeft`：持锁后在进入各 f0 分支前做两道检查，命中即记 Info 日志并原样返回：
  1. 请求 arg1 携带的鱼 GUID 非空、`st.Pending != nil` 且两者不一致 → 忽略（陈旧上报，只能作用于它报告的那条鱼）；
  2. `completedCatchPending(st)` 为真 → 忽略（已结算未处理，任何上报不得再变更）。
  f0=6（确认入组）同样受这两道检查约束；命中时跳过 PushSent 写入与 pendingTimer 取消以外的全部副作用。
- `internal/gameserver/protocol_dynamic_handlers.go` `hDynamicRodBreak`：在执行 `breakLineOrRod` 之前检查 `completedCatchPending`；命中则不跑断件事务、不动物品耐久、不清鱼，回零断件 outcome 的 11028（实现时必须用 `tools/proto.ps1` 核对该形状过契约校验；不过契约则回 `ReplyFailure`）。搏鱼中（CastActive=true）的 17/9 不受影响，现有断件裁决与持久化保持不变。
- 不改动：14/5/14/6 终态清理、`recoverCompletedCatch`、`resumeFishingState`、`scheduleBiteState`、`trip_equip` 的清鱼语义。
- 数据与迁移：无表结构变更。兼容：客户端基线保持 `4.0.25034`，协议字段无增减；回滚方式为 revert 本改动提交。

## Testing Decisions

**层**：行为级，真实 SQLite + 仓库静态道具定义，沿用现有 handler 测试接缝（参照 `catch_lifecycle_regression_test.go`、`hook_break_test.go` 的构造方式）。

用例（新增 `settled_catch_guard_test.go`）：

1. 结算后（`catch-settlement:{uid}:{fishGUID}` 回执已产生）依次发 14/12 f0=2、f0=7、f0=3：断言 `Pending/Hooked` 不变、escapes 计数不变、无播报推送；随后 14/5 入护成功，`keepnet_items` 恰好一条。
2. 结算后发 17/9（合法竿与部件 GUID）：装备耐久与线长不变，Pending 保留，14/5 成功。
3. 搏鱼中（CastActive=true）发 14/12 f0=2 与 17/9：现有语义不变——已中鱼播报并清鱼、断件持久化并清鱼。该用例由现有 `hook_break_test`、`reel_drag_break_test`、`catch_lifecycle_regression_test` 锁定，回滚本改动后它们保持绿。
4. GUID 错配：搏鱼中发 arg1 为其他鱼 GUID 的 14/12 f0=2：当前 Pending 不被清。
5. 护网满：结算 → 护网满容 → 14/12 f0=2 → 鱼保留 → 腾出容量后 14/5 成功。

**期望值来源**：直读 DB（`keepnet_items` 行数、物品耐久字段、`catch-settlement` 回执 KV），不使用实现代码反算。

**回滚后哪些测试会红**：用例 1、2、4、5。

## Out of Scope

- 断钩不清饵（H2）、`hCast` 无锁写（H3）、813.f7 银币口径（H4）——各自独立改动。
- 17/9 的跨请求幂等回执（重复 call_id 已由请求回执表覆盖，其余重复源不做处理）。
- 14/12 f0=7 语义与 `docs/gameplay.md` 的表述分歧修订。
- 恢复/重连路径（`recoverCompletedCatch`、`resumeFishingState`）的行为调整。

## Further Notes

- `[事实]` 2026-11-07 00:16:46 日志：14/11 上钩 coalfish 16787g → 35 秒搏鱼（14/8 遥测连续）→ 14/4 回 77 字节空结算；keepnet_items、catch_log、结算回执 KV 均无该鱼痕迹。
- `[事实]` 2026-11-07 00:01:55→57 日志：turbot 上钩 2 秒后第二条 14/14 推送 coalfish，客户端回 f0=6 接受；最终入库的是 coalfish（catch_log 00:02:52）。同族事故，已由 af32207 / #215 / a5f22af 修复调度侧覆盖，本 spec 处理其残余窗口。
- `[事实]` 14/12 请求 arg1 为所报告鱼的 GUID：00:01:57 的 14/12 TRACE 与该次 14/14 推送的鱼 GUID 一致（`fbdbe260…f87f`）。
- `[事实]` 现行 `hFishingLeft` 三个清鱼分支与 `hDynamicRodBreak` 均无 `completedCatchPending` 守门；该守门现仅挂 14/1、14/2（`pendingCatchAllowsCast`）与 `scheduleBiteState`。
- `[推断]` 窗口内的 14/12/17/9 只能是迟到/重复/异常上报：同一 realtime 连接 TCP 有序，14/4 之后才到达的这类上报不可能是搏鱼中的真实事件。
- `[假设]` 客户端对「零断件 11028」的处理是保留装备本地模型（依据客户端 dump：11028 移除清单驱动本地删除）——未实机验证，若实机表现不符再以抓包证据修订。
