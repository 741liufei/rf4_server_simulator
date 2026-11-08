# 组合包条目数量双语义：mlapkoadfnh=true 为绝对个数

Status: done

<!-- 状态取值：draft | ready | in-progress | done | cancelled
     完成时原地改这里，不要归档、不要搬家、不要另开第二份。 -->

## Problem Statement

玩家在钓鱼站购买并拆开大师蚯蚓盒（box_worm_asurv_100 等 5 种，条目数量 100）后，背包得到 3000 个蚯蚓而非 100 个。`prepareBundleItems` 对所有计件条目一律按「条目数量 × 子物定义默认量」乘算（worm_asurv def `ljnnpokapig=30` → 30×100=3000），无视条目上的语义开关字段。该乘法对血虫盒等普通数量盒是正确的，但 `mlapkoadfnh=true` 的条目官方语义是绝对个数。

## Solution

`prepareBundleItems` 解析条目字段 `mlapkoadfnh`：为 true 时计件内含物最终数量 = 条目数量 × 包数（不再乘子物 def `ljnnpokapig`）；缺省时维持现行乘法语义。商店购买即拆、邮件附件展开、4/8 礼盒拆开三个入口共用此函数，一处修复全覆盖。溢出守卫相应改为对绝对个数直接比对数量槽上限。

## User Stories

1. 作为玩家，我希望拆开 box_worm_asurv_100 后得到 100 个蚯蚓，与礼盒详情预览的数量一致。
2. 作为玩家，我希望血虫盒等普通数量盒的发放数量不变（box_bloodworm_10 仍是 300 = 30×10）。

## Implementation Decisions

- 模块：`internal/gameserver`（`bundle_inventory.go`、`csnames.go` 常量）。不改表结构、不迁移、不改协议形状。
- 条目解析：`entry["mlapkoadfnh"]` 布尔（静态库中取值 true/null，null 按缺省乘法处理）。`contentPlan` 增加 `absolute` 位。
- 数量计算分支：`absolute` → `quantity = units`（条目×包数），超数量槽上限直接拒绝；缺省 → 现行 `it.Qty * units` 与既有守卫不变。
- 兼容与回滚：回滚 = revert 本分支；缺省条目（全静态库 1788/1793 条）行为零变化。

## Testing Decisions

- 行为（真实静态库 SKU）：`prepareBundleItems("box_worm_asurv_100")` → 一行 worm_asurv、权威/UI 数量槽=100；`prepareBundleItems("box_bloodworm_10")` → 300（锁定缺省乘法不回退）。
- 边界：绝对个数条目超 U32 数量槽上限必须拒绝。
- 端到端：4/8 拆开 box_worm_asurv_100 → 800.f0 携带 1 棵树、礼盒行删除、worm_asurv 行数量=100。
- 回滚后本组测试变红，属预期。

## Further Notes

- `[事实]` 官服客户端 `ahoacflhhno.ekfefhbgegl(onobdmohahp def, int 条目数, bool mlapkoadfnh)`（Assembly-CSharp RVA 0xD15F50）：`if (mlapkoadfnh) return 条目数;` 否则对可堆叠子物返回 `def.ljnnpokapig × 条目数`（phdchabilin.ljnnpokapig 位于 offset 0x78，与乘法所用偏移一致）。客户端展示与发放期望均出自该函数。
- `[事实]` 静态库 cat 55 全部 1793 条内含物条目中 `mlapkoadfnh=true` 恰好 5 条：box_worm_asurv_100 / box_worm_msor_100 / box_worm_orlen_100 / box_worm_sur_100 / box_worm_volh_100（各 ×100）；其余 1788 条缺省。受影响 SKU 边界与此完全吻合。
- `[事实]` 实机（本模拟器 + 0.1.7 客户端）：拆开 box_worm_asurv_100 后背包 worm_asurv 堆=3000（=30×100），box_worm_volh_100 两盒=6000。
- `[推断]` 价格旁证：box_bloodworm_10（缺省条目）1782 银 ÷ 300 件 ≈ 5.9 银/件，与散装 bloodworm 180 银/30 件 = 6 银/件自洽——缺省条目乘法语义与经济定价一致；box_worm_asurv_100 9980 银对应 100 件高级饵定价合理。
- 官服礼盒拆开报文样本仍缺（与 gift-box-use 相同），本次以客户端算法（dump 级证据）为准。

## Out of Scope

- 已按错误数量发放的存量堆修正（运营侧决定，不在代码修复内）。
- 非计件内含物与 `mlapkoadfnh` 的组合（客户端对非堆叠子物同样直接返回条目数，现行逐个创建语义与之等价）。
- `mlapkoadfnh` 在 3100 礼盒实例字段上的其他语义（若有）。
