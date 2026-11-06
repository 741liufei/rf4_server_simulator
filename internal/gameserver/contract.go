package gameserver

import (
	"embed"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"rf4server/internal/proto"
)

// ---- 内嵌资产（协议表与配置数据）----

//go:embed assets/command_contract.json
var contractJSON []byte

//go:embed assets/command_responses.json
var fallbackTableJSON []byte

//go:embed assets/fallback_kinds.json
var fallbackKindsJSON []byte

//go:embed assets/argcode_table.json
var argcodeJSON []byte

//go:embed assets/config_wire
var configWireFS embed.FS

// ---- 命令合同 ----

type contractEntry struct {
	Names  []string `json:"names"`
	Method string   `json:"method"`
	Resp   struct {
		Kind    string   `json:"kind"`
		Classes []string `json:"classes"`
		TypeIDs []int    `json:"type_ids"`
	} `json:"resp"`
}

type contractData struct {
	ByKey    map[string]*contractEntry `json:"by_key"`
	PushArgs map[string][]string       `json:"push_args"`
}

var (
	contractOnce sync.Once
	contract     *contractData
)

func loadContract() *contractData {
	contractOnce.Do(func() {
		c := &contractData{}
		if err := json.Unmarshal(contractJSON, c); err != nil {
			panic(fmt.Sprintf("command_contract.json 解析失败: %v", err))
		}
		contract = c
	})
	return contract
}

// checkPushArgs 校验推送参数与 command_contract.json 中的 push_args 一致。
//   - []：要求空 args；
//   - ["*"]：不限制参数形状；
//   - 尾部 "pattern..."：前缀固定，其余参数按该模式重复；
//   - "item"：type_id 为 100..199 的对象；
//   - 其他模式：按位置校验参数个数与形状。
//
// 返回空串表示通过，否则返回拒发原因。
func checkPushArgs(main, sub uint8, body *proto.Value) string {
	c := loadContract()
	decl, ok := c.PushArgs[cmdKey(main, sub)]
	if !ok || body == nil || body.Kind != proto.KindArgs {
		return "" // 未声明或不是 args 时交给其他层校验
	}
	if len(decl) == 1 && decl[0] == "*" {
		return ""
	}
	items := body.Items
	// 尾部 "pattern..." = 散开重复
	if len(decl) > 0 && strings.HasSuffix(decl[len(decl)-1], "...") {
		base, tail := decl[:len(decl)-1], strings.TrimSuffix(decl[len(decl)-1], "...")
		if len(items) < len(base) {
			return fmt.Sprintf("推送参数个数 %d < 合同前缀 %d（形状 %v）", len(items), len(base), decl)
		}
		for i := 0; i < len(base); i++ {
			if reason := matchPattern(base[i], items[i]); reason != "" {
				return fmt.Sprintf("推送参数[%d] %s", i, reason)
			}
		}
		for i := len(base); i < len(items); i++ {
			if reason := matchPattern(tail, items[i]); reason != "" {
				return fmt.Sprintf("推送参数[%d] %s", i, reason)
			}
		}
		return ""
	}
	if len(decl) != len(items) {
		return fmt.Sprintf("合同要求 %d 个参数, 实得 %d", len(decl), len(items))
	}
	for i, want := range decl {
		if reason := matchPattern(want, items[i]); reason != "" {
			return fmt.Sprintf("推送参数[%d] %s", i, reason)
		}
	}
	return ""
}

// itemTypeIDs 物品实例 type_id 显式白名单，集合由 items.go 的
// categoryWireClass（类别 → wire 类/tid 唯一权威映射）派生：基础装备
// 100..199 之外，实际 tid 覆盖鱼护 25、铃铛 700、提取物 721、渔轮组件
// 610..616、电机 1470、礼包 3100/3103、打窝器/弹弓 3200/3201、竿架 11500、
// 定义类 12600/12601、铃铛族 12702/12703、标本 13001、13041、房契类
// 11301、2015、技能书 13301 及兜底 140。
// 服务端下发的物品树一律出自 categoryWireClass（ItemToTree / prepareNewItem），
// 以它为单一事实来源可同时满足两类实证案例：按 100..199 设上限会拒发
// 钓鱼游装备包的铃铛/竿架（4/22 缺件，2026-11-05 实测），漏 13001 会让
// 标本制作后的背包增量 4/22 丢失（2026-11-06 实测）；tid < 100 是系统
// 对象，仍拒绝。
var itemTypeIDs = func() map[uint32]struct{} {
	m := make(map[uint32]struct{}, len(categoryWireClass))
	for _, e := range categoryWireClass {
		m[uint32(e.TypeID)] = struct{}{}
	}
	return m
}()

// isItemTypeID 物品实例 type_id 白名单判定。
func isItemTypeID(tid uint32) bool {
	_, ok := itemTypeIDs[tid]
	return ok
}

// matchPattern 单个参数与模式匹配；返回空串=通过。
func matchPattern(want string, it *proto.Value) string {
	switch {
	case want == "*":
		return ""
	case want == "item":
		// 物品实例包装对象：type_id 必须命中 categoryWireClass 派生的
		// 实例白名单（见 itemTypeIDs 注释）。
		if it == nil || !it.IsKind(proto.KindObj) {
			return "应为 item(obj 实例白名单)"
		}
		if !isItemTypeID(it.TypeID) {
			return fmt.Sprintf("应为 item(obj 实例白名单), 实得 %d", it.TypeID)
		}
		return ""
	case want == "guid":
		if !it.IsKind(proto.KindGuid) {
			return fmt.Sprintf("应为 guid, 实得 %s", kindName(it))
		}
		return ""
	case want == "obj" || strings.HasPrefix(want, "obj:"):
		if !it.IsKind(proto.KindObj) {
			return fmt.Sprintf("应为 obj, 实得 %s", kindName(it))
		}
		if rest := strings.TrimPrefix(want, "obj:"); rest != want && rest != "" {
			var wantTid uint32
			fmt.Sscanf(rest, "%d", &wantTid)
			if wantTid != 0 && it.TypeID != wantTid {
				return fmt.Sprintf("应为 type_id %d, 实得 %d", wantTid, it.TypeID)
			}
		}
		return ""
	case want == "str":
		if !it.IsKind(proto.KindStr) {
			return fmt.Sprintf("应为 str, 实得 %s", kindName(it))
		}
		return ""
	case want == "int" || want == "u32" || want == "u64":
		if !it.IsKind(proto.KindU32) && !it.IsKind(proto.KindU64) {
			return fmt.Sprintf("应为整数, 实得 %s", kindName(it))
		}
		return ""
	}
	return ""
}

func kindName(v *proto.Value) string {
	if v == nil {
		return "nil"
	}
	return string(v.Kind)
}

// ---- argcode 表 ----

type argcodeData struct {
	ByElem   map[string]string `json:"by_elem"`
	ByCmd    map[string]string `json:"by_cmd"`
	ByTypeID map[string]string `json:"by_type_id"`
}

var (
	argcodeOnce sync.Once
	argcode     *argcodeData
)

func loadArgcode() *argcodeData {
	argcodeOnce.Do(func() {
		a := &argcodeData{}
		if err := json.Unmarshal(argcodeJSON, a); err != nil {
			panic(fmt.Sprintf("argcode_table.json 解析失败: %v", err))
		}
		argcode = a
	})
	return argcode
}

// ContainerCodeForTypeID 元素 type_id -> args 容器 code（by_elem 优先，by_type_id 兜底）。
func ContainerCodeForTypeID(tid uint32) (string, bool) {
	a := loadArgcode()
	k := fmt.Sprintf("%d", tid)
	if v, ok := a.ByElem[k]; ok && v != "524" {
		return v, true
	}
	if v, ok := a.ByTypeID[k]; ok {
		return v, true
	}
	return "", false
}
