# 弹弓抛饵视觉与散布参数

Status: done

## Problem Statement

13/5 的 805 响应需要同时满足客户端姿态、物品增量和可选飞行饵渲染。

额外抛出物实例在客户端通常已经自行绘制，服务端重复下发会出现两份饵团。

## Solution

805 保持姿态块和物品变化，额外抛出物实例改为显式开启；散布参数进入 tunable。

## User Stories

1. 作为玩家，我希望每次抛饵只出现一份飞行饵。

2. 作为玩家，我希望改变散布参数后行为稳定可复现。

3. 边界：显式开启额外抛出物时，自清开关决定是否在 f5 移除临时实例。

## Implementation Decisions

- 模块：`internal/gameserver` 的动态协议处理。

- 协议：805 的 f0 距离、f1 散布、f2 姿态、f3 更新、f4 可选抛出物、f5 移除。

- 缺省：`handitem_throw_scatter=5.0`、`handitem_throw_projectile=0`、`handitem_throw_selfclean=0`。

- 不新增落点或朝向字段。

## Testing Decisions

- 协议单测覆盖完整 805 字段、散布边界和抛出物开关。

- 回滚后失败项：散布参数断言和缺省无额外抛出物断言。

## Out of Scope

- 不修改客户端飞行方向计算。

- 不实现客户端补丁。
