# 修复多竿独立钓鱼状态

Status: implemented; partial client validation

## Problem

多根鱼竿共用钓鱼状态，后抛竿可能覆盖其他竿的计时器和待发鱼；读取未变化的鱼竿也会刷新版本，放竿或切竿可能重建客户端的水中装备。一次空口还会使仍在水中的抛投停止等待。

## Solution

状态按玩家和鱼竿 GUID 保存；计时回调捕获原状态。请求按竿或鱼 GUID 路由，后台发鱼不切换手持竿。00041 迁移将快照键改为 `(uid, rod_guid)` 并保留旧行。放竿保留原鱼和计时器，只读鱼竿不推进物品版本；有效钓组空口后继续独立排期。已收鱼未入护或放生时仍阻止新抛竿。

## User Stories

- 三根鱼竿独立等待和搏鱼，切换手持竿不覆盖其他竿的鱼。
- 放下已中鱼的竿后保留原鱼，收鱼和装备处理只影响对应鱼竿。
- 重启后恢复各竿快照，保留旧存档已有记录。

## Implementation Decisions

- 兼容客户端版本为 `4.0.25034`，复用现有响应形状和命令字段。
- 只写玩家库，静态配置库只读；迁移可重复执行，失败时回滚。
- 17/8 rod/put 为携带竿 GUID 的无响应请求，显式处理并保留水中状态。
- 物品版本由真实装配和耐久修改推进，读取不触发客户端装备重建。
- 空口仅结束本次判定；仍入水、有饵、装备可用且未中鱼的钓组继续等待，不改变单次判鱼权重。

## Testing Decisions

`TestThreeFishingRodsTimersEventsAndRestore` 覆盖独立计时、跨手持竿收鱼与恢复；`TestFishingRodsUpgradeAndRollback` 覆盖旧库升级和迁移回滚。

`TestRodReadKeepsVersionUntilEquipmentChanges`、`TestPutAndTakePreserveThreeHookedRods` 和 `TestEmptyRollKeepsOtherRodWaiting` 覆盖版本稳定、真实形状请求回环和空口独立重排。

独立分支的完整 Go test/vet/build 已通过。包含相同多竿修复的集成构建中，客户端已确认放竿保鱼、两竿同时有鱼；三竿同时实钓与客户端重登恢复尚未确认，不能用自动测试代替这些结果。

## Out of Scope

不修改客户端报文格式、鱼种权重、咬钩概率或经济奖励。
