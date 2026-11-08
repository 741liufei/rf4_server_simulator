# 钓获礼包（随机开箱）

钓上鱼并入护成功后，按概率当场开出一个礼包，奖励直接进背包并播报。
不需要点开背包，也不需要客户端支持任何新协议。

## 效果

钓上鱼 → **入护成功**（14/5）→ 按概率触发礼包 → 服务端当场掷骰 → 奖励直接进背包 →
聊天播报「钓到了活动礼包，开出：XXX ×N」。

钓到奖杯（3）或蓝冠（4）鱼时默认必开一包，作为保底。

## 配置

写在 `kv.loot_box_defs`（JSON），改完 30 秒内生效，不用重启：

```json
[
  {
    "id": "event_box",
    "name": "活动礼包",
    "chance": 0.05,
    "rewards": [
      {"item_id": "worm_01", "qty": 2, "weight": 40},
      {"item_id": "bait_dreissena", "qty": 1, "weight": 30}
    ]
  }
]
```

| 字段 | 说明 |
|---|---|
| `chance` | 每次钓获触发的概率（0~1） |
| `rewards` | 奖励池；**留空则从商店货架随机抽一件有库存的商品** |
| `rewards[].weight` | 相对权重（不是百分比） |
| `rewards[].qty` | 数量；非计件物品（竿/轮）每件一个实例，数量恒为 1 |
| `carrier_item` / `carrier_qty` | 可选：开袋时额外发放的"看得见的袋子"道具 |

写入方式（主库）：

```sql
INSERT INTO kv(k,v) VALUES('loot_box_defs','[...]')
  ON CONFLICT(k) DO UPDATE SET v=excluded.v;
```

## 开关

**默认关闭**：需要在「游戏参数 → 钓获礼包」里把总开关设为 1 才会触发。

| 参数 | 默认 | 说明 |
|---|---|---|
| `loot_box_enabled` | 0 | 总开关；设 1 开启（掷骰、发奖），0 完全不触发 |
| `loot_box_chance` | 空 | 覆盖配置里的 `chance`；留空表示按配置 |
| `loot_box_trophy_guarantee` | 1 | 开启后，钓到奖杯/蓝冠鱼必开一包 |

## 实现要点

- 入口：`fishingKeep`（14/5 入护）成功后异步调用，不阻塞回包。
- 抽奖：配置了 `rewards` 时按权重抽取；否则 `shop_item` 里 `stock>0` 的商品随机一件。
- 资格校验：随机奖励和 `rewards` 都排除 category 55（套盒）与 73（礼物袋），并要求类别存在
  `categoryWireClass` 权威映射；`carrier_item` 也必须能正确物化。无法发放的配置项会被跳过。
- 发奖：`AddToStack` → `pushItemUpdate`（4/22）→ 聊天播报。
- 幂等：同一尾鱼重复 14/5 只触发一次礼包。
- 容错：写库失败只记 Warn，不影响入护结果；goroutine 带 recover。

## 为什么不用客户端开箱弹窗

客户端的 `10304 奖励包` 在 wiregen 表里有字段登记，但服务端零实现，也没有
真实报文形状可依据；形状不对会让客户端解析异常。当前做法复用已验证的
`4/22 物品更新 + 聊天播报` 通道。
