package gameserver

import (
	"context"
	"strings"
	"time"

	"rf4server/internal/store"
)

const largeHookSmallFishRatio = 0.25

// hookFamilyFor 把 item_id 前缀归到稳定的钩型族，供管理台筛选。厂牌
// 仍保留在 Brand 字段；钩型族和厂牌不是同一层数据。
func hookFamilyFor(itemID string) string {
	id := strings.ToLower(strings.TrimSpace(itemID))
	switch {
	case strings.HasPrefix(id, "triple_"):
		return "treble"
	case strings.HasPrefix(id, "jhead_"):
		return "jighead"
	case strings.HasPrefix(id, "offset_"), strings.HasPrefix(id, "woffset_"):
		return "offset"
	case strings.HasPrefix(id, "mhook_"), strings.HasPrefix(id, "mdhook_"), strings.HasPrefix(id, "gmhook_"):
		return "marine"
	case strings.HasPrefix(id, "c_hook_"):
		return "carp"
	case strings.HasPrefix(id, "clas_"), strings.HasPrefix(id, "orig_"):
		return "classic"
	default:
		return "other"
	}
}

// SyncHookBiteProfiles 从静态 item_def 同步真实钩子物理数据。调参字段在冲突
// 更新时保留，只有型号、尺寸、拉力等真实数据会被刷新。
func (s *Server) SyncHookBiteProfiles(ctx context.Context) (int, error) {
	return s.syncHookBiteProfiles(ctx)
}

func (s *Server) syncHookBiteProfiles(ctx context.Context) (int, error) {
	if s == nil || s.DB == nil {
		return 0, nil
	}
	defs, err := s.DB.ListItemDefsByCategory(ctx, catHook)
	if err != nil {
		return 0, err
	}
	now := float64(time.Now().Unix())
	n := 0
	for _, def := range defs {
		if def == nil || strings.TrimSpace(def.ItemID) == "" {
			continue
		}
		p := store.HookBiteProfile{
			ItemID:      def.ItemID,
			NumericID:   def.NumericID,
			Size:        itemDefInt(def, gearAttrHookSize),
			LengthMM:    itemDefF32(def, gearAttrHookLen),
			StrengthKg:  itemDefF32(def, gearAttrMaxLoad),
			WireWeightG: itemDefF32(def, "ijbjficbhog"),
			Quality:     itemDefInt(def, "obifdihmcfn"),
			Family:      hookFamilyFor(def.ItemID),
			Brand:       def.Brand,
			UpdatedTS:   now,
		}
		if p.LengthMM <= 0 && p.Size >= 0 {
			p.LengthMM = 1.5 + 0.35*float64(p.Size)
		}
		if err := s.DB.UpsertHookBiteProfile(ctx, p); err != nil {
			return n, err
		}
		n++
	}
	if err := s.reloadHookBiteProfiles(ctx); err != nil {
		return n, err
	}
	return n, nil
}

func (s *Server) reloadHookBiteProfiles(ctx context.Context) error {
	if s == nil || s.DB == nil {
		return nil
	}
	rows, err := s.DB.ListHookBiteProfiles(ctx)
	if err != nil {
		return err
	}
	next := make(map[string]store.HookBiteProfile, len(rows))
	for _, p := range rows {
		next[p.ItemID] = p
	}
	s.hookProfileMu.Lock()
	s.hookProfiles = next
	s.hookProfileMu.Unlock()
	return nil
}

func (s *Server) hookBiteProfileFor(itemID string) (store.HookBiteProfile, bool) {
	if s == nil || strings.TrimSpace(itemID) == "" {
		return store.HookBiteProfile{}, false
	}
	s.hookProfileMu.RLock()
	p, ok := s.hookProfiles[itemID]
	s.hookProfileMu.RUnlock()
	return p, ok
}

// applyHookBiteProfile 只对基础几何概率做型号级微调；所有倍率默认 1.0，
// 缺档案时完全保持旧行为。small_fish_floor 和 over_capacity_hold_floor
// 是管理台显式配置的兜底值，0 表示不启用。
func applyHookBiteProfile(weightG int64, spec gearPhysicsSpec, p store.HookBiteProfile, solid, hold float64) (float64, float64) {
	if weightG <= 0 || !spec.HookPresent {
		return solid, hold
	}
	// 数据库默认值是 1.0；0 是后台允许的有效调参值，必须真正关闭对应概率。
	// 这里不接受负数，避免脏数据把概率反向放大。
	if p.SolidScale >= 0 {
		solid *= p.SolidScale
	}
	if p.HoldScale >= 0 {
		hold *= p.HoldScale
	}
	gape := hookGapeG(spec)
	if gape > 0 {
		r := float64(weightG) / gape
		if r < largeHookSmallFishRatio && p.SmallFishFloor > 0 && solid < p.SmallFishFloor {
			solid = p.SmallFishFloor
		}
		if r > 1 && p.OverCapacityHoldFloor > 0 && hold < p.OverCapacityHoldFloor {
			hold = p.OverCapacityHoldFloor
		}
	}
	return clampUnit(solid), clampUnit(hold)
}
