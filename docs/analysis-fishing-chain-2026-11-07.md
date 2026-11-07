# 钓鱼全链路分析报告：抛竿 → 上鱼 → 搏鱼 → 入户

> 分析日期：2026-11-07
> 分析范围：本仓库 `internal/gameserver`（服务端）+ `E:\RF4\cs\il2csharp_full`（客户端 il2cpp 反编译）+ `E:\RF4\cs\ida-docs`（人工逆向笔记）
> 性质：**一次性分析快照**，非长期维护文档；行号以当日代码为准，后续提交可能漂移。
> 标注：★ = 已亲自回读源码逐条确认；其余为结构化排查结论，证据链完整但未逐行复核。

---

## 一、全流程实现总览

整体架构是**「服务端发牌 + 记账，客户端本地物理」**的混合模式。客户端反编译代码印证了服务端的这个定位是正确的：搏鱼期间的拉力、疲劳、距离演化全部在客户端本地模拟，服务端只负责派鱼（发参数）和记账（结算/入库）。

### 1.1 协议时序表（服务端实现 × 客户端 dump 印证）

| 阶段 | 命令 | 服务端实现 | 客户端侧行为（dump 证据） |
|---|---|---|---|
| 抛竿准备 | 14/1 → 回 158 | `hCastPrepare`（`handlers_game.go:755`）校验断竿/装备限制/坏轮，回深度/钩参数；先过 `pendingCatchAllowsCast` 守门 | 蓄力前调用（`deghjmihlcf.cs:2253`），参数仅竿 guid |
| 抛竿 | 14/2 → 回 800 钓组 | `hCast`（`handlers_game.go:852`）记落点/饵/竿，过水体闸门后 `scheduleBite`（`fishing.go:1509`）排咬钩 timer | `FishingSet.cs:2764` 构造 19 号抛投上下文（落点距离/深度/力度/水面块） |
| 落水/等待 | 14/3、14/7 | 14/3 记 `FallTs`；14/7 更新入水位/路亚水层/动画位 | tick 按 **≥1 秒或 ≥1 米**节流上报（`FishingSet.cs:4427-4448`），**并非每帧** |
| **上鱼** | S→C 14/14 | `onBiteTimerTicket`（`fishing.go:1576`）持锁判鱼：名册权重→后台覆盖→禁饵过滤→分布→生态→饵效 tier→体重对数正态采样；生成 `RolledFish` 后 push obj27（体力池 f5、初始体力比 f6、拉力 f8、搏鱼静态参数 f9-f20、搏鱼曲线 f21） | 收到后回 **14/12 f0=6** 确认入组，本地协程生成鱼实体 AI（`Fish.cs:742`）；解析不到钓组回 f0=3 拒收 |
| 刺鱼 | 14/11 | `hFishingHooked`（`handlers_game.go:1205`）置 `Hooked=true`，按 FishGUID 幂等扣饵（`bait_consumption.go:519-542`） | 鱼实体进搏鱼态时发送（`FishingSet.cs:2080`），210 号体含咬钩延迟/时刻 |
| **搏鱼** | 14/8 / 14/9 / 14/7 | **不模拟**拉力/疲劳/距离，只做簿记（`Hooked/Pending/PushSent`）；拉力、体力、搏鱼曲线在 14/14 推鱼那一刻一次性算好下发（`fishing.go:1323-1358`、`serverdb.go:96-153`） | 搏鱼物理全在客户端：每帧算线载（`FishingSet.cs:5851`），组合钓力 = min(竿, 线, 前导, 刹车)（`ida-docs/fields/_ROD_LINE_PHYSICS.md`） |
| 断件 | 17/9 → 回 11028 | `hDynamicRodBreak`（`protocol_dynamic_handlers.go:102`）：客户端报"已超载"，**断哪件由服务端** `resolveGearBreak`（`gear_damage.go:494-640`）按"当前耐久下真实强度取最小值"重裁决并单事务持久化 | 上报体 `dboabijkbif` 含断裂力/线载/泄力/线长；判定完全本地（`effjdeblglh.cs:5407` 递归遍历钓组节点） |
| 收竿 | 14/4 → 回 801 | `fishingEndSettle`（`progress_ops.go:321`）：`settleCatchOnce`（`catch_settlement.go:110`）一次性结算 XP/磨损，回执键 `catch-settlement:{uid}:{fishGUID}` 防重放；813 统计块内嵌 801.f0 | 801 驱动渔获弹窗；**空竿 f0 必须空数组**（`ida-docs/object_chains/fishing_caagocpofin.md`） |
| **入户** | 14/5 / 14/6 | `fishingKeep`（`econ.go:247`）：GUID 一致性校验 → `KeepFishingCatchWithCapacity`（`repos_fishing_keep.go:20-87`）单事务写 keepnet_items + catch_log + 清 pending；放生走 `releaseCatchOnce`（`catch_release.go:17-84`，幂等键 `catch-release:*`） | 入护/放生按钮分别触发，回 800 内嵌 25 号鱼条目 / 回 807 档案 |
| 剪线 | 17/10 | `hRodCutLine`（`protocol_dynamic_handlers.go:240`）按放出米数扣主线、级联清下游件、显式终态清 FishState | `cnefjpnennb.opblbpnbpmo(int 线长)` |
| 恢复 | — | `fishing_persist.go` 快照（Unlock 时比对落库）+ `resumeFishingState` 重推**同一尾**；已结算未处理的鱼由 `recoverCompletedCatchOnLogin`（`catch_recovery.go:16-29`）自动入护 | — |

### 1.2 「同一尾鱼」红线的实现手段

1. **指针传递**：`rollFishForRig` 生成 `RolledFish`（含 `FishGUID`）→ `st.Pending` 同指针 → 14/14 推同一指针 → 14/4 结算 `st.Pending` → 14/5 `kept := *pending` → keepnet `EchoGUID=fishGUID`。
2. **防串杆**：`castGeneration`（换杆作废旧回调）+ `biteTicket`/`pendingTicket`（同杆重排也作废）+ `pendingTimer` 捕获鱼指针再校验，回归测试锁定在 `fishing_timer_test.go:25-115`。
3. **防重推**：`pushing` 在途互斥 + `claimFishPush` 会话级 5 秒去重 + `maxPendingRepush=2` 放弃机制。
4. **防抢跑**：`pendingCatchAllowsCast`（`fishing_recovery.go:12-28`）挂在 14/1、14/2，已结算未处理的鱼禁止重新抛竿。
5. **幂等**：结算/放生/扣饵全部有回执键（fishGUID 维度），重放还原事实不重复入账。

**结论：骨架设计扎实，客户端 dump 在主干协议（14/1..14/12、17/9/10、801/807/800、27/26 对象）上与服务端互相印证，无明显断裂。**

---

## 二、问题清单

### 2.1 高严重度

#### H1 ★ 结算后→入护前的"幽灵鱼"窗口

- **位置**：`handlers_game.go:1293-1343`（14/12 两个清鱼分支）、`protocol_dynamic_handlers.go:126-134`（17/9）
- **现象**：`completedCatchPending` 守门（`fishing_recovery.go:8-10`）只挂在 14/1、14/2 上。14/4 已结算（XP 已入账）但 14/5/14/6 未到期间，任何一条迟到的 14/12（f0=1/2/8 或 default 分支）或 17/9 都会**无条件 `st.Pending = nil`**——此后 XP 已加、catch_log 没写、护网无条目、内存态已清，重启恢复（`recoverCompletedCatchOnLogin` 依赖 `Pending != nil`）也救不回。
- **触发概率**：正常客户端流程中搏鱼已结束、鱼已上岸，不应再发 14/12/17/9；但异常/迟到/重放包可触发。
- **建议**：给 `hFishingLeft` 的清鱼分支和 `hDynamicRodBreak` 补 `completedCatchPending` 守门（命中则保留 Pending 并跳过清场）。改动极小。

#### H2 ★ 断钩后饵料残留 → 钓组永久空口

- **位置**：`gear_damage.go:395-490`（`breakComponentTx`）vs `bait_consumption.go:222-235`（`applyBaitedHookSpec`）
- **现象**：钩耐久归零退回背包时，**不清理** `bait_hook:<饵>` 的 rod_setting 映射，也不摘挂在该钩上的饵组件。下次抛竿 `baitedHookSets` 仍组出"饵→死钩"的组，`applyBaitedHookSpec` 因 `durability <= 0` 恒 false → 单钩钓组全组失败 → `rollFishForRig` 永远不派鱼。玩家换上新钩也无效（旧映射仍指向死钩），只能手动拆饵重挂才能恢复，且无任何提示。
- **对照**：断线路径（`downstreamLossCategories`）会级联清下游件，断钩路径不对称。
- **建议**：`breakComponentTx` 在钩件断裂时同事务清理其 `bait_hook:*` 映射并摘除该钩上的饵组件（或转挂到存活钩）。

#### H3 ★ 14/2 无锁写共享状态

- **位置**：`handlers_game.go:869-876`
- **现象**：`st.Loc = loc`、`st.Spot = spot` 在 `st.mu.Lock()`（878 行）**之外**写入；咬钩 timer 回调持锁读 `st.Loc` 判鱼（`fishing.go:1665-1687`）、持久化快照并发读——真数据竞态，`-race` 必报，快照可能存进撕裂状态。
- **建议**：这两行挪进 878 行的锁内。

#### H4 ★ 银币两处口径差 28 倍

- **位置**：`econ.go:35-50`（`catchValue`）vs `progress_ops.go:257-264`（`buildSettle813`）
- **现象**：`catchValue` 的 silver = `rate×w/silverRateSeedDivisor(28)`（`econ_market.go:50`，与鱼市卖价一致，入 catch_log）；而 813.f7 的 `silverCents = round(rate×w)` **不除 28**。结算卡显示价与实际卖鱼所得差 28 倍（若客户端把 f7 当"分"÷100 也差 2.8 倍）。
- **影响**：silver 只展示不入账，不是刷钱漏洞，但展示口径明显错。
- **建议**：先抓一次真机结算报文确认客户端对 813.f7 的解读（银币还是分），再统一公式。

### 2.2 中严重度

#### M1 ★ 与客户端实测节流不符：14/7 不是每帧

- **位置**：服务端 `handlers_game.go:1148` 注释 vs `FishingSet.cs:4427-4448`
- **现象**：客户端 tick 按 ≥1 秒或 ≥1 米节流。路亚动作识别（`LureMotionMask` 采样）实际只有 ~1Hz，`jig/顺流` 状态判定天然滞后约 1 秒——功能可用但节奏偏粗，注释与性能预估都错。

#### M2 空 GearGuid 时 14/14 发全零 GUID → 必然进入重推循环

- **位置**：`fishing.go:1446-1448`
- **现象**：客户端解析不到钓组会回 14/12 f0=3（`deghjmihlcf.cs:4870-4971`），服务端按拒收 6 秒重推 ≤2 次后放弃——这条鱼就丢了。
- **建议**：顺延到取竿后再推，而不是发全零 GUID。

#### M3 ★ 判鱼全程持玩家锁做十余次 DB 查询

- **位置**：`fishing.go:1650,1683-1688`
- **现象**：`onBiteTimerTicket` 在 `st.mu` 内执行装备归属、饵组、名册、档位线、地点解析等 DB 查询；同一玩家的 14/7/14/4/14/12 全部阻塞在这把锁上，且 Unlock 后还要同步 `json.Marshal` 整个状态写库（`fishing_persist.go:29-49`）——写放大 + 并发 Unlock 时快照可能乱序覆盖。
- **建议**：判鱼改为先快照、移出锁外；持久化快照加序号防乱序。

#### M4 双 14/4 结算实现并存（死代码）

- **位置**：`econ.go:68-201`（`fishingEnd`/`build813`/`grantXp`/`awardFishingProficiency`）vs `progress_ops.go:321`（现行 `fishingEndSettle`）
- **现象**：旧实现已无任何注册调用（14/4 只走 `fishingEndSettle`），但留着且口径不同（无加成回执、无防重放），极易被误用。`wearRigForCatchPersisted`（`progress_ops.go:92`）同样无人调用。
- **建议**：删除或显式标注废弃。

#### M5 其余中等问题（证据链完整，未逐行复核）

| 问题 | 位置 |
|---|---|
| `fishingEndSettle` 只 `Stop()` 不清 `biteTimer` 指针/ticket，应改用 `cancelBiteTimerLocked` | `progress_ops.go:330-332` |
| 14/7 handler 解锁后读 `LureAction/LureLayer` 并调 `effectiveBaitDepthCMLocked`（race detector 必报） | `handlers_game.go:1145-1152` |
| 水体闸门用玩家站位（3/9）而非抛竿落点（同函数已存 `CastPosX/Z` 却不用） | `handlers_game.go:880 vs 945-953` |
| `KeepFishingCatchWithCapacity` 的 `kept` 返回值在已存在时也返回 true，重启并发下内存缓存可能重复 | `repos_fishing_keep.go:30-37` |
| 14/12 f0=3/5 无条件清 `Hooked`，搏鱼中若到达会降级为未挂钩 → 14/4 误报"未刺中收竿" | `handlers_game.go:1278` |
| 跳过 14/4 直接 14/5 会以 XP=0 写 catch_log 且永远没有结算回执 | `econ.go:285-300` |
| `fishStateMutex.Unlock()` 在释放主锁后才 `SaveFishingState`，两个并发保存可乱序覆盖；14/7 每帧动画位变化全量 JSON 写库 | `fishing_persist.go:29-49` |
| `tunableF` 持全局 `s.tunMu` 同步查 DB；`nextCallID` 每次推送（含每秒 2/2 保活）做一次 KVSet | `fishing.go:436-465`、`session.go:197-206` |

### 2.3 低严重度 / 待观察

| 问题 | 位置 |
|---|---|
| 概率门在规则开关开启后硬封顶 0.95，管理员设 `bite_probability=1.0` 不生效 | `fishing.go:1666-1668` |
| DB 加载钓鱼状态失败静默落到硬编码 `loc1_02/worm_01`（错误地图判鱼，仅 Warn） | `fishing_persist.go:54-58` |
| 14/7 用"第一个非空 STR"当 Loc，纯信任 wire 字段顺序 | `handlers_game.go:1098-1104` |
| 17/9 无"正在搏鱼"前置校验，任何时刻可上报断件（自伤型接口） | `protocol_dynamic_handlers.go:102-145` |
| 813.f4 随机缩放不进回执，重放显示值漂移（纯显示） | `progress_ops.go:256` |
| 17/8 rod/put（竿放竿架）无 handler，客户端竿架交互可能拿族兜底响应 | `rigops.go:2380-2386` 注册表缺 17/8 |
| `onBiteTimer`/`repushPending14_14` 生产无调用（仅测试），`onBiteTimer` 形态绕过 AfterFunc 捕获语义，易误用 | `fishing.go:1568-1574,1709-1720` |
| 14/9 兼作海钓刺鱼确认是自证行为，真服抓包未覆盖（`marine_hook_confirm`/`marine_hook_item_sync` 默认关闭是对的） | `handlers_game.go:1179-1198` |

### 2.4 一处需要修正认知的分歧（建议改文档，不改代码）

服务端注释把 f0=7 当"真脱钩"，default 分支（含 f0=7）一律结束本杆，与 `gameplay.md:47`"脱钩后保留本杆重新排鱼"有张力。但客户端 dump 显示 **f0=7 的语义是"无钓组"**（`naihiiaooka.bachphcpjff` 状态机：无持竿者报 3、无钓组报 7）——钓组都没了，结束本杆是正确行为。真正搏鱼中跑鱼走 f0=1/2/8（2026-11-06 线上实证，全程无 8/7），服务端已按"服务端记的 14/11 中鱼确认"为准处理。**建议修订 `gameplay.md` 与代码注释，而非改代码。**

---

## 三、总结与修复优先级

**主干是健康的**：服务端"发牌+记账"定位与客户端反编译互相印证；防串杆/防重推/幂等结算/同一尾鱼指针传递这套骨架有回归测试锁定；obj27 字段语义（体力 f5、拉力 f8、曲线 f21）两端一致。

**建议修复顺序**：

| 优先级 | 问题 | 理由 |
|---|---|---|
| 1 | H2 断钩不清饵 | 确定性玩法损坏，玩家可稳定复现"换钩后永远没口"，且无提示 |
| 2 | H1 幽灵鱼窗口 | 给 14/12/17/9 补 `completedCatchPending` 守门，改动极小，堵住红线漏洞 |
| 3 | H3 无锁写 | 一行挪进锁内，消除数据竞态 |
| 4 | M3 判鱼持锁 DB 查询 | 性能与一致性双收益，但改动面大，需回归验证 |
| 5 | H4 银币口径 | 需先抓真机报文确认 813.f7 语义再动 |
| 6 | M2 空 GUID 重推循环、M4 死代码、M1 注释修正 | 低风险清理 |

按仓库约束，上述 H1-H3 若要动手，应走 `specs/changes/<slug>/spec.md` 快速路径建 spec 后实施；约束文档本身的修订（如 2.4 节的文档分歧）走约束路径并需维护者评审。
