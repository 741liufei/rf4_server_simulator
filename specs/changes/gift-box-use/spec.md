# 礼盒（ieopbeejfmc）按官服分流：发放保留本体、4/8 使用拆开

Status: done

<!-- 状态取值：draft | ready | in-progress | done | cancelled
     完成时原地改这里，不要归档、不要搬家、不要另开第二份。 -->

## Problem Statement

玩家在商店购买"礼盒"类商品（如 149 元飞德套装 box_feeder_pro/start/start_2）后，礼包被服务端立即拆成 6 件装备塞进背包。官服行为是：礼盒以一件物品躺在背包里，玩家点击"使用"才会拆开。邮件附件里的组合包同样被立即展开。结果是本服玩家无法"存着礼包以后再开"，与官服体验不一致。

## Solution

礼盒与饵料盒按配置位 `ieopbeejfmc` 分流：饵料盒（位为空，官服报文实证购买即展开）保持现状；礼盒（位为 true）购买/领附件时只发放礼盒本体（cat 55 → wire 类 coacllgmnml/3100），玩家在背包点"使用"（4/8 items/use）时服务端同事务删除礼盒、展开发放内容物，800 回执 f0 携带内容物、f2 携带礼盒 GUID。客户端零改动。

## User Stories

1. 作为玩家，我希望购买飞德套装后背包里出现一个礼盒物品，以便我可以以后再打开它。
2. 作为玩家，我希望点击礼盒的"使用"后，礼盒消失、6 件套装内容物进入背包。
3. 作为玩家，我希望从邮箱领取礼盒附件时，领到的也是礼盒本体而不是拆散的装备。
4. 边界：食品店饵料盒（box_food_egg_10、box_worm_01_10 等）购买后仍直接得到饵料堆，与官服报文行为一致，不回退。
5. 边界：礼盒只能在背包中拆开（非背包容器拒绝），重复的 4/8 请求（同 call_id 重放）不会拆出第二份内容物。

## Implementation Decisions

- 模块：`internal/gameserver`。不改表结构、不迁移；礼盒本体直接复用 `prepareNewItem` 现行 3100 构造路径。
- 判别：新增 `bundleOpenable(def)`，读 `item_def.attrs_json["ieopbeejfmc"]` 为 true。cat 55 且该位为 true → 礼盒；否则维持"购买即展开"。
- 22/2 发放分流（`prepareStoreGoods`）：礼盒分支返回单个礼盒物品（`isBundle` 仍为 true，回执走 804.f2，与官服盒子购买 f1=null/f2=args 的形状一致）；饵料盒分支不变。804 收据 166 仍嵌入首棵树（现状行为，未改动）。
- 19/3 邮件附件分流（`mailbox.go`）：组合包附件在 `BuyPrice==0` 守卫之后按 `bundleOpenable` 分流——礼盒按包数逐个发放礼盒本体（非计件，一包一件），饵料盒维持 `prepareBundleItems` 展开。
- 4/8 新分支（`world.go hItemUse`，新文件 `giftbox_use.go`）：目标物品是礼盒时——`prepareBundleItems` 先行构造内容物（其约定：只读、必须在库存事务前），再开经济事务 `removeItemsTx` 删礼盒 + `grantPreparedItemTx` 入库内容物，提交后 `ItemToTree` 构树（事务内禁 ItemToTree，见既有约定），回 `buildRigSnapshot800(f0=内容物, f1=null, f2=[礼盒GUID])`。校验：容器枚举=0（背包）、物品属于本人、`Container=="backpack"`、无装配占用。非礼盒物品不受影响（分支返回 false 落回既有 handler）。
- 幂等：响应回执由调度层统一回执表（token 哈希 + main + sub + call_id）兜底，礼盒拆开事务在 handler 内一次执行，重放由回执表回放旧响应，不再自建 kv 回执（与 shop 购买同一模式）。
- 兼容：客户端基线 4.0.25034。1/4 配置 blob 为官服原样下发，12006（礼盒 ConfigT）随 type_1 携带，礼盒详情与"使用"按钮由客户端按 `ieopbeejfmc` 自行渲染，无需服务端配置改动。回滚 = revert 本分支；饵料盒路径未动，回滚后行为与现状一致。
- 证据等级：官服饵料盒购买即展开、804 回执 f1/f2 分工、4/8 请求 args("524")[容器枚举 u32, guid] 与响应 800 三段（f0 新增/f1 更新/f2 删除 GUID）形状均为 `[事实]`（官服真实报文）；"礼盒（ieopbeejfmc=true）官服以本体发放、点使用拆开"为 `[推断]` + 玩家目击（客户端按该配置位渲染礼盒详情与"使用"入口，"使用"即发 4/8；无官服礼盒购买报文样本）。

## Testing Decisions

- 单元（真实 SQLite fixture，复用 bundle_inventory_test 的建库接缝）：`bundleOpenable` 对 box_feeder_pro=true、box_food_egg_10=false；购买礼盒后 item 表仅一行 cat 55 礼盒（wire 类 coacllgmnml/3100）、无内容物行；饵料盒购买后为 food_egg 内容物行（回滚后本组测试变红，属预期）。
- 行为（handler 级）：4/8 指向礼盒 → 库存礼盒行删除、6 件内容物行入库，响应 800.f2 含礼盒 GUID、f0 含内容物树；4/8 指向非背包容器或他人物品 → 拒绝且库存不变；4/8 指向饵料堆 → 落回既有消耗品/通用分支，礼盒分支不拦截。
- 存储：邮件礼盒附件领取后 item 表为礼盒本体行。
- 协议：响应过既有合同守卫（804/800 注册规则不变），不新增契约。

## Out of Scope

- 4/8 使用礼盒以外的物品行为改动（酒精/技能书/消耗品分支不动）。
- 礼盒跨容器移动、堆叠、交易、上架市场的限制规则（官服未采集，礼盒按普通非计件物品对待）。
- `dnmfciolgep`（3100.f11）等 3100 实例字段的官服语义标定。
- 邮件附件非计件物品 qty>1 的既有语义（礼盒按包数逐件发放仅限组合包分支）。
- 官服礼盒购买/拆开的真实报文采集（当前以推断 + 玩家目击为依据，后续采集到样本再校准）。

## Further Notes

- `[事实]` 静态库 cat 55 共 1585 条：`ieopbeejfmc=true` 100 条（飞德套装、钓具配件包、6 连装罐/洗护包等多件组合），为空 1485 条（全部是 box_<单一内容物>_<数量> 的数量盒，即官服报文实证的即拆类）。判别位因此覆盖 100 个 SKU 的新行为——这不是飞德套装特例，而是客户端自己的分类位。
- `bundle_inventory_test.go` 展开路径夹具由 AV_shamp1_pack_6_8（ieopbeejfmc=true，现归礼盒路径）改指 box_food_egg_10（为空，官服即拆类），保持测试意图不变；数量断言 12/24 相应改为 10/20。
- `[事实]` 官服 22/2 买 box_food_egg_10（farmstore_002，504 银）回执 804：f1=null，f2=args("61")[food_egg(6608) 堆]；box_worm_01_10 同（官服真实报文）。
- `[事实]` 4/8 items/use 请求 args("524")[容器枚举 u32, guid]；响应 800 f0=新增/f1=更新/f2=Guid[] 删除（官服真实报文：消耗品使用后数量递减）。
- `[事实]` 客户端对礼盒的处理由配置位 ieopbeejfmc 驱动：为 true 时物品详情提供内容预览与"使用"入口，点击"使用"发出 4/8，礼盒在非背包容器中会被拒绝。
- `[事实]` 静态库 box_feeder_pro/start/start_2 的 attrs `ieopbeejfmc=true`；box_food_egg_10/box_worm_01_10 为 null（server_data/static.sqlite3）。
- `[推断]` 官服礼盒购买回执 804.f2 应携带礼盒本体树（3100）——无直接样本，由客户端礼盒 UI 行为与玩家目击支持。
- 未验证：官服礼盒拆开后 800 三段的确切分配（f0 放内容物、f2 放礼盒 GUID 按 800 字段既有语义推断）。
