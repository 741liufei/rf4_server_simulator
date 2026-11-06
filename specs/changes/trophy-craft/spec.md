# 标本制作（猫 71）：嵌渔获、消耗与失败语义

Status: done

## Problem Statement

玩家用鱼护里的渔获制作标本板/落地架/吊顶（cat 71，52 个配方），此前服务端
不认识该制品族：803 产出列表的容器 code 按 by_type_id 合成段补成 471，客户端
按枚举解码器强转直接断线（Player.log 2026-11-06 02:59 实证）；即使不崩，
渔获也不嵌进板子、不消耗，玩家拿到空白标本；制作后背包增量 4/22 被
type_id 100..199 合同守卫拒发，重登才可见。

## Solution

猫 71 制作全链路可用：制作必须携带并定位到一条有效鱼护记录，渔获按鱼护
同款 25 类编码嵌进板子，制作与渔获消耗在同一事务内完成；无渔获/鱼种
不符/读取异常一律 803(false)，不再产出空白板。

## User Stories

1. 作为玩家，我希望用鱼护的鱼制作标本，板子上渲染这条鱼，鱼护里的原鱼随之移除。
2. 边界：请求不带有效鱼护 GUID、鱼种与鱼护不符、渔获已被并发消费时，
   制作失败（803.f0=0），不产板、不扣标本券、不动鱼护。

## Implementation Decisions

- `internal/gameserver/craft_ops.go`：挂鱼定位（KeepnetGetTx）、嵌鱼
  （embedMountedFish）、消耗（DeleteKeepnetTx+镜像行+槽位关联）全部在
  经济事务内；事务回滚则鱼不陪葬，事务提交则鱼必被消费（原子消费一次）。
- 容器 code：`listCodeForObjs` 保持 `by_elem → by_type_id → 61` 原序，
  仅对 13001 显式覆盖为 61（有断线实证的条目才进覆盖表，窝料 49/干饵 74/
  香精 54/礼包 193/需求对象 18 等既有值不变——不做无回归矩阵的全局变更）。
- `internal/gameserver/contract.go`：物品实例 type_id 白名单由
  `categoryWireClass` 权威映射派生（isItemTypeID），替代 100..199 区间判断；
  与钓鱼游 PR（feat/fishing-trip）写入完全一致，消除同文件双策略冲突。
- 数据与迁移：无表结构变更；static.sqlite3 为数据发布物（本地文件
  integrity_check=ok；早前 API 通道推送曾因二进制 CRLF→LF 归一化损坏，
  推送脚本已改为二进制直传）。
- 兼容：客户端 4.0.25034；回滚方式 = revert 分支提交。

## Testing Decisions

- 存储/行为层（真实 SQLite，`openRecipeTestDB` 模板）：
  - `TestMountedFishTreeTx`：定位、25 类编码、guid 派生、鱼种不符拒绝、
    无引用返回原因。
  - `TestCraftFishBoardConsumesKeepnet`：端到端 803.f1 嵌鱼、f8 携带
    被消耗 guid、鱼护条目删除。
  - `TestCraftFishBoardWithoutFishFails`：无渔获 → 803.f0=0，标本券未扣、
    鱼护未动、无板子产出（回滚本 PR 该用例必红）。
  - `TestListCodeForObjsMatrix`：13001→61 覆盖 + 5 类既有 by_type_id 值
    保持 + 未知 tid 回落 61（期望值 = argcode_table.json 字面量，非同源计算）。
- 门禁：`go build ./... && go vet ./... && go test ./internal/...`（CI 同款，
  Linux LF 下含 TestEmbeddedShopCatalog）。

## Out of Scope

- 标本分解（9/3）的鱼返还语义。
- 标本图鉴/收藏系统。
- by_type_id 合成段整体治理（另立任务，需全量实抓矩阵）。

## Further Notes

- `[事实]` code 471 断线：Player.log 2026-11-06 02:59 InvalidCastException。
- `[事实]` 空白板：清材料+无鱼护 GUID 请求返回 803.f0=1（审查复现）。
- `[推断]` 61 为通用对象列表安全回落：cat 13/17/26/27 的 by_elem 实抓一致。
