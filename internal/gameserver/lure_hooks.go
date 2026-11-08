package gameserver

// 拟饵钩位。
//
// 拟饵实例的钩子不在竿的 f5 组件表里：它们存放在拟饵自身 f0 的
// gpoaffnjbdo 字段（args(60)，元素即钩类 agedjipcnpe/flgcebbcehb）。
// 因此 15/1、15/2 必须改实例字段；按竿的装配路径写 f5/rig_component
// 只会把钩从背包摘走，拟饵仍渲染出厂钩——表现为"拟饵更换不了钓钩"。

import (
	"context"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

const (
	// lureWireClass 是拟饵（品类 17）的 wire 类。
	lureWireClass = "ioombppcnlh"
	// lureHooksAttr 是拟饵 f0 的出厂钩数组字段名。
	lureHooksAttr = "gpoaffnjbdo"
	// lureHookArgsCode 是出厂钩数组容器的 args code。
	lureHookArgsCode = "60"
	// lureHookIndexBase 是钩位规范索引起点（与竿钩位 250+ 序号同族）。
	lureHookIndexBase = 250
)

// isLureRow 判断物品行是否为拟饵实例。
func isLureRow(row *store.Item) bool {
	return row != nil && row.WireClass == lureWireClass
}

// lureHookFieldIndex 返回拟饵钩数组字段下标。
func lureHookFieldIndex(class string) int {
	return fieldIdxByCS(class, lureHooksAttr)
}

// lureHookItemGUID 取出厂钩数组第 i 项的 GUID。
func lureHookItemGUID(items []*proto.Value, i int) string {
	if i < 0 || i >= len(items) || items[i] == nil {
		return ""
	}
	for k := range items[i].Fields {
		if items[i].Fields[k].Op == "GUID16" && items[i].Fields[k].Hex != "" {
			return items[i].Fields[k].Hex
		}
	}
	return ""
}

// lureHookItemGUIDs 按顺序列出出厂钩 GUID。
func lureHookItemGUIDs(items []*proto.Value) []string {
	out := make([]string, 0, len(items))
	for i := range items {
		if g := lureHookItemGUID(items, i); g != "" {
			out = append(out, g)
		}
	}
	return out
}

// lureActiveOrdinal 返回拟饵当前选中的钩序号。
// f1 保存活动钩下标（lureActiveHookGUID 同口径），越界回退到 0。
func lureActiveOrdinal(fields []proto.Field, n int) int {
	if n <= 0 {
		return 0
	}
	if len(fields) > 1 && fields[1].Num != nil {
		if v := int(u64Of(fields[1].Num)); v >= 0 && v < n {
			return v
		}
	}
	return 0
}

// lureHookTargetOrdinal 选出本次要替换或插入的钩序号。
// 客户端尾部 int 优先：250+序号为规范索引；落在 [0,n) 视为 0 基序号。
// 两者都不匹配时落在当前活动钩上，钩位为空时追加。
func lureHookTargetOrdinal(extra int64, n int, active int) int {
	if n <= 0 {
		return 0
	}
	if extra >= lureHookIndexBase && extra < lureHookIndexBase+int64(n) {
		return int(extra - lureHookIndexBase)
	}
	if extra >= 0 && extra < int64(n) {
		return int(extra)
	}
	if active >= 0 && active < n {
		return active
	}
	return 0
}

// setLureChildRef 把钩对象写成拟饵子部件：父引用指向拟饵、容器枚举为 rig(4)。
func setLureChildRef(v *proto.Value, parent string) {
	if v == nil || v.Kind != proto.KindObj {
		return
	}
	if i := fieldIdxByCS(v.Class, csParent); i >= 0 && i < len(v.Fields) &&
		(v.Fields[i].Op == "OPTGUID" || v.Fields[i].Op == "GUID16") {
		v.Fields[i].Hex = parent
	}
	if i := fieldIdxByCS(v.Class, csSlotEnum); i >= 0 && i < len(v.Fields) && fieldOpAt(v.Class, i) == "BYTE" {
		v.Fields[i].Num = ptrU64(4)
	}
}

// dropLureHookRows 清掉拟饵钩位在 rig_component 里可能残留的行。
// 拟饵钩的真源是实例 f0；索引行只由旧装配路径写入，留着会变成幽灵组件。
func (s *Server) dropLureHookRows(ctx context.Context, lureGUID string) {
	rows, err := s.DB.ListRigComponents(ctx, lureGUID)
	if err != nil {
		return
	}
	for _, rc := range rows {
		if rc.Slot == catHook {
			_ = s.DB.DeleteRigComponentAt(ctx, lureGUID, rc.Slot, rc.Ordinal)
		}
	}
}

// lureHookMount 把背包里的钩装到拟饵的某个钩位上（15/1）。
// 旧钩若是有独立物品行的实例则退回背包；出厂钩只有子对象，直接丢弃。
func (s *Server) lureHookMount(ctx context.Context, uid int64, lureGUID, hookGUID string, extra int64) *rigSetResult {
	res := &rigSetResult{UpdatedTrees: []*proto.Value{}}
	lureRow, err := s.DB.GetItemByGUID(ctx, lureGUID)
	if err != nil || !isLureRow(lureRow) || lureRow.UID != uid {
		s.log.Warn("拟饵换钩被拒：拟饵不属于当前玩家", "lure", shortGUID(lureGUID), "uid", uid)
		res.Rejected = true
		return res
	}
	hookRow, err := s.DB.GetItemByGUID(ctx, hookGUID)
	if err != nil || hookRow == nil || hookRow.UID != uid || hookRow.Container != "backpack" || hookRow.RigGUID != "" {
		s.log.Warn("拟饵换钩被拒：鱼钩不属于当前玩家或不在背包", "hook", shortGUID(hookGUID), "uid", uid)
		res.Rejected = true
		return res
	}
	if cat, ok := s.itemCategoryOf(ctx, hookRow); !ok || cat != catHook {
		s.log.Warn("拟饵换钩被拒：不是鱼钩", "item", shortGUID(hookGUID), "cat", cat)
		res.Rejected = true
		return res
	}
	if s.itemBrokenForMount(ctx, hookRow) {
		s.log.Warn("拟饵换钩被拒：钩已损坏", "item", shortGUID(hookGUID))
		res.Rejected = true
		return res
	}

	fields, err := FieldsFromJSON(lureRow.FieldsJSON)
	if err != nil {
		res.Rejected = true
		return res
	}
	idx := lureHookFieldIndex(lureRow.WireClass)
	if idx < 0 || idx >= len(fields) {
		res.Rejected = true
		return res
	}
	list := fields[idx].Val
	if list == nil || list.Kind != proto.KindArgs {
		list = proto.Args(proto.S(lureHookArgsCode))
	}
	items := append([]*proto.Value(nil), list.Items...)
	ordinal := lureHookTargetOrdinal(extra, len(items), lureActiveOrdinal(fields, len(items)))

	tree, terr := s.itemTree(ctx, hookRow)
	if terr != nil || tree == nil || tree.Kind != proto.KindObj {
		res.Rejected = true
		return res
	}
	setLureChildRef(tree, lureGUID)

	oldGUID := ""
	if ordinal < len(items) {
		oldGUID = lureHookItemGUID(items, ordinal)
		items[ordinal] = tree
	} else {
		items = append(items, tree)
		ordinal = len(items) - 1
	}
	list.Items = items
	fields[idx] = proto.Field{Op: "VAL", Val: list}
	s.saveItemFields(ctx, lureRow, fields)

	// 钩本体转为已装配：父引用指向拟饵，背包不再列出。
	_ = s.DB.SetItemRigLink(ctx, hookGUID, lureGUID, catHook)
	if fresh, ferr := s.DB.GetItemByGUID(ctx, hookGUID); ferr == nil && fresh != nil {
		s.setWireParent(ctx, fresh, lureGUID)
		if t, terr2 := s.itemTree(ctx, fresh); terr2 == nil {
			res.UpdatedTrees = append(res.UpdatedTrees, t)
		}
	}

	// 旧钩返还：只有带独立物品行的实例才需要退回背包。
	if oldGUID != "" && oldGUID != hookGUID {
		res.ReplacedGUID = oldGUID
		if prev, perr := s.DB.GetItemByGUID(ctx, oldGUID); perr == nil && prev != nil {
			s.returnToBackpack(ctx, prev)
			if fresh, ferr := s.DB.GetItemByGUID(ctx, oldGUID); ferr == nil && fresh != nil {
				if t, terr2 := s.itemTree(ctx, fresh); terr2 == nil {
					res.UpdatedTrees = append(res.UpdatedTrees, t)
				}
			}
		}
	}

	s.dropLureHookRows(ctx, lureGUID)
	s.setLureHookSelection(ctx, lureGUID, hookGUID)
	if lure, lerr := s.DB.GetItemByGUID(ctx, lureGUID); lerr == nil && lure != nil {
		if t, terr2 := s.itemTree(ctx, lure); terr2 == nil {
			res.UpdatedTrees = append(res.UpdatedTrees, t)
		}
	}
	res.MountedGUID = hookGUID
	s.log.Info("拟饵换钩", "lure", shortGUID(lureGUID), "hook", shortGUID(hookGUID), "ordinal", ordinal)
	return res
}

// lureHookRemove 从拟饵钩位移除一颗钩（15/2）。
// hookGUID 非空时按 GUID 定位，否则按 extra/活动钩定位。
func (s *Server) lureHookRemove(ctx context.Context, uid int64, lureGUID, hookGUID string, extra int64) *rigDetachResult {
	lureRow, err := s.DB.GetItemByGUID(ctx, lureGUID)
	if err != nil || !isLureRow(lureRow) || lureRow.UID != uid {
		s.log.Warn("拟饵拆钩被拒：拟饵不属于当前玩家", "lure", shortGUID(lureGUID), "uid", uid)
		return nil
	}
	fields, err := FieldsFromJSON(lureRow.FieldsJSON)
	if err != nil {
		return nil
	}
	idx := lureHookFieldIndex(lureRow.WireClass)
	if idx < 0 || idx >= len(fields) {
		return nil
	}
	list := fields[idx].Val
	if list == nil || list.Kind != proto.KindArgs || len(list.Items) == 0 {
		return nil
	}
	items := list.Items
	ordinal := -1
	if hookGUID != "" {
		for i := range items {
			if lureHookItemGUID(items, i) == hookGUID {
				ordinal = i
				break
			}
		}
	}
	if ordinal < 0 {
		ordinal = lureHookTargetOrdinal(extra, len(items), lureActiveOrdinal(fields, len(items)))
	}
	if ordinal < 0 || ordinal >= len(items) {
		return nil
	}

	removedGUID := lureHookItemGUID(items, ordinal)
	items = append(items[:ordinal], items[ordinal+1:]...)
	list.Items = items
	fields[idx] = proto.Field{Op: "VAL", Val: list}
	s.saveItemFields(ctx, lureRow, fields)

	res := &rigDetachResult{CompGUID: removedGUID}
	if removedGUID != "" {
		if row, rerr := s.DB.GetItemByGUID(ctx, removedGUID); rerr == nil && row != nil && row.UID == uid {
			s.returnToBackpack(ctx, row)
			if fresh, ferr := s.DB.GetItemByGUID(ctx, removedGUID); ferr == nil && fresh != nil {
				if t, terr := s.itemTree(ctx, fresh); terr == nil {
					res.ReturnedTree = t
				}
			}
		}
	}
	s.dropLureHookRows(ctx, lureGUID)
	// 选中钩关联指向旧钩时必须让位，否则上鱼判定仍按旧钩（甚至是不存在的 GUID）。
	s.setLureHookSelection(ctx, lureGUID, "")

	// 活动钩被拆后下标要留在有效范围内，否则下次读 f0 会越界。
	if len(items) > 0 {
		if len(fields) > 1 && fields[1].Num != nil {
			if idx := fieldIdxByCS(lureRow.WireClass, "nkogmkobfng"); idx >= 0 && idx < len(fields) &&
				u64Of(fields[1].Num) >= uint64(len(items)) {
				fields[idx].Num = ptrU64(0)
				s.saveItemFields(ctx, lureRow, fields)
			}
		}
	}
	s.log.Info("拟饵拆钩", "lure", shortGUID(lureGUID), "hook", shortGUID(removedGUID), "ordinal", ordinal)
	return res
}

// setLureHookSelection 同步竿上拟饵的 bait_hook 关联。
// 关联行以竿 GUID 为主键、key=bait_hook:<拟饵GUID>、value=当前钩 GUID；
// hookGUID 为空时删除关联，由后续 baitHookAssignments 按新活动钩重建。
func (s *Server) setLureHookSelection(ctx context.Context, lureGUID, hookGUID string) {
	key := "bait_hook:" + lureGUID
	var err error
	if hookGUID == "" {
		_, err = s.DB.SQL.ExecContext(ctx, `DELETE FROM rod_setting WHERE key=?`, key)
	} else {
		_, err = s.DB.SQL.ExecContext(ctx, `UPDATE rod_setting SET value=? WHERE key=?`, hookGUID, key)
	}
	if err != nil {
		s.log.Warn("拟饵钩位关联同步失败", "lure", shortGUID(lureGUID), "hook", shortGUID(hookGUID), "err", err)
	}
}
