# 升级礼物（2~11 级直发背包，对齐官方 2023 更新；11 级按钓法分套）

Status: done

<!-- 状态取值：draft | ready | in-progress | done | cancelled -->

## Problem Statement

玩家在游戏里从 1 级升到 2~11 级时什么奖励都收不到。真实 RF4（2023 更新起）每达到
前 10 级中的每一级都会收到一份礼物，11 级为整套装备且按升级所用钓法分浮钓/路亚
两套；模拟器缺失该反馈，新手期升级没有获得感。

上游 `catch_lifecycle_regression_test.go` 曾以"已移除的奖励"口径把"升级不发金币、
不发礼物"锁死；本改动代表叉维护者决定恢复官方行为，并按官方礼物表实现。

## Solution

玩家升级（收竿结算或放生导致等级提升）后，礼物**直接发进背包**（钓鱼游装备包
同型发放），在线立即 4/22 推送。礼物是普通永久物品：不打钓鱼游临时标志、不登记
trip_grant、不回收（无使用时间限制），保留物品原生等级锁。11 级按升到该级最后
一尾鱼的钓法分套：路亚（纺车）给 Arrow S60L + Hydra Smart 2 25S，其余钓法给
Comfort Telescopic 5 浮钓套。

## User Stories

1. 作为新手玩家，我从 2 级升到 11 级的过程中，每升一级背包直接收到一份升级礼物
   （内容与官方一致），以便有成长反馈。
2. 边界：同一等级只发一次（跨结算/放生/重连/重放都不得重复）；金币不变；
   多级连升每级各发一份；礼物不走邮箱（领取邮件流程不涉及）。
3. 边界：路亚钓法升到 11 级拿到路亚套而非浮钓套；礼物物品的等级要求按物品
   定义原生生效（全部 ≤11 级可用）。

## Implementation Decisions

- 礼物内容表：`internal/gameserver/level_gifts.go` 内置，来源为官方礼物展示画面
  逐帧转录（Rutube《Подарки в РР4. За первые 11 уровней》，2023-08-02，全服统一；
  11 级路亚套来自官方论坛"Подарок за 11 уровень"截图与社区合集，item_id 已核对
  静态目录）。
- 发放机制：`prepareNewItem`/`prepareNewItemWithCount`（计件物品按官方件数精确
  构造）+ `grantPreparedItemTx` 入包，与钓鱼游 `CreateItem` 同型但不打 f12=16|256
  临时标志、不登记 trip_grant、无回收；在线会话推 4/22。
- 幂等：`kv` 键 `level_gift:<uid>:<level>` 与全部物品发放同一事务
  （`INSERT OR IGNORE` + RowsAffected 判定），先到先得，杜绝并发/重放双发。
- 触发点：两条真实升级路径的结算函数内部（回执提交之后、非重放返回之前）——
  `settleCatchOnce`（14/4 收竿结算，钓法取本尾鱼 SkillID）与 `releaseCatchOnce`
  （14/6 放生，新增 rigType 参数取 `st.RigType`，bonus>0 时）；重放路径提前返回
  天然隔离。多级连升按 from+1..to 逐级各发一份。
- 呈现层（官方"新级别礼物"卡片）：结算弹窗客户端只从响应内嵌的 **811 礼物行数组**
  取卡片渲染（`kegidigbokk(source=1)`，GameAssembly 0x180D4BD40；依据见
  monitor `docs/LEVEL_UP_GIFT_CARDS.md`）。礼物发放后按
  `args(173)[ obj(811){f0=1, f1=最终等级, f2=礼物物品(args 61), f3=空} ]`
  填 **807.f1（放流）** 与 **801.f4（收鱼结算）**；无礼物保持 null（官服未升级
  样本形态）。礼物本体同时推 4/22。客户端实测（2026-11-08）：卡片栈出现，
  物品与背包一致。
- 开关：tunable `level_gifts_enabled`（默认 1；<=0 时整体跳过，旧行为）。
- 表结构、迁移：不变（811 行复用既有 wiregen 表与 argcode 表）。

## Testing Decisions

- 测试层：存储/行为层（真实 SQLite，沿用 `newRigTestServer` 助手）+ 协议响应层
  （解码 801/807 body 断言 811 行形状与重放置空）。
- 重写 `TestCatchAndReleaseLevelUpWithoutGoldOrGiftMail` 为两个用例：
  - `TestCatchAndReleaseLevelUpGrantsOfficialGifts`：9→10 结算直发 `naiad`×15
    （金币不变、无邮件）；同一尾鱼重放不重发；放生再回 10 级不重发；关闭
    tunable 升 11 不发，重开后补发浮钓四件套。
  - `TestLevel11GiftVariesByRigType`：rigType=5（路亚族）升到 11 发
    `spin_10003_S60L`+`spin_5826`，不发浮钓竿。
  - 回滚本改动后这些测试会变红（无物品入包）。
- 门禁：`go build ./...` + `go vet ./...` + `go test ./...`，另跑 `build.ps1` 产线构建。

## Out of Scope

- 11 级以上（12+）的礼物；2~10 级礼物按钓法分套（官方画面为全服统一表）。
- 升级发金币/技能点（官方升级奖励不含金币；技能点由既有技能系统处理）。
- 邮件协议形状变更（`19/x` 维持现状）。
- 独立于结算弹窗之外的全屏礼物展示页（club 渠道）；结算弹窗内的礼物卡片栈
  已随 811 行实现。官方"升级时刻"的 807/801 报文样本仍缺，f0=1 行的 f1/f2/f3
  细节如有出入以官服抓包为准再校准。
