# 面板奖杯星号按鱼种去重（307.f16 与 24/19.f5 同字段同口径）

Status: done

<!-- 状态取值：draft | ready | in-progress | done | cancelled
     完成时原地改这里，不要归档、不要搬家、不要另开第二份。 -->

## Problem Statement

等级旁 HUD 的星号数字（奖杯数）当前按「稀有渔获条数」发放（catch_log 中 grade>=3 的行数），与统计页的稀有鱼计数恰好同值。官服语义是**按鱼种去重**：某鱼种只要钓到过重量稀有（grade 3，奖杯线）或超级稀有（grade 4，蓝冠线）就计 1，同鱼种多条不重复。实机对照：测试账号两种口径分别为 27（条数）与 11（鱼种），面板星号显示的是前者，应为后者。

## Solution

- `store.CatchCountAtLeastGrade`（`COUNT(*)`）更名为 `RareSpeciesCount`，SQL 改为 `COUNT(DISTINCT fish_key)`。
- 两个发放点同步切换：307.f16（`profilebuild.go`）与 24/19.f5（`social_handlers.go`，原为全量拉取 catch_log 后内存循环计数，一并改为复用 store 函数）。
- 统计页 3/2 的逐鱼种 f9/f10/f11（总钓获/奖杯/超稀有条数）**不动**——那是另一口径，官方亦按条数。
- 实时刷新补推送：客户端唯一写星号的路径是「收到新档案」（`hncnmlgnhjm` 把 307 的 `kgpgemhhiik` 拷进 `ifbfdldbana.gebeadddmlp`），从不本地自增。14/4 结算卡 f7 的档案快照早于 catch_log 落库（鱼还在 Pending），不含本尾鱼；因此 14/5 入护后补推一次 `Push2_1()`（econ.go），14/6 放生既有 `Push2_1()` 从「奖励>0」条件放宽为所有真实放生。推送走既有 2/1 通道（args 524 包 307），会员购买/卖鱼等路径已长期使用。

## User Stories

1. 作为玩家，我希望等级旁的星号显示我钓到过稀有/超稀有重量的鱼种数，与官服一致，而不是与统计页稀有鱼计数相同的条数。

## Implementation Decisions

- 模块：`internal/store`（查询口径）+ `internal/gameserver`（两个发放点）。不改表结构、不迁移、不改协议形状。
- 两处必须同步：客户端 dump 实证 f16 与 24/19.f5 同落 `ifbfdldbana.gebeadddmlp`（offset 0x2c），是同一字段的两条服务端投递路径，口径分裂会造成面板与玩家卡数字不一致。
- 24/19 原实现全量拉取 catch_log 行再内存计数，改为 SQL 计数后顺带消除全表拉取。
- 兼容与回滚：回滚 = revert 本分支；统计页与成就口径（`achievements_rules.go` 自行读 catch_log）不受影响。

## Testing Decisions

- 扩展 `profilebuild_rare_test.go`：同一鱼种插两条 grade 3 + 其他鱼种 grade 2/3/4 + 他账号数据，断言 `RareSpeciesCount=3`（按条数会得 4，能区分新旧口径）且 307.f16=3。
- 新增 `catch_profile_push_test.go`：入护一条 grade 3 鱼后断言收到 2/1 推送且其档案 f16=1（修复前无推送，判别有效）。
- 回滚后本测试变红，属预期。

## Further Notes

- `[事实]` 客户端数据链：307/资料 profile 字段 `pjhgpkgpjih.kgpgemhhiik`（0x9c）被拷入 `ifbfdldbana.gebeadddmlp`（0x2c，hncnmlgnhjm.cs:6572）；24/19 玩家卡 DTO `dgbmlekfpbg : mgnmpeenine<ifbfdldbana>` 的序列化器把 f5 写入同一 `gebeadddmlp`；`RUIUserInfoWidget`（等级旁星号）与 `ddkpcmhdfdb.fgnijlofipp` 显示该值。
- `[事实]` grade 判定即「鱼种重量稀有度」：`rollGradeDB` 按重量对 `species_weight` 三阈值定档——trophy_g 达标线（grade 2）、trophy_line_g 奖杯/稀有线（grade 3）、super_trophy_line_g 蓝冠/超级稀有线（grade 4）。grade>=3 ⇔ 重量稀有或超级稀有。
- `[事实]` 实机对照（测试账号）：catch_log 中 grade>=3 共 27 条、去重后 11 个鱼种（其中 4 种上过蓝冠）。
- `[推断]` 官服面板星号与统计页稀有计数是两个不同的数（玩家目击），前者为鱼种数。

## Out of Scope

- 统计页 3/2 各字段的口径（按条数，维持现状）。
- 成就系统对 catch_log 的独立查询（`achievements_rules.go`）。
- 历史脏数据修正：无——口径修正即正确值，无需回填。
