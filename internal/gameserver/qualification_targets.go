package gameserver

import (
	_ "embed"
	"encoding/json"
	"sync"
)

// qualification_targets.json 保存 168 条资格任务的静态目标值（5007.Idx3 进度分母）。
// 客户端进度 = 已计数集合长度 / Idx3；分母为 0 会除零导致面板异常。
//
//go:embed assets/qualification_targets.json
var qualificationTargetsJSON []byte

var (
	qualificationTargetsOnce sync.Once
	qualificationTargetsMap  map[uint64]uint32
)

// qualificationTarget 返回规则的目标值；未收录时回退 1（保证分母 > 0）。
func qualificationTarget(ruleID uint64) uint32 {
	qualificationTargetsOnce.Do(func() {
		var doc struct {
			Targets map[uint64]uint32 `json:"targets"`
		}
		if err := json.Unmarshal(qualificationTargetsJSON, &doc); err == nil {
			qualificationTargetsMap = doc.Targets
		}
	})
	if v, ok := qualificationTargetsMap[ruleID]; ok && v > 0 {
		return v
	}
	return 1
}
