# 钓获礼包

Status: done

## Problem Statement

玩家钓获入护后只有鱼本身进入护网，没有可配置的随机奖励反馈；活动运营也缺少一种不依赖新客户端协议的即时发奖方式。同时，奖励池若允许任意商店物品，可能把没有权威 wire 映射的类别错误物化，导致背包显示异常。

## Solution

14/5 入护成功后，服务端按概率异步开出一个服务端礼包：奖励直接进背包，通过既有 4/22 物品更新和聊天播报反馈。礼包总开关默认关闭，概率与奖杯/蓝冠保底可在后台调整，奖励池由 `kv.loot_box_defs` 配置。所有奖励路径统一过滤不可物化类别，重复 14/5 不会重复发奖。

## User Stories

1. 作为玩家，我希望钓获入护后偶尔直接获得可见奖励，不必打开额外界面。
2. 作为运营，我希望默认关闭礼包，并能在后台开启、调整概率和奖杯/蓝冠保底。
3. 作为运营，我希望配置错误或不可物化的商品被安全跳过，而不是污染玩家背包。
4. 边界：同一尾鱼重复上报 14/5 时，奖励只发放一次。

## Implementation Decisions

- `internal/gameserver/econ.go`：14/5 入护成功后调度异步礼包；异步失败不回滚已完成的入护。
- `internal/gameserver/loot_box.go`：读取 `kv.loot_box_defs`，按配置或商店库存抽取奖励，复用 `AddToStack`、4/22 和系统聊天通道。
- 奖励资格统一使用 `categoryWireClass`：随机奖励和配置 `rewards` 排除 category 55/73，并要求类别存在权威 wire 映射；`carrier_item` 同样要求可物化。
- `internal/admin/web/index.html`：新增「钓获礼包」参数组；`loot_box_enabled` 默认 0，`loot_box_chance` 可覆盖配置概率，`loot_box_trophy_guarantee` 控制奖杯/蓝冠保底。
- 数据与迁移：不新增表结构；配置保存在现有 `kv`。
- 兼容：不新增协议 opcode；客户端继续消费既有 4/22 物品更新和聊天播报。回滚时关闭 `loot_box_enabled` 即可停止触发。

## Testing Decisions

- 单元/行为测试：`internal/gameserver/loot_box_test.go` 覆盖概率 1 必开、配置奖励、总开关关闭、随机池类别过滤、配置奖励与载体过滤。
- 幂等测试：通过真实 14/5 入口首次入护并重复上报，断言同一 FishGUID 的礼包物品只增加一次。
- 期望值来自固定测试物品与显式概率，不通过生产随机分布反推。
- 回滚后会变红：类别过滤、配置奖励过滤、14/5 重放幂等用例。

## Out of Scope

- 不新增客户端开箱弹窗或 10304 协议实现。
- 不改变商店后台库存覆盖规则；随机池继续读取静态 `shop_item` 的库存。
- 不放宽不可物化类别的奖励资格。
- 不调整已有鱼类结算、护网容量或鱼价。

## Further Notes

- `[事实]` category 43 的部分商店物品与 category 73 礼物袋没有 `categoryWireClass` 映射，直接创建会落兜底钓线类。
- `[事实]` 礼包入口在 14/5 入护成功后执行，重复请求在 Pending 清空后不会再次调度。
- `[推断]` 复用 4/22 比新增 opcode 风险更低，因为该通道已有实机验证。