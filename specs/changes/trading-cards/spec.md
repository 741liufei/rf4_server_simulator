# 原生卡片与卡册物品

Status: done

## Problem Statement

已有静态定义包含卡片与卡册，但卡册没有对应的物品类型映射，卡片实例也缺少钓获鱼资料。

## Solution

补全原生卡片与卡册物品结构，背包和个人资料读取同一份收藏。卡片由正常钓获产生，后台不提供补录页面或接口。

## User Stories

- 玩家钓到的卡片作为独立物品保存，重新登录后仍能读取。
- 游戏个人资料从已保存的卡册物品读取已收集项。

## Implementation Decisions

- 只读 item_def 的 category 78/82，不复制专有配置，不写静态库，不新增玩家表。
- 卡片为 11301/dapkdcghfmg，f0 是钓获鱼 25/ecknbkpeeji；卡册为 11304/ckfmndabaei，f0 为实例数组 code 362；个人资料 2003.f9 使用卡册实例数组 code 364。编号来自客户端真实类型注册表，见 trading-card-array-types/spec.md。
- 卡片与卡册为独立实例；旧的空卡片载荷补齐普通鱼资料，空卡册始终发送有类型的空数组。
- 沿用现有物品创建、序列化、背包和个人资料命令，不添加卡片管理工具。

## Testing Decisions

- 临时库验证原生卡片与空卡册创建，编码往返、数组类型及个人资料读取。
- Go test/vet/build 通过。客户端 4.0.25034 已由玩家确认收藏显示、已有卡位、取回原卡、再次入册及重新登录均正常；实现与验收边界见 trading-card-albums/spec.md。

## Out of Scope

不做卡片补录。卡册奖励、交易、覆膜和资格任务暂不实现。
