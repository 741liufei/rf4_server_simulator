# 卡片出售与卡册塑封

Status: done

## Problem Statement

27/1 出售与 27/4 塑封此前返回失败桩（"卡片覆膜和卡册奖励暂未开放"）：玩家无法出售重复卡变现，集齐卡册后没有塑封封存与奖座奖励。另外掉落卡走单条 4/22 增量推送，开着的卡牌列表不随之刷新。

## Solution

补齐 27/1（只卖散卡、以 def 售价入账、回执幂等）与 27/4（集齐校验、FILETIME 密封时间戳、pnaapkplkfo 奖座幂等补发、塑封后冻结），并把掉落/出售/塑封后的客户端同步改为收藏册全量先删后推两代重同步。

## User Stories

1. 玩家多张重复卡出售换银，成交价以服务端 def 为准，重试不重复入账。
2. 玩家集齐卡册后塑封：界面转为已塑封渲染（f0 密封快照 + f1 时间戳），弹窗展示奖座，奖座进背包且不重复发放。
3. 塑封后的卡册冻结：入册、取回与重复塑封均被服务端拒绝。
4. 钓获掉卡后无需重开界面，卡牌列表即可见新卡。

## Implementation Decisions

- 在 gameserver 以 hTradingCardSell/hTradingCardLaminate 替换 27/1、27/4 桩；27/1-3 契约 800，27/4 契约 809（llgjfoldbcc）。
- [事实] 27/1 参数 [GuidArr 卡实例, U64 客户端报价]（capstone send 调用点 mov r9b,0x1b + sub=1，0x603d9c）；27/4 参数 [Guid 卡册]（lkokodffmln，0x1805f994d mov byte[rsp+0x20],4）。27/4 响应 wire 类 gglppphenoi/809 = pcnefbpgdhl 派生加 f0 plhpmhpadcg[]，解码序 (描述[], 物品树[], 物品树[], Guid[])；奖励弹窗取 f1 物品树，响应先经 eiihgcpdpbb 应用再回调（2026-11-08 反编译+实机）。
- [事实] 卡册 f1 密封时间戳按 FILETIME 渲染：发 .NET Ticks（0001 纪元）会被 FromFileTime 多算 1600 年，实机显示 3626/11/8。塑封时间 = proto.WindowsFiletimeNow()。
- 出售只接受收藏册容器（native:18）中未入册的散卡；已入册卡（container=card_deck）须先取回，与客户端入口一致。售价取 def 属性 obcephkkfhi，客户端报价仅对账；删除与入账同事务，回执键 card-sell:<uid>:<token>:<main>:<sub>:<call>，同 call 重放返回缓存 ACK。
- 塑封校验 f0 内嵌卡覆盖 def cjmjfnheidh 全部物种；奖座 def 属性 pnaapkplkfo 已有则不重发，ACK 恒带全部奖座树供弹窗展示。奖座构造在事务外（静态库查询不进 SQLite 经济事务），事务内仅 INSERT 与密封字段更新。塑封守卫挂在 changeTradingCardAlbum：f1>0 时入册/取回均拒绝。
- [事实] 客户端刷新（2026-11-08 实机）：4/22 upsert 不替换本地已有物品对象；卡牌列表视图重建滞后一个事件代且同帧推送被合并为一次重建。故同步通道 = 4/21 全量收藏册 guid + 4/22 全量树，600ms 后跨帧再发一代（resyncTradingCardsTwice）；出售另发已售 guid 的 4/21 与 2/1 余额。
- 客户端基线 4.0.25034；实机验证出售入账/塑封渲染与奖座/掉卡即时可见（2026-11-08）。
