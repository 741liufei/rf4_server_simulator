# 鱼市按卖鱼所在地图计价

Status: ready

## Problem Statement

鱼市询价与结算都用鱼的钓获地图（`keepnet_items.keep_loc` → `level_...`）取倍率，玩家把鱼带到另一个水域卖出时，价格不会跟着变。例如同一批在老奥钓的欧鳊，拿到海图卖仍是 0.916 倍率的价格，水域价差在跨图交易里被抹掉。

## Solution

询价（20/1）与结算（20/2）统一改用卖鱼时玩家所在水域（`Server.currentLevel(uid)`）取倍率，与鱼的钓获地图无关。同一批鱼在不同地图卖出，价格随该图 `market_pct` 变化，卖出入账金额与询价显示一致。

## User Stories

1. 作为玩家，把在老奥钓的鱼带到海图卖，同一条欧鳊从 58.27 变成 63.61。
2. 作为玩家，回老奥卖出时又变回 58.27，价格不会因为鱼“记忆”了旧地图而卡住。
3. 作为玩家，卖出时入账金额与鱼市列表显示的价格一致。

## Implementation Decisions

- `hMarketRate`：`mapKey` 由 `levelKeyOfLoc(e.KeepLoc)` 改为 `currentLevel(uid)`。
- `hMarketSell`：事务前算一次 `sellMapKey := currentLevel(uid)`，事务内所有鱼共用，避免事务内做定位查询。
- `currentLevel` 已有内存缓存 + `player_location` 回退，进程重启不会退回出生图。
- 价格表结构、每日系数、`market_pct` 折算均不变。

## Testing Decisions

- `TestMarketPriceFollowsSellMap`：同一鱼条目在 1 图（0.831）、老奥（0.916）、海图（1.0）的报价严格递增，且比值与倍率表一致。
- `TestMarketRateAndSellUseSameMap`：三个水域下询价与结算返回同一分值，防止“看一个价、入账另一个价”。
- 回滚后前一个用例会变红（三档相等），后一个用例在分别取 `keep_loc` 与 `currentLevel` 时也会变红。

## Out of Scope

- 不改价格表数值、不改每日系数、不改稀有度口径。
- 不改玩家位置持久化（“每次登录回到出生图”是独立问题）。
- 不引入按水域区分的鱼市库存或收购限制。
