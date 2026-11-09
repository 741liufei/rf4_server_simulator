# 鱼护渔获作为制作材料

Status: done

## Problem Statement

鱼类加工配方（蟹肉、鱼片、活饵、死鱼饵、熏鱼等）的材料是玩家的渔获，但这些鱼种
只存在于 `keepnet_items`，没有 `item_def` 定义。制作的材料扣除只扫描 `item` 表的
`backpack`/`rig` 容器，永远找不到它们，因此这类配方一律返回「材料不足」，制作
失败（803.f0=0），客户端提示「未成功，再试一次」。

用 `recipe.materials_json` 与 `item_def` 交叉核对：**64 个配方**的全部可接受 id 都
不在 `item_def` 里，整族鱼类加工玩法不可用。

## Solution

玩家把鱼护里的渔获拖进制作材料槽即可完成鱼类加工：配方按鱼种匹配鱼护条目，制作
成功后在结果与背包增量里给出产物，鱼护里被用掉的那条鱼随之移除；材料不够或条目
已失效时制作失败（803.f0=0），不扣鱼、不产出。

鱼护渔获的扣减与产物入库在同一事务内完成，同一尾鱼不会被两条制作同时消费。

## User Stories

1. 作为玩家，我希望把鱼护里的蟹拖进制作栏就能做出蟹肉，以便拿它当饵。
2. 作为玩家，我希望鱼护里被用掉的那条鱼立即从鱼护消失，剩余条目不受影响。
3. 边界：材料槽没有指向有效鱼护条目、鱼种与需求不符、或该条目已被并发消费时，
   制作失败（803.f0=0），不产生产物、不动鱼护、不写制作流水。
4. 边界：一次制作需要多份同种鱼时，从多条鱼护条目中依次扣减；凑不够即整体失败。
5. 边界：其他玩家的鱼护条目不参与匹配（按 `uid` 过滤）。

## Implementation Decisions

- `internal/gameserver/craft_ops.go`：材料扣除新增「鱼护渔获」来源。
  现有两条扣料分支（`allDemandsExplicit` 全显式分支、按材料槽扣减分支）都只查
  `item` 表的 `backpack`/`rig` 容器（`listItemsTx`）；鱼护渔获只存在于
  `keepnet_items`，两者天然不会互相命中，因此按「背包找不到才回落到鱼护」的
  顺序补一条来源，不改变既有背包语义。
- 匹配规则：需求的 `demandAccepts` 里任一 id 等于鱼护条目的 `species` 即视为命中；
  材料槽的 `GUID` 若指向鱼护条目则优先消费该条（客户端拖入的就是它），否则按
  鱼种在同 `uid` 的条目里按时间顺序取。
- 消耗复用 `DeleteKeepnetTx` 并清理镜像行与槽位关联，与标本挂鱼（cat 71）的
  「渔获即消耗」语义保持一致；整段在经济事务 `beginEconomyTx` 内，与产物入库
  一起提交或回滚。
- 数据与迁移：无表结构变更、无静态库变更。`keepnet_items` 只按 `uid`/`echo_guid`
  读写。
- 兼容：客户端 4.0.25034；803 响应形状不变（f1 产出、f6 背包增量、f8 移除列表），
  只是把鱼护条目 guid 加进 f8，并补一条 4/21 让鱼护界面撤下被吃掉的鱼。
- 回滚方式 = revert 本次提交。

## Testing Decisions

- 存储/行为层（真实 SQLite，`openRecipeTestDB` + 走 9/1 真实 handler）：
  - `TestCraftCrabMeatConsumesKeepnetCrab`：鱼护有渔获、材料槽指向它 →
    803.f0=1、产出 `crab_meat`、鱼护条目被删除。
  - `TestCraftCrabMeatWithoutKeepnetCrabFails`：鱼护为空 → 803.f0=0、无产物。
  - `TestCraftFishBaitRejectsForeignKeepnetFish`：鱼在别的 uid 名下 → 失败，
    且该条目仍在。
  - `TestCraftConsumesMultipleKeepnetFishForBatch`：需要多份时从多条条目扣足。
- 期望值来源：`recipe.materials_json` 里的 `alternatives` 字面量、`keepnet_items`
  行、`item_def` 的产物定义——不是用同样代码再算一遍。
- 去掉鱼护回落分支后，前两个与批量用例会红。

## Out of Scope

- 标本板（cat 71）的挂鱼语义，已有 `trophy-craft` 覆盖，本次不动。
- 烹饪（9/2）、分解（9/3）对鱼护渔获的读取。
- 鱼护容量/入护链路本身。
