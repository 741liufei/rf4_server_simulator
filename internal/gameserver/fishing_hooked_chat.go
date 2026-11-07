package gameserver

import (
	"fmt"
	"strings"
)

// takeHookedChatFishLocked 在 st.mu 内按鱼领取一次播报；Hooked 被拒收重置时保留标记。
func (st *FishState) takeHookedChatFishLocked() (RolledFish, bool) {
	fish := st.Pending
	if fish == nil || fish.FishGUID == "" || !st.CastActive || st.Resolved || !st.Hooked ||
		st.hookedChatFishGUID == fish.FishGUID {
		return RolledFish{}, false
	}
	st.hookedChatFishGUID = fish.FishGUID
	return *fish, true
}

// notifyFishHooked 只通知中鱼玩家，使用本根竿已经生成的鱼，不参与判鱼或结算。
func (s *Server) notifyFishHooked(sess *Session, fish RolledFish, rod string) bool {
	if s == nil || sess == nil || fish.FishGUID == "" || fish.Species == "" || fish.WeightG <= 0 ||
		s.tunableF("fish_hooked_chat", 0) <= 0 {
		return false
	}
	if rod == "" {
		rod = fish.GearGUID
	}
	if rod == "" {
		return false
	}
	text := fmt.Sprintf("[上钩] %s：%s %d.%03dkg", s.hookedRodLabel(sess.Userid, rod),
		s.speciesZhName(s.dbCtx(), fish.Species), fish.WeightG/1000, fish.WeightG%1000)
	return sess.Push(24, 27, buildChatEchoPush("", text, uint64(sess.Userid), 1)) != 0
}

func (s *Server) hookedRodLabel(uid int64, rod string) string {
	label := fmt.Sprintf("鱼竿（%s）", shortGUID(rod))
	if s.DB == nil {
		return label
	}
	ctx := s.dbCtx()
	shortcuts, err := s.DB.ListShortcuts(ctx, uid)
	if err == nil {
		// ListShortcuts 已按槽位排序；同一竿绑定多个快捷键时取最小编号。
		for _, shortcut := range shortcuts {
			if strings.EqualFold(shortcut.GUID, rod) {
				return fmt.Sprintf("快捷栏%d号竿", shortcut.Slot)
			}
		}
	}
	var name string
	if err := s.DB.SQL.QueryRowContext(ctx, `
		SELECT COALESCE(NULLIF(r.name,''),NULLIF(c.name_zh,''),NULLIF(c.name,''),i.item_id,'')
		FROM item i
		LEFT JOIN rig r ON r.guid=i.guid AND r.uid=i.uid
		LEFT JOIN item_catalog c ON c.item_id=i.item_id
		WHERE i.uid=? AND i.guid=?`, uid, rod).Scan(&name); err == nil && name != "" {
		return fmt.Sprintf("%s（%s）", name, shortGUID(rod))
	}
	return label
}
