package gameserver

import (
	"context"
	"math"
	"strings"

	"rf4server/internal/store"
)

// reelCorePartKeys 是渔轮能正常参与泄力、摩擦片磨损和零件聚合所必需的六件。
// 只要求这六件，不把电机等扩展件算进完整性判定。
var reelCorePartKeys = []string{
	"dlfjnokncfl", // 线杯
	"mlfjkaoicdn", // 摩擦片
	"fkmdfljgann", // 轴承
	"mglgpmpeond", // 摇柄
	"fblnceginlm", // 轮油
	"oiaplokhcii", // 齿轮
}

func isReelCorePartKey(key string) bool {
	for _, candidate := range reelCorePartKeys {
		if key == candidate {
			return true
		}
	}
	return false
}

// reelPartConfigCompleteValue 判断一个嵌套零件配置是否能物化成真实子实例。
// 摩擦片还必须带制动力，否则客户端会收到一个 0kg 的刹车片。
func reelPartConfigCompleteValue(key string, raw any) bool {
	cfg, ok := raw.(map[string]any)
	if !ok || len(cfg) == 0 {
		return false
	}
	if id, _ := cfg["nhjofmmmngl"].(string); strings.TrimSpace(id) == "" {
		return false
	}
	if _, ok := cfg["mgkfaekpnoj"]; !ok {
		return false
	}
	if key == "mlfjkaoicdn" && nestedF32(cfg, gearAttrDragMax) <= 0 {
		return false
	}
	return true
}

// reelCorePartsComplete 判断轮型是否具备六件核心零件。
func reelCorePartsComplete(attrs map[string]any) bool {
	if attrs == nil {
		return false
	}
	for _, key := range reelCorePartKeys {
		if !reelPartConfigCompleteValue(key, attrs[key]) {
			return false
		}
	}
	return true
}

// reelPartsNeedFallback 判断这只轮型是否要从同系列完整轮型借零件模板。
func reelPartsNeedFallback(def *store.ItemDef) bool {
	return def != nil && def.Category == gearCatReel && !reelCorePartsComplete(defAttrs(def))
}

// reelPartFallbackCandidates 加载所有带完整六件核心零件的渔轮定义。
// 只读静态表，并在 Server 生命周期内缓存。
func (s *Server) reelPartFallbackCandidates(ctx context.Context) []*store.ItemDef {
	if s == nil || s.DB == nil {
		return nil
	}
	s.reelPartFallbackOnce.Do(func() {
		defs, err := s.DB.ListItemDefsByCategory(ctx, gearCatReel)
		if err != nil {
			s.reelPartFallbackErr = err
			return
		}
		for _, def := range defs {
			if def != nil && reelCorePartsComplete(defAttrs(def)) {
				s.reelPartFallbackDefs = append(s.reelPartFallbackDefs, def)
			}
		}
	})
	return s.reelPartFallbackDefs
}

func reelItemPrefix(itemID string) string {
	if i := strings.IndexByte(itemID, '_'); i > 0 {
		return itemID[:i]
	}
	return itemID
}

func reelDonorScore(target, candidate *store.ItemDef) int64 {
	if target == nil || candidate == nil {
		return math.MaxInt64
	}
	const (
		subtypePenalty  = int64(1_000_000)
		prefixPenalty   = int64(100_000)
		brandPenalty    = int64(10_000)
		sizeStepPenalty = int64(100)
	)
	score := int64(0)
	if itemDefInt(candidate, "fopbhlmkcpe") != itemDefInt(target, "fopbhlmkcpe") {
		score += subtypePenalty
	}
	if reelItemPrefix(candidate.ItemID) != reelItemPrefix(target.ItemID) {
		score += prefixPenalty
	}
	targetBrand := strings.ToLower(strings.TrimSpace(target.Brand))
	if targetBrand != "" && strings.ToLower(strings.TrimSpace(candidate.Brand)) != targetBrand {
		score += brandPenalty
	}
	targetSize := itemDefInt(target, "gkfbafmbpam")
	candidateSize := itemDefInt(candidate, "gkfbafmbpam")
	if targetSize > 0 && candidateSize > 0 {
		delta := targetSize - candidateSize
		if delta < 0 {
			delta = -delta
		}
		score += delta * sizeStepPenalty
	}
	delta := candidate.NumericID - target.NumericID
	if delta < 0 {
		delta = -delta
	}
	score += delta
	return score
}

// reelPartFallbackDonor 选取同子类、同系列、编号最近的完整轮型。
// 目标轮型自身不带零件时，仍保留目标轮型的顶层定义、名称和数值 id；
// 只借用完整轮型的嵌套零件模板。
func (s *Server) reelPartFallbackDonor(ctx context.Context, target *store.ItemDef) *store.ItemDef {
	var best *store.ItemDef
	bestScore := int64(math.MaxInt64)
	for _, candidate := range s.reelPartFallbackCandidates(ctx) {
		if candidate == nil || candidate.NumericID == target.NumericID {
			continue
		}
		score := reelDonorScore(target, candidate)
		if score < bestScore {
			best, bestScore = candidate, score
		}
	}
	return best
}

// reelMaterializationAttrs 返回渔轮物化时应使用的 attrs。
// 完整轮型原样返回；占位轮型把完整轮型的零件模板作为底座，再保留目标轮型
// 已经存在且合法的零件配置。这样玩家换过的合法零件不会被覆盖。
func (s *Server) reelMaterializationAttrs(ctx context.Context, def *store.ItemDef) map[string]any {
	attrs := defAttrs(def)
	if !reelPartsNeedFallback(def) {
		return attrs
	}
	donor := s.reelPartFallbackDonor(ctx, def)
	donorAttrs := defAttrs(donor)
	if donorAttrs == nil {
		return attrs
	}
	merged := make(map[string]any, len(donorAttrs)+len(attrs))
	for key, value := range donorAttrs {
		merged[key] = value
	}
	for key, value := range attrs {
		if value == nil {
			continue
		}
		if isReelCorePartKey(key) && !reelPartConfigCompleteValue(key, value) {
			continue
		}
		if cfg, ok := value.(map[string]any); ok && len(cfg) == 0 {
			continue
		}
		merged[key] = value
	}
	return merged
}
