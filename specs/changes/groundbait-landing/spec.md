# 窝料落点三档与弹弓记账

Status: done

## Problem Statement

弹弓和手抛只上报距离，没有世界坐标；落点未知时若直接丢弃，玩家先打窝再抛竿会等于白打。
相邻 5 米格也容易把同一团窝切在边界两侧。跨登录复用 CallID 还可能让弹弓窝账撞旧幂等键。

## Solution

落点按可信度分三档：浮标落点全额生效；没有浮标时使用玩家坐标；都没有时按整个钓点记模糊窝。
邻格与模糊窝按衰减系数计入，弹弓幂等键加入会话号。

## User Stories

1. 作为玩家，我希望先打窝再抛竿时窝仍然生效。

2. 作为玩家，我希望窝落在邻格时不会被当成完全无关。

3. 边界：重复上报同一发不能重复投放。

4. 边界：关闭邻格容差后回到严格同格。

## Implementation Decisions

- 模块：`internal/gameserver` 的窝料落点、判鱼和动态协议处理。

- 落点优先级：`groundbait_throw_land_cell` 计算落点、浮标落点、玩家坐标、模糊钓点。

- 衰减：`groundbait_diffuse_fall` 缺省 0.40；`groundbait_neighbor_cells` 缺省 1。

- 幂等：弹弓键为 `catapult:<hand>:<session>:<call_id>`。

- 无迁移、无协议字段形状变化。

## Testing Decisions

- 行为：浮标格、玩家脚下格、邻格和模糊窝的权重比较。

- 扣份：先本格、再邻格、最后模糊窝。

- 幂等：同 CallID 跨会话分别入账。

## Out of Scope

- 不精确反推客户端飞行方向。

- 不引入新的世界坐标协议。
