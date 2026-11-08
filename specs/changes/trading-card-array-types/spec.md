# 卡册实例数组类型修复

Status: done

## Problem Statement

收藏卡已经显示，但放进卡册时界面卡住并断线。再次登录时，已保存的卡册也会触发同样的解析错误。

## Solution

卡册及个人资料使用明确的实例数组类型。保留原卡、原鱼资料及已有入册状态，旧存档不需要补卡或重新建册。

## User Stories

1. 玩家可以将已有散卡入册，再取回同一张原卡。
2. 已保存的卡册重新登录后可读取，空册仍返回空数组。

## Implementation Decisions

- 客户端基线 4.0.25034。`proto.ps1` 核对卡片 dapkdcghfmg/11301、卡册 ckfmndabaei/11304 和操作响应 dlbahppbdad/800；27/2 实际请求为 string、Guid、Guid，符合现有 handler。
- [事实] 客户端入册响应抛出 `phjbhljooid` 到 `ipdpoaiekem` 的 InvalidCastException，位于数组解析及卡册解码链路。卡册 f0 的目标类型为 `ipdpoaiekem[]`，个人资料列表目标为 `imdcjllgdcn[]`。
- [事实] argcode_table 的 by_type_id 包含按序合成的映射，不能证明卡片 363、卡册 365 在客户端的真实含义。停止使用这两个合成值发送收藏数组。
- [事实] 从当前客户端真实OpenTypeResolver注册表读取：362→`ipdpoaiekem`，363→`phjbhljooid`，364→`imdcjllgdcn`。卡片和卡册使用362、364，并在by_elem补入两项显式映射；不重排合成表、不放宽响应合同。
- [事实] DTO名称未注册，直接发送名称会触发GetType not found；模板KV的旧具名编码不能证明其他类型名称可用。
- 旧 fields_json 中的 363 仍可读取，ItemToTree 统一投影为362卡片数组；入册、取回事务写入新编码。只读操作不改存档，无表迁移。
- 保留800响应其他字段、原卡GUID、原鱼、事务及回执语义。

## Testing Decisions

- 字面量断言 marker 0x03、362/364编号、空数组计数，不以合成映射表作为期望值。
- 既有真实 SQLite 协议测试覆盖新入册响应、模拟旧363存档后的服务重建读取、取回、原鱼资料保留及失败回滚。撤销修复会使字面量和旧册投影测试失败。
- 完整 Go test/vet/build；游戏内入册、取回和重登需用户验收，构造报文不算实机通过。

验证：已按真实注册表核对编号。最终卡片定点测试、完整 `go test ./... -count=1 -p=1`（gameserver 80.557秒）、`go vet ./...`、`go build ./...`通过。2026-11-07 玩家确认已有卡位、取回、再次入册及重新登录全部正常。

## Out of Scope

卡片补录或补发、概率调整、覆膜、卡册兑奖、资格任务，以及800其他字段的猜测性调整。
