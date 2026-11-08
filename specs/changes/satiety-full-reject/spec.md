# 饱食度满时拒绝进食

Status: done

## Problem Statement

`useAlcoholDrink`（4/8 使用通道）的食品分支只做 `clampFloat(Secondary + restore, 0, 1)`：饱食度到 1.0 后继续吃，物品照扣、状态条不动，等于白扔物品，客户端也没有任何反馈。

## Solution

在扣减物品之前检查当前饱食度，达到阈值就直接拒绝，物品不扣，并通过系统聊天推送提示。

## User Stories

1. 作为玩家，我希望吃饱之后点食物不会被莫名扣掉，而是明确告诉我已经吃不下了。
2. 边界：酒精饮品仍走原有酒兴逻辑，不受该检查影响；饱食度略低时（0.99）仍可正常进食。

## Implementation Decisions

- `internal/gameserver/drink_use.go` 新增常量 `foodSatietyFullThreshold = 0.995`：用略低于 1.0 的阈值，避免浮点累加尾差导致“永远差一点点”。
- 检查点放在事务内、`consumeItemQtyTx` 之前，直接读 `player_condition.secondary`（该行不存在时按“未饱”处理，交给后续 `applyConsumableRestoreTx` 建行）。
- 仅在 `isConsumable`（食品/热饮分类命中 `consumableRestoreForItem`）时生效；酒类（`isDrink`）不拦。
- 拒绝走 `fail("你已经吃饱了")` 并额外 `pushSystemChat("你已经吃饱了，吃不下更多食物")`——客户端没有可用的 `kOperationResult_PlayerFull` 数值（IL2CPP metadata 被精简），只能用聊天通道给可见反馈。

## Testing Decisions

- 新增 `satiety_full_test.go`：把 `secondary` 设为 1.0 后使用 `carrot`，断言 `hItemUse` 返回失败且数量不变；把 `secondary` 设为 0.5 后同样使用一次，断言正常扣到 2。
- 完成前运行 `go build ./...`、`go vet ./internal/gameserver`、`go test -run TestFoodUse ./internal/gameserver`。

## Out of Scope

不改挖饵精力（上游 #318 正在改同一段 `baitdig.go`）、不改状态条推进曲线、不处理酒类的饱食度上限。
