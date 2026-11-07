package gameserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

const (
	catLeaderLock = int64(14)
	catWireLeader = int64(15)
)

// gearDamageEvent 是 item_damage_log.event 的稳定取值。
const (
	damageEventWear                     = "wear"
	damageEventLineBreak                = "line_break"
	damageEventLineExhausted            = "line_exhausted"
	damageEventLostOnLineBreak          = "lost_on_line_break"
	damageEventLineCut                  = "line_cut"
	damageEventLostOnLineCut            = "lost_on_line_cut"
	damageEventDetachedOnRodBreak       = "detached_on_rod_break"
	damageEventDetachedOnReelBreak      = "detached_on_reel_break"
	damageEventDetachedOnComponentBreak = "detached_on_component_break"
	damageEventRodBreak                 = "rod_break"
	damageEventReelOverload             = "reel_overload"
	damageEventComponentBreak           = "component_break"
	damageEventRepair                   = "repair"
)

type gearBreakOutcome struct {
	LineBroken      bool
	ReelDamaged     bool
	ReelBroken      bool // 轮子耐久本次被打到 0：已损坏，须修理才能再抛投。
	FrictionWorn    bool // 摩擦片(cat9)磨穿：泄力失效，最大泄力归零，需换摩擦片。
	ComponentBroken bool
	BreakPart       string // hook / leader / line / reel / rod / component
	LineLossM       int64
	BrokenGUID      string
	RemovedGUIDs    []string
	// StackRefreshGUIDs 是被删计件事件的背包来源堆（或同物品号剩余堆），
	// 装配拆堆时服务端数量已减但客户端不知道，删除时需要一并刷新。
	StackRefreshGUIDs []string
	UpdatedTrees      []*proto.Value
}

type gearBreakDecision struct {
	Part     string
	GUID     string
	Category int64
	// ReelSlip 区分渔轮两种损坏：true=泄力打滑（磨摩擦片 csDragForce），
	// false=锁轮超载（磨锁轮装置/本体耐久）。
	ReelSlip bool
}

// lineBreakTerminalCategories 是断线时随线一起丢失的终端组件。
// 路亚也在其中：线断后拟饵、钩、前导和饵都不可能留在竿上。
var lineBreakTerminalCategories = map[int64]bool{
	catHook: true, catLead: true, catLeaderLock: true, catWireLeader: true,
	catLure: true, catBait: true, catFloat: true,
}

// 断件不在钓组模板里时的兜底集合：子线断只损失钩组，浮漂挂在上方主线，
// 不应跟着消失；铅芯前导(cat57)在鲤族钓组里位于子线上游，同样不能列入，
// 只有铅芯自己断或主线断时才丢。模板能定位断件时一律走
// downstreamLossCategories，不使用这张表。
var leaderBreakLossCategories = map[int64]bool{
	catHook: true, catLead: true, catLeaderLock: true, catWireLeader: true,
	catLure: true, catBait: true,
}

// terminalConsumableCategories 是挂在钓组最末端的消耗件，无论断在哪一节都会
// 随下游一起丢失。浮漂、铅坠、铅芯、子线等上游件不在此列。
var terminalConsumableCategories = map[int64]bool{
	catHook: true, catLure: true, catBait: true,
}

// mainLineLossCategories 是主线崩断/剪断时随线一起丢失的全部品类。
// 不能只盯着钩和前导：铅坠、食笼、PVA、线组主体、铅芯前导这些同样挂在线上，
// 实机就出现过剪线后沉子原地留下的情况。
// 数据源用仓库自己的槽位表：slotAccepts 说明主线/前导/线组主体上能装什么，
// rigSlotTemplates 说明各钓组把哪些品类挂到线上，两者取闭包后再并上
// 历史实现已确认会丢失的终端件。竿体件（竿坯/竿柄/竿梢/渔轮/主线/灵敏梢）不进入。
var mainLineLossCategories = buildMainLineLossCategories()

func buildMainLineLossCategories() map[int64]bool {
	lost := map[int64]bool{}
	// 从主线、前导、钢丝前导出发沿槽位表向下扩展。
	queue := []int64{catLine, catLead, catWireLeader}
	visited := map[int64]bool{}
	for len(queue) > 0 {
		slot := queue[0]
		queue = queue[1:]
		if visited[slot] {
			continue
		}
		visited[slot] = true
		for cat := range slotAccepts[slot] {
			lost[cat] = true
			queue = append(queue, cat)
		}
	}
	// 钓组模板里的组件品类一律挂在线上（含 47 底钓组主体、57 铅芯前导、44 线组漂）。
	for _, tmpl := range rigSlotTemplates {
		for _, comp := range tmpl {
			for _, cat := range comp.Cats {
				lost[cat] = true
			}
		}
	}
	for cat := range lineBreakTerminalCategories {
		lost[cat] = true
	}
	delete(lost, catLine)
	delete(lost, catReel)
	return lost
}

// downstreamLossCategories 按断件在当前钓组模板中的位置给出"下游丢失"品类。
//
// rigSlotTemplates 的数组顺序就是主线往下的装配顺序，例如 24 号鲤族钓组是
// 47 食笼 → 57 铅芯 → 29 铅坠 → 13 子线 → 12 钩。旧实现把同一张品类表同时
// 用于子线、连接件和铅芯断裂，导致断子线时把上游仍在主线上的铅芯一起删掉。
// 这里只取断件位置及其之后的品类，再并上永远挂在末端的消耗件（钩/饵/拟饵）。
// 模板里同品类多槽位（如 13 出现两次）仍按品类处理：现有 rig_component
// 只按 slot 分类，没有可靠的上下游序号，不去臆造顺序。
func downstreamLossCategories(rigType, breakCategory int64) map[int64]bool {
	lost := map[int64]bool{breakCategory: true}
	for cat := range terminalConsumableCategories {
		lost[cat] = true
	}
	tmpl := rigSlotTemplates[rigType]
	pos := -1
	for i, comp := range tmpl {
		for _, cat := range comp.Cats {
			if cat == breakCategory {
				pos = i
				break
			}
		}
		if pos >= 0 {
			break
		}
	}
	if pos < 0 {
		// 断件不在模板（如独立连接件 14）或钓组类型未知：沿用窄集合，
		// 其中已不含上游的铅芯。
		for cat := range leaderBreakLossCategories {
			lost[cat] = true
		}
		return lost
	}
	for _, comp := range tmpl[pos:] {
		for _, cat := range comp.Cats {
			lost[cat] = true
		}
	}
	return lost
}

// sourceLineCategories 是可上报的线源承力件：主线、前导/钢丝前导，以及已
// 进入承力链的铅芯前导(cat57)。断任何一项都交给 resolveGearBreak 按最弱环
// 裁决；breakLineTx 再按实际品类决定是否扣主线长度（57 不扣）。
var sourceLineCategories = map[int64]bool{
	catLine: true, catLead: true, catLeaderLock: true, catWireLeader: true,
	gearCatLeadcore: true,
}

// terminalBreakCategories 是不承力的终端消耗件：拟饵、饵、浮漂。
// 这类件由客户端在打斗中直接上报断裂，上报可以采信：没有作弊收益，误判
// 成本也只是消耗件本身。钩已经进入承力链，不能在物理裁决前无条件采信，
// 否则强钩配弱线只会丢钩、不会判弱线。
var terminalBreakCategories = map[int64]bool{
	catLure: true, catBait: true, catFloat: true,
}

func ptrFloat64(v float64) *float64 { return &v }
func ptrInt64(v int64) *int64       { return &v }

// itemLengthField 读取鱼线实例的剩余长度（字段 ljnnpokapig，单位米）。
func itemLengthField(row *store.Item, fields []proto.Field) int64 {
	if row == nil {
		return 0
	}
	if idx := fieldIdxByCS(row.WireClass, csQty); idx >= 0 && idx < len(fields) {
		return int64(u64Of(fields[idx].Num))
	}
	if row.Qty > 0 {
		return row.Qty
	}
	return 0
}

func (s *Server) itemLength(ctx context.Context, row *store.Item) int64 {
	if row == nil {
		return 0
	}
	if fields, err := FieldsFromJSON(row.FieldsJSON); err == nil {
		if idx := fieldIdxByCS(row.WireClass, csQty); idx >= 0 && idx < len(fields) {
			return int64(u64Of(fields[idx].Num))
		}
	}
	if def, err := s.itemDefForRow(ctx, row); err == nil && def != nil {
		return lineLengthM(def)
	}
	return 0
}

func setItemLengthField(row *store.Item, fields []proto.Field, length int64) bool {
	if row == nil || length < 0 {
		return false
	}
	idx := fieldIdxByCS(row.WireClass, csQty)
	if idx < 0 || idx >= len(fields) {
		return false
	}
	fields[idx].Num = ptrU64(uint64(length))
	return true
}

// lineBreakLoss 返回本次断线损失的线长。
// 客户端 17/9 只上报张力/冲量，没有断线长度字段；requested<=0 时按剩余长度的
// 1..80% 随机扣减，保证不会把剩余长度扣成负数。
func lineBreakLoss(remaining, requested int64) int64 {
	if remaining <= 0 {
		return 0
	}
	if requested > 0 {
		if requested > remaining {
			return remaining
		}
		return requested
	}
	maxLoss := remaining * 80 / 100
	if maxLoss < 1 {
		maxLoss = 1
	}
	return 1 + rand.Int63n(maxLoss)
}

func (s *Server) itemBrokenForMount(ctx context.Context, row *store.Item) bool {
	if row == nil || row.Durability <= 0 {
		return true
	}
	def, err := s.itemDefForRow(ctx, row)
	if err != nil || def == nil || def.Category != catLine {
		return false
	}
	return s.itemLength(ctx, row) <= 0
}

// breakLineOrRod 处理 17/9：客户端明确上报断件，服务端在确认归属后按上报件
// 处置。只有上报件缺失、空/zero GUID 或出厂内嵌件时才回退到最弱环裁决；
// 已存在但不受支持的装配品类视为无效上报，避免任意物品触发断件。
func (s *Server) breakLineOrRod(ctx context.Context, uid int64, rodGUID, reportedGUID string) (*gearBreakOutcome, error) {
	rodGUID = strings.TrimSpace(rodGUID)
	reportedGUID = strings.TrimSpace(reportedGUID)
	report, err := s.validateBreakReport(ctx, uid, rodGUID, reportedGUID)
	if err != nil {
		return nil, err
	}
	if report.terminalCategory != 0 {
		return s.breakComponentTx(ctx, uid, rodGUID, reportedGUID, report.terminalCategory)
	}
	// 只有客户端明确上报竿 GUID（或上报件本身是竿类）时才直接断竿。
	// 空/zero GUID 表示“未识别断件”，继续走下面的最弱环回退，
	// 物理数据也缺失时 resolveGearBreak 才兜底判竿。
	if reportedGUID == rodGUID || report.category == gearCatRod {
		return s.breakRodTx(ctx, uid, rodGUID, rodGUID)
	}
	if report.category != 0 {
		if s.log != nil {
			s.log.Info("17/9 按客户端上报断件", "rod", guidShort(rodGUID),
				"reported", guidShort(reportedGUID), "category", report.category)
		}
		switch {
		case report.category == catHook:
			return s.breakComponentTx(ctx, uid, rodGUID, reportedGUID, catHook)
		case report.category == gearCatReel:
			return s.damageReelLockOverload(ctx, uid, rodGUID, reportedGUID)
		case sourceLineCategories[report.category]:
			return s.breakLineTx(ctx, uid, rodGUID, reportedGUID, report.category)
		}
	}
	// 空/zero 或出厂内嵌件：回退到最弱环裁决。
	dec, err := s.resolveGearBreak(ctx, uid, rodGUID, reportedGUID)
	if err != nil {
		return nil, err
	}
	switch dec.Part {
	case "leader", "line", "leadcore", "connector":
		// 前导/子线、铅芯前导、连接件都是终端承力件，断了走断子线逻辑：
		// 主线长度不动，主线以下的终端件随断件丢失。
		return s.breakLineTx(ctx, uid, rodGUID, dec.GUID, dec.Category)
	case "hook":
		// 钩被拉直/拉断：摘除该终端件，不动主线长度。
		return s.breakComponentTx(ctx, uid, rodGUID, dec.GUID, dec.Category)
	case "reel":
		return s.damageReelLockOverload(ctx, uid, rodGUID, dec.GUID)
	default:
		brokenGUID := dec.GUID
		if report.embedded {
			brokenGUID = reportedGUID
		}
		return s.breakRodTx(ctx, uid, rodGUID, brokenGUID)
	}
}

type validatedBreakReport struct {
	terminalCategory int64
	category         int64
	embedded         bool
}

// validateBreakReport 只接受本人的竿和该竿上的受支持部件。终端件走独立摘除。
func (s *Server) validateBreakReport(ctx context.Context, uid int64, rodGUID, reportedGUID string) (validatedBreakReport, error) {
	rod, err := s.DB.GetItemByGUID(ctx, rodGUID)
	if err != nil || rod == nil || rod.UID != uid {
		return validatedBreakReport{}, sql.ErrNoRows
	}
	rodDef, err := s.itemDefForRow(ctx, rod)
	if err != nil || rodDef == nil || rodDef.Category != gearCatRod {
		return validatedBreakReport{}, sql.ErrNoRows
	}
	if reportedGUID == "" || reportedGUID == zeroGUID || reportedGUID == rodGUID {
		return validatedBreakReport{}, nil
	}

	part, err := s.DB.GetItemByGUID(ctx, reportedGUID)
	if errors.Is(err, sql.ErrNoRows) {
		// 出厂竿件只存在于母竿内，继续交由整组承力规则裁决。
		tree, treeErr := s.ItemToTree(ctx, rod)
		if treeErr != nil {
			return validatedBreakReport{}, treeErr
		}
		for i := 0; i < 3; i++ {
			if child := tree.Field(i).Val; child != nil &&
				child.Field(fieldIdxByCS(child.Class, csGUID)).Hex == reportedGUID {
				return validatedBreakReport{embedded: true}, nil
			}
		}
		return validatedBreakReport{}, sql.ErrNoRows
	}
	if err != nil || part == nil || part.UID != uid {
		return validatedBreakReport{}, sql.ErrNoRows
	}
	def, err := s.itemDefForRow(ctx, part)
	if err != nil || def == nil {
		return validatedBreakReport{}, sql.ErrNoRows
	}
	terminal := terminalBreakCategories[def.Category]
	// 钩不再是无条件采信的终端件，但仍是客户端会主动上报的合法断件：
	// 只允许终端件和承力链上可判定的品类。已确认挂在该竿上的其他品类
	// 不能冒充断件，否则任意背包/装饰物品都可能触发真实损坏。
	if !terminal && !sourceLineCategories[def.Category] &&
		def.Category != catHook && def.Category != gearCatReel {
		return validatedBreakReport{}, errors.New("断件类型不支持")
	}

	fields, err := FieldsFromJSON(rod.FieldsJSON)
	if err != nil {
		return validatedBreakReport{}, err
	}
	mounted, err := breakReportMounted(ctx, s.DB.SQL, rod, fields, part, def.Category)
	if err != nil {
		return validatedBreakReport{}, err
	}
	if !mounted {
		return validatedBreakReport{}, errors.New("断件不在该钓组中")
	}
	if terminal {
		return validatedBreakReport{terminalCategory: def.Category, category: def.Category}, nil
	}
	return validatedBreakReport{category: def.Category}, nil
}

type breakReportQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func breakReportMounted(ctx context.Context, db breakReportQueryer, rod *store.Item, fields []proto.Field, part *store.Item, category int64) (bool, error) {
	if part.RigGUID != "" && part.RigGUID != rod.GUID {
		return false, nil
	}
	if name, dedicated := categorySlotField[category]; dedicated {
		if idx := fieldIdxByCS(rod.WireClass, name); idx >= 0 && idx < len(fields) && fields[idx].GuidHex() == part.GUID {
			return true, nil
		}
	} else {
		for _, comp := range rigCompsOf(protoFieldAt(fields, rod.WireClass, csRigObject)) {
			if comp.GUID == part.GUID && comp.Category == category {
				return true, nil
			}
		}
	}
	// 兼容尚未同步到竿快照的钓组投影。
	var mounted bool
	err := db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM rig_component WHERE rig_guid=? AND slot=? AND guid=?)`, rod.GUID, category, part.GUID,
	).Scan(&mounted)
	return mounted, err
}

// ---- 断件后的残留清理 ----
//
// 断钩时挂在钩上的饵必须一起离组；断竿/断轮时整条钓组都要退回背包。
// 拆分子实例（从背包大堆拆出的子件）不能在背包里再变成独立一堆，否则看起来
// 像复制物品：这里把数量并回来源堆并删除子实例。

// refundSplitChildTx 是 refundableSplitChild 的事务内版本。返回来源堆 GUID；
// ok=false 表示不是可退款的拆分子实例，调用方按普通物品退回背包。
func (s *Server) refundSplitChildTx(ctx context.Context, tx *sql.Tx, row *store.Item, qty int64) (string, bool, error) {
	if row == nil {
		return "", false, nil
	}
	srcGUID := splitSourceGUID(row)
	if len(srcGUID) != 32 || srcGUID == row.GUID {
		return "", false, nil
	}
	src, err := s.DB.GetItemByGUIDTx(ctx, tx, srcGUID)
	if err != nil || src == nil {
		return "", false, nil
	}
	// 来源堆必须仍属于同一玩家、同一物品号且留在背包，才允许并回。
	if src.UID != row.UID || src.NumericID != row.NumericID ||
		src.Container != "backpack" || src.RigGUID != "" || src.Qty <= 0 {
		return "", false, nil
	}
	if qty < 1 {
		qty = 1
	}
	if err := s.setStackCountTx(ctx, tx, src, stackCountTx(ctx, tx, src)+qty); err != nil {
		return "", false, err
	}
	if err := s.DB.DeleteItemCascadeTx(ctx, tx, row.GUID); err != nil {
		return "", false, err
	}
	return srcGUID, true, nil
}

// detachComponentToBackpackTx 把完好组件摘回背包：清钓组引用、落字段并清
// rig_guid。拆分子实例不要走这里，先由 refundSplitChildTx 处理。
func (s *Server) detachComponentToBackpackTx(ctx context.Context, tx *sql.Tx, row *store.Item) error {
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return err
	}
	row.Container = "backpack"
	row.Slot = 0
	row.RigGUID = ""
	clearRigFieldsForBackpack(fields, row.WireClass)
	if err := updateItemFieldsTx(ctx, s.DB, tx, row, fields); err != nil {
		return err
	}
	return s.DB.DetachReturnToBackpackTx(ctx, tx, row.GUID)
}

// detachOnBreakTx 断竿/断轮/断钩时把完好组件摘回背包；拆分子实例并回来源堆。
// 返回需要刷新的来源堆 GUID（为空表示没有退款）。
func (s *Server) detachOnBreakTx(ctx context.Context, tx *sql.Tx, uid int64, rodGUID string, row *store.Item, event, reason, brokenGUID string) (string, error) {
	srcGUID, refunded, err := s.refundSplitChildTx(ctx, tx, row, baitQuantity(row))
	if err != nil {
		return "", err
	}
	if !refunded {
		if err := s.detachComponentToBackpackTx(ctx, tx, row); err != nil {
			return "", err
		}
	}
	if err := store.AddItemDamageLog(ctx, tx, store.ItemDamageLog{
		UID: uid, ItemGUID: row.GUID, ItemID: row.ItemID, Event: event,
		BeforeDurability: ptrFloat64(row.Durability), AfterDurability: ptrFloat64(row.Durability),
		BrokenGUID: brokenGUID,
		DetailJSON: damageDetailJSON(map[string]any{"rod_guid": rodGUID, "reason": reason}),
	}); err != nil {
		return "", err
	}
	return srcGUID, nil
}

// clearRodBaitSettingsTx 删除该竿上的自动补饵请求和饵-钩映射。断竿/断轮后
// 钓组已清空，这些记录继续留着会在修好竿第一次抛竿时把饵挂回去。
func clearRodBaitSettingsTx(ctx context.Context, tx *sql.Tx, rodGUID string) error {
	for _, pattern := range []string{"rebait:%", "bait_hook:%"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM rod_setting WHERE guid=? AND key LIKE ?`, rodGUID, pattern); err != nil {
			return err
		}
	}
	return nil
}

// clearRebaitForHookTx 清除会往该钩补饵的 rebait 记录：JSON 里带同一钩 GUID
// 的记录，以及按钩位序号记录（旧格式）的那一条。
func (s *Server) clearRebaitForHookTx(ctx context.Context, tx *sql.Tx, rodGUID, hookGUID string, comps map[int64]rigComp) error {
	keys := map[string]bool{}
	if hookGUID != "" {
		rows, err := tx.QueryContext(ctx, `SELECT key,value FROM rod_setting WHERE guid=? AND key LIKE 'rebait:%'`, rodGUID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var key, value string
			if err := rows.Scan(&key, &value); err != nil {
				rows.Close()
				return err
			}
			if !strings.HasPrefix(value, "{") {
				continue
			}
			var req rebaitRequest
			if json.Unmarshal([]byte(value), &req) == nil && req.HookGUID == hookGUID {
				keys[key] = true
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	if key, _, ok := rigCompKeyForGUID(comps, hookGUID); ok {
		keys[fmt.Sprintf("rebait:%d", rigCompOrdinal(key))] = true
	}
	for key := range keys {
		if _, err := tx.ExecContext(ctx, `DELETE FROM rod_setting WHERE guid=? AND key=?`, rodGUID, key); err != nil {
			return err
		}
	}
	return nil
}

// clearComponentBaitSettingsTx 处理终端件自身断裂：删掉它的 bait_hook 映射，
// 并清掉会把它补回同一钩的 rebait 记录，避免断掉的饵又被自动补一份。
func (s *Server) clearComponentBaitSettingsTx(ctx context.Context, tx *sql.Tx, rodGUID, compGUID string, comps map[int64]rigComp) error {
	hook := ""
	if err := tx.QueryRowContext(ctx, `SELECT value FROM rod_setting WHERE guid=? AND key=?`,
		rodGUID, "bait_hook:"+compGUID).Scan(&hook); err != nil && err != sql.ErrNoRows {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM rod_setting WHERE guid=? AND key=?`,
		rodGUID, "bait_hook:"+compGUID); err != nil {
		return err
	}
	if hook != "" {
		return s.clearRebaitForHookTx(ctx, tx, rodGUID, hook, comps)
	}
	if key, _, ok := rigCompKeyForGUID(comps, compGUID); ok {
		if _, err := tx.ExecContext(ctx, `DELETE FROM rod_setting WHERE guid=? AND key=?`,
			rodGUID, fmt.Sprintf("rebait:%d", rigCompOrdinal(key))); err != nil {
			return err
		}
	}
	return nil
}

// detachHookBaitsTx 断钩时把挂在断钩上的饵/拟饵一并摘回背包（或并回来源堆），
// 并删除对应 bait_hook 映射与自动补饵请求。没有显式映射的单钩钓组也按钩位
// 推断，避免留下"有饵无钩"。
func (s *Server) detachHookBaitsTx(ctx context.Context, tx *sql.Tx, uid int64, rodGUID, hookGUID string, comps map[int64]rigComp) (removed, stackRefresh []string, err error) {
	mapped := map[string]string{}
	rows, qerr := tx.QueryContext(ctx, `SELECT key,value FROM rod_setting WHERE guid=? AND key LIKE 'bait_hook:%'`, rodGUID)
	if qerr != nil {
		return nil, nil, qerr
	}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			rows.Close()
			return nil, nil, err
		}
		mapped[strings.TrimPrefix(key, "bait_hook:")] = value
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}

	hooks := rigCompKeysOfCategory(comps, catHook)
	singleHook := len(hooks) <= 1
	hookExists := map[string]bool{}
	for _, key := range hooks {
		hookExists[comps[key].GUID] = true
	}
	var baitGUIDs []string
	for _, cat := range []int64{catBait, catLure} {
		for _, key := range rigCompKeysOfCategory(comps, cat) {
			baitGUID := comps[key].GUID
			if baitGUID == "" {
				continue
			}
			mappedHook := mapped[baitGUID]
			switch {
			case mappedHook == hookGUID:
			case singleHook && mappedHook == "":
			case singleHook && mappedHook != "" && !hookExists[mappedHook]:
				// 单钩钓组的映射指向已经不存在的钩：饵同样无法继续挂载。
			default:
				continue
			}
			baitGUIDs = append(baitGUIDs, baitGUID)
		}
	}
	sort.Strings(baitGUIDs)
	for _, baitGUID := range baitGUIDs {
		row, ierr := s.DB.GetItemByGUIDTx(ctx, tx, baitGUID)
		if ierr != nil {
			return nil, nil, ierr
		}
		delete(mapped, baitGUID)
		if _, derr := tx.ExecContext(ctx, `DELETE FROM rod_setting WHERE guid=? AND key=?`, rodGUID, "bait_hook:"+baitGUID); derr != nil {
			return nil, nil, derr
		}
		if row != nil && row.UID == uid && row.RigGUID == rodGUID {
			srcGUID, derr := s.detachOnBreakTx(ctx, tx, uid, rodGUID, row,
				damageEventDetachedOnComponentBreak, "hook_break", hookGUID)
			if derr != nil {
				return nil, nil, derr
			}
			if srcGUID != "" {
				stackRefresh = append(stackRefresh, srcGUID)
			}
			removed = append(removed, baitGUID)
		}
		if _, derr := tx.ExecContext(ctx, `DELETE FROM rig_component WHERE rig_guid=? AND guid=?`, rodGUID, baitGUID); derr != nil {
			return nil, nil, derr
		}
		if key, _, ok := rigCompKeyForGUID(comps, baitGUID); ok {
			delete(comps, key)
		}
	}
	// 指向断钩的所有映射都作废（包括饵已不在钓组的脏记录）。
	for baitGUID, mappedHook := range mapped {
		if mappedHook == hookGUID {
			if _, derr := tx.ExecContext(ctx, `DELETE FROM rod_setting WHERE guid=? AND key=?`, rodGUID, "bait_hook:"+baitGUID); derr != nil {
				return nil, nil, derr
			}
		}
	}
	if err := s.clearRebaitForHookTx(ctx, tx, rodGUID, hookGUID, comps); err != nil {
		return nil, nil, err
	}
	return removed, stackRefresh, nil
}

// breakComponentTx 处理终端件断裂：组件耐久归零、从钓组摘除并退回背包
// （物品保留，可经工坊修理后重新装配），竿本体与承力线不受影响。
func (s *Server) breakComponentTx(ctx context.Context, uid int64, rodGUID, compGUID string, category int64) (*gearBreakOutcome, error) {
	tx, err := s.DB.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rod, err := s.DB.GetItemByGUIDTx(ctx, tx, rodGUID)
	if err != nil || rod == nil || rod.UID != uid {
		return nil, sql.ErrNoRows
	}
	comp, err := s.DB.GetItemByGUIDTx(ctx, tx, compGUID)
	if err != nil || comp == nil || comp.UID != uid {
		return nil, sql.ErrNoRows
	}
	rodFields, err := FieldsFromJSON(rod.FieldsJSON)
	if err != nil {
		return nil, err
	}
	mounted, err := breakReportMounted(ctx, tx, rod, rodFields, comp, category)
	if err != nil {
		return nil, err
	}
	if !mounted {
		return nil, errors.New("断件不在该钓组中")
	}
	compFields, err := FieldsFromJSON(comp.FieldsJSON)
	if err != nil {
		return nil, err
	}
	before := comp.Durability
	cIdx := fieldIdxByCS(comp.WireClass, csDurability)
	if cIdx < 0 || !setWireFloat(compFields, comp.WireClass, cIdx, 0) {
		return nil, errDurabilityField
	}
	comp.Durability = 0
	comp.Container = "backpack"
	comp.Slot = 0
	comp.RigGUID = ""
	clearRigFieldsForBackpack(compFields, comp.WireClass)
	if err := updateItemFieldsTx(ctx, s.DB, tx, comp, compFields); err != nil {
		return nil, err
	}
	// 投影可能先于 f5 存在；摘除投影不能依赖 f5 是否命中。
	if _, err := tx.ExecContext(ctx, `DELETE FROM rig_component WHERE rig_guid=? AND guid=?`, rodGUID, comp.GUID); err != nil {
		return nil, err
	}

	// 从 f5 钓组存储中摘除该组件；断钩时还要把挂在钩上的饵一起摘掉。
	stored := protoFieldAt(rodFields, rod.WireClass, csRigObject)
	comps := rigCompsOf(stored)
	var removed, stackRefresh []string
	if category == catHook {
		baitsRemoved, stacks, derr := s.detachHookBaitsTx(ctx, tx, uid, rodGUID, comp.GUID, comps)
		if derr != nil {
			return nil, derr
		}
		removed = append(removed, baitsRemoved...)
		stackRefresh = append(stackRefresh, stacks...)
	} else if err := s.clearComponentBaitSettingsTx(ctx, tx, rodGUID, comp.GUID, comps); err != nil {
		return nil, err
	}
	detached := false
	for key, c := range comps {
		if c.GUID == comp.GUID {
			delete(comps, key)
			detached = true
			break
		}
	}
	if detached || len(removed) > 0 {
		name, rigType, typeOK := rigIdentity(stored)
		if !typeOK {
			rigType = s.defaultRigTypeForRod(ctx, rod.ItemID)
			name = rigNameForType(rigType)
		}
		depth := rigDepthOf(stored, defaultDepthFor(name, rigType))
		if idx := fieldIdxByCS(rod.WireClass, csRigObject); idx >= 0 && idx < len(rodFields) {
			rodFields[idx].Op = "VAL"
			rodFields[idx].Val = buildRig153Like(stored, name, rigType, comps, depth, rigClipOf(stored))
		}
		if err := updateItemFieldsTx(ctx, s.DB, tx, rod, rodFields); err != nil {
			return nil, err
		}
	}
	// 损坏组件退回背包（清空 rig_guid），与摘除同事务提交；否则组件滞留在
	// rig 容器里，重登后既不在背包也不在钓组树。
	if err := s.DB.DetachReturnToBackpackTx(ctx, tx, comp.GUID); err != nil {
		return nil, err
	}
	entry := store.ItemDamageLog{
		UID: uid, ItemGUID: comp.GUID, ItemID: comp.ItemID, Event: damageEventComponentBreak,
		BeforeDurability: ptrFloat64(before), AfterDurability: ptrFloat64(0), BrokenGUID: comp.GUID,
		DetailJSON: damageDetailJSON(map[string]any{"rod_guid": rodGUID}),
	}
	if err := store.AddItemDamageLog(ctx, tx, entry); err != nil {
		return nil, err
	}
	stackRefresh = uniqueStrings(stackRefresh)
	updated := append([]string{rodGUID, comp.GUID}, stackRefresh...)
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	out := &gearBreakOutcome{
		ComponentBroken: true, BreakPart: "component", BrokenGUID: comp.GUID,
		RemovedGUIDs: removed, StackRefreshGUIDs: stackRefresh,
		UpdatedTrees: s.loadUpdatedTrees(ctx, updated),
	}
	return out, nil
}

// resolveGearBreak 按真实装配决定超载对象。手竿比较竿/主线/子线；
// 带轮竿额外比较当前耐久阶段的锁轮承力。
func (s *Server) resolveGearBreak(ctx context.Context, uid int64, rodGUID, reportedGUID string) (gearBreakDecision, error) {
	rod, err := s.DB.GetItemByGUID(ctx, rodGUID)
	if err != nil || rod == nil || rod.UID != uid {
		return gearBreakDecision{}, sql.ErrNoRows
	}
	ids := s.rigGearIDs(ctx, uid, rodGUID)
	spec := s.loadGearSpecForRig(ctx, ids, rodGUID)

	lineGUID := ""
	if g, gerr := s.DB.GetItemGUIDByRigSlot(ctx, rodGUID, catLine); gerr == nil {
		lineGUID = g
	}
	if lineGUID == "" {
		lineGUID, _ = s.storedRigItemGUID(ctx, rod, csLine)
	}
	// 引线 GUID 与最弱环计算同源：rigGearIDs 已经把竿实例的钓组对象和
	// rig_component 派生索引合并解析过（优先 15 前导线，回退 13 子线），
	// 这里不能再只读实例字段，否则只在派生索引里有装配时断件又会漏掉引线。
	leaderGUID := ids.LeaderGUID
	if leaderGUID == "" {
		leaderGUID = s.rigComponentGuidOf(ctx, rodGUID, catWireLeader)
	}
	if leaderGUID == "" {
		leaderGUID = s.rigComponentGuidOf(ctx, rodGUID, gearCatLeader)
	}
	// 钩也承力：大鱼吃小钩时，断的是钩而不是主线。有独立物品行就断钩；
	// 拟饵出厂钩没有物品行，退而断整个拟饵（客户端看到的是拟饵没了）。
	hookGUID := ""
	hookCat := int64(0)
	if g := s.mountedHookGuid(ctx, rodGUID); g != "" {
		if row, rerr := s.DB.GetItemByGUID(ctx, g); rerr == nil && row != nil && row.UID == uid {
			hookGUID, hookCat = g, catHook
		}
	}
	if hookGUID == "" {
		if lure := s.mountedLureGUID(ctx, rodGUID); lure != "" {
			if row, rerr := s.DB.GetItemByGUID(ctx, lure); rerr == nil && row != nil && row.UID == uid {
				hookGUID, hookCat = lure, catLure
			}
		}
	}

	type candidate struct {
		part     string
		guid     string
		category int64
		kg       float64
		priority int
	}
	var cs []candidate
	if lineGUID != "" && spec.LineStrengthKg > 0 {
		cs = append(cs, candidate{part: "line", guid: lineGUID, category: catLine, kg: spec.LineStrengthKg, priority: 1})
	}
	if leaderGUID != "" && spec.LeaderStrengthKg > 0 {
		leaderCat := catWireLeader
		leaderRow, _ := s.DB.GetItemByGUID(ctx, leaderGUID)
		if leaderRow != nil {
			if def, derr := s.itemDefForRow(ctx, leaderRow); derr == nil && def != nil {
				leaderCat = def.Category
			}
		}
		cs = append(cs, candidate{part: "leader", guid: leaderGUID, category: leaderCat, kg: spec.LeaderStrengthKg, priority: 0})
	}
	// 铅芯前导（57）与独立连接件（14）是并列的终端承力件，各自有拉力，
	// 与前导/子线一样是短链短板，同强度时先断这些便宜件。
	if ids.LeadcoreGUID != "" && spec.LeadcoreStrengthKg > 0 {
		cs = append(cs, candidate{part: "leadcore", guid: ids.LeadcoreGUID, category: gearCatLeadcore,
			kg: spec.LeadcoreStrengthKg, priority: 0})
	}
	if ids.ConnectorGUID != "" && spec.ConnectorStrengthKg > 0 {
		cs = append(cs, candidate{part: "connector", guid: ids.ConnectorGUID, category: catLeaderLock,
			kg: spec.ConnectorStrengthKg, priority: 0})
	}
	if spec.RodPowerKg > 0 {
		cs = append(cs, candidate{part: "rod", guid: rodGUID, category: gearCatRod, kg: spec.RodPowerKg, priority: 3})
	}
	// 钩与线组同级：同强度时先断子线，再断钩。
	if hookGUID != "" && spec.HookStrengthKg > 0 {
		cs = append(cs, candidate{part: "hook", guid: hookGUID, category: hookCat,
			kg: spec.HookStrengthKg, priority: 1})
	}

	// 17/9 是客户端明确上报的"断件"事件：只要来了就必须断，摩擦档位不能把
	// 它改成无损坏的泄力打滑。即使 23/24 档，鱼力尖峰仍可能拉坏最弱件；
	// 旧实现把"最弱件强于泄力上限"判成打滑，导致客户端已提示断线、服务端
	// 却保留整组钓具。线上 29（UI 30）锁轮后锁轮承力才作为候选参与比较；
	// 泄力打滑是另一条独立事件，不再从 17/9 推断。
	setting, _ := s.rodReelDragSetting(ctx, rodGUID)
	if spec.ReelPresent && ids.ReelGUID != "" && setting >= maxReelDragStop && spec.ReelDragKg > 0 {
		cs = append(cs, candidate{part: "reel", guid: ids.ReelGUID, category: gearCatReel,
			kg: spec.ReelDragKg, priority: 2})
	}

	if len(cs) > 0 {
		best := cs[0]
		for _, c := range cs[1:] {
			if c.kg < best.kg-1e-9 || (math.Abs(c.kg-best.kg) <= 1e-9 && c.priority < best.priority) {
				best = c
			}
		}
		if s.log != nil {
			kg2 := func(v float64) float64 { return math.Round(v*100) / 100 }
			s.log.Info("17/9 断件裁决", "rod", guidShort(rodGUID), "part", best.part,
				"drag_setting", setting, "kg", kg2(best.kg), "rod_kg", kg2(spec.RodPowerKg),
				"line_kg", kg2(spec.LineStrengthKg), "leader_kg", kg2(spec.LeaderStrengthKg),
				"hook_kg", kg2(spec.HookStrengthKg),
				"reel_lock_kg", kg2(spec.ReelDragKg), "reported", guidShort(reportedGUID))
		}
		return gearBreakDecision{Part: best.part, GUID: best.guid, Category: best.category}, nil
	}

	// 物理数据缺失时，按客户端上报品类做兼容回退。
	if reportedGUID == "" || reportedGUID == zeroGUID {
		return gearBreakDecision{Part: "rod", GUID: rodGUID, Category: gearCatRod}, nil
	}
	if row, rerr := s.DB.GetItemByGUID(ctx, reportedGUID); rerr == nil && row != nil {
		if def, derr := s.itemDefForRow(ctx, row); derr == nil && def != nil {
			switch {
			case def.Category == catHook || def.Category == catLure:
				return gearBreakDecision{Part: "hook", GUID: reportedGUID, Category: def.Category}, nil
			case def.Category == gearCatLeadcore:
				return gearBreakDecision{Part: "leadcore", GUID: reportedGUID, Category: def.Category}, nil
			case sourceLineCategories[def.Category]:
				return gearBreakDecision{Part: "line", GUID: reportedGUID, Category: def.Category}, nil
			}
		}
	}
	return gearBreakDecision{Part: "rod", GUID: rodGUID, Category: gearCatRod}, nil
}

// damageReelSlip 处理"泄力打滑"：鱼力超过泄力档时，摩擦片打滑放线、发热磨损，
// 制动力 csDragForce 线性衰减（reelSlipWear=2%/次，约 50 次磨穿）→ 泄力失效。
// 摩擦片是"只能摩擦不能损坏"的消耗件，磨穿只泄力失效、不伤本体，换片即恢复。
func (s *Server) damageReelSlip(ctx context.Context, uid int64, rodGUID, reelGUID string) (*gearBreakOutcome, error) {
	reel, err := s.DB.GetItemByGUID(ctx, reelGUID)
	if err != nil || reel == nil || reel.UID != uid {
		return nil, sql.ErrNoRows
	}
	before := reel.Durability

	wornOut, found, derr := s.degradeFrictionWasher(ctx, reel)
	if derr != nil {
		return nil, derr
	}
	if !found {
		// 旧档回退：没有独立摩擦片子件，磨本体耐久。
		return s.damageReelLockOverload(ctx, uid, rodGUID, reelGUID)
	}

	perf, perr := s.syncReelPerformance(ctx, reel)
	if perr != nil {
		return nil, perr
	}
	if err := s.DB.AddItemDamageLog(ctx, store.ItemDamageLog{
		UID: uid, ItemGUID: reel.GUID, ItemID: reel.ItemID, Event: damageEventReelOverload,
		BeforeDurability: ptrFloat64(before), AfterDurability: ptrFloat64(reel.Durability),
		DetailJSON: damageDetailJSON(map[string]any{
			"rod_guid": rodGUID, "base_drag_kg": perf.BaseDragKg,
			"locked_drag_kg": perf.LockedDragKg, "lock_multiplier": reelLockMultiplier(reel.Durability),
			"bearing_quality": perf.BearingQuality, "oil_quality": perf.OilQuality,
			"oil_wear_factor": perf.OilWearFactor, "friction_worn": wornOut,
		}),
	}); err != nil {
		return nil, err
	}
	updated := s.loadUpdatedTrees(ctx, []string{rodGUID, reelGUID})
	return &gearBreakOutcome{
		ReelDamaged: true, FrictionWorn: wornOut,
		BreakPart: "reel", BrokenGUID: reelGUID, UpdatedTrees: updated,
	}, nil
}

// damageReelLockOverload 处理"渔轮被拉坏"：客户端上报渔轮断件（或回退裁决判渔轮）时，
// 官方表现是耐久直接归 0、轮子留在竿上但不可用，而不是分多档磨损。
// 泄力打滑磨摩擦片是另一条独立通道，仍由 damageReelSlip 处理。
func (s *Server) damageReelLockOverload(ctx context.Context, uid int64, rodGUID, reelGUID string) (*gearBreakOutcome, error) {
	tx, err := s.DB.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rod, err := s.DB.GetItemByGUIDTx(ctx, tx, rodGUID)
	if err != nil || rod == nil || rod.UID != uid {
		return nil, sql.ErrNoRows
	}
	reel, err := s.DB.GetItemByGUIDTx(ctx, tx, reelGUID)
	if err != nil || reel == nil || reel.UID != uid {
		return nil, sql.ErrNoRows
	}
	before := reel.Durability
	reelFields, err := FieldsFromJSON(reel.FieldsJSON)
	if err != nil {
		return nil, err
	}
	cIdx := fieldIdxByCS(reel.WireClass, csDurability)
	if cIdx < 0 || !setWireFloat(reelFields, reel.WireClass, cIdx, 0) {
		return nil, errDurabilityField
	}
	reel.Durability = 0
	if err := updateItemFieldsTx(ctx, s.DB, tx, reel, reelFields); err != nil {
		return nil, err
	}

	// 渔轮留在竿上（f4）但已损坏；主线、前导、拟饵等其余钓组组件摘回背包。
	rodFields, err := FieldsFromJSON(rod.FieldsJSON)
	if err != nil {
		return nil, err
	}
	stored := protoFieldAt(rodFields, rod.WireClass, csRigObject)
	compGUIDs := map[string]bool{}
	if rows, qerr := tx.QueryContext(ctx,
		`SELECT COALESCE(guid,'') FROM rig_component WHERE rig_guid=? AND guid<>?`, rodGUID, reelGUID); qerr == nil {
		for rows.Next() {
			var g string
			if err := rows.Scan(&g); err == nil && g != "" {
				compGUIDs[g] = true
			}
		}
		_ = rows.Err()
		_ = rows.Close()
	}
	for _, c := range rigCompsOf(stored) {
		if c.GUID != "" && c.GUID != reelGUID {
			compGUIDs[c.GUID] = true
		}
	}
	if lineGUID, lerr := s.storedRigItemGUID(ctx, rod, csLine); lerr == nil && lineGUID != "" && lineGUID != reelGUID {
		compGUIDs[lineGUID] = true
	}
	guids := make([]string, 0, len(compGUIDs))
	for g := range compGUIDs {
		guids = append(guids, g)
	}
	sort.Strings(guids)
	var removed, stackRefresh []string
	for _, g := range guids {
		row, gerr := s.DB.GetItemByGUIDTx(ctx, tx, g)
		if gerr != nil || row == nil {
			continue
		}
		srcGUID, derr := s.detachOnBreakTx(ctx, tx, uid, rodGUID, row,
			damageEventDetachedOnReelBreak, "reel_break", reelGUID)
		if derr != nil {
			return nil, derr
		}
		removed = append(removed, g)
		if srcGUID != "" {
			stackRefresh = append(stackRefresh, srcGUID)
		}
	}
	// 钓组已整体清空：补饵请求和饵-钩映射必须一起清掉，否则修好轮子后
	// 第一次抛竿会把饵挂回一条空钓组。
	if err := clearRodBaitSettingsTx(ctx, tx, rodGUID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM rig_component WHERE rig_guid=? AND guid<>?`, rodGUID, reelGUID); err != nil {
		return nil, err
	}
	comps := rigCompsOf(stored)
	for k := range comps {
		delete(comps, k)
	}
	name, rigType, typeOK := rigIdentity(stored)
	if !typeOK {
		rigType = s.defaultRigTypeForRod(ctx, rod.ItemID)
		name = rigNameForType(rigType)
	}
	depth := rigDepthOf(stored, defaultDepthFor(name, rigType))
	if i := fieldIdxByCS(rod.WireClass, csRigObject); i >= 0 && i < len(rodFields) {
		rodFields[i].Op = "VAL"
		rodFields[i].Val = buildRig153WithClip(name, rigType, comps, depth, rigClipOf(stored))
	}
	if i := fieldIdxByCS(rod.WireClass, csLine); i >= 0 {
		rodFields[i].Op = "OPTGUID"
		rodFields[i].Hex = ""
	}
	if err := updateItemFieldsTx(ctx, s.DB, tx, rod, rodFields); err != nil {
		return nil, err
	}

	if err := store.AddItemDamageLog(ctx, tx, store.ItemDamageLog{
		UID: uid, ItemGUID: reel.GUID, ItemID: reel.ItemID, Event: damageEventReelOverload,
		BeforeDurability: ptrFloat64(before), AfterDurability: ptrFloat64(0),
		DetailJSON: damageDetailJSON(map[string]any{
			"rod_guid": rodGUID, "lock_multiplier": reelLockMultiplier(0),
			"wear_progress": 0, "reel_broken": true,
		}),
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	if fresh, ferr := s.DB.GetItemByGUID(ctx, reelGUID); ferr == nil && fresh != nil {
		perf, perr := s.syncReelPerformance(ctx, fresh)
		if perr != nil {
			return nil, perr
		}
		perf.WearProgress = 0
		if err := s.DB.UpsertReelPerformance(ctx, *perf); err != nil {
			return nil, err
		}
	}
	stackRefresh = uniqueStrings(stackRefresh)
	updated := append([]string{rodGUID, reelGUID}, stackRefresh...)
	return &gearBreakOutcome{
		ReelDamaged: true, ReelBroken: true,
		BreakPart: "reel", BrokenGUID: reelGUID, RemovedGUIDs: removed,
		StackRefreshGUIDs: stackRefresh,
		UpdatedTrees:      s.loadUpdatedTrees(ctx, updated),
	}, nil
}

// rigBrokenReelGUID 返回竿上那只耐久归零的渔轮 GUID；没有轮子或轮子完好时返回空串。
// 轮子损坏后仍留在竿上（不像线会从钓组消失），所以抛竿前必须单独校验，
// 否则损坏的轮子还能继续上鱼。
func (s *Server) rigBrokenReelGUID(ctx context.Context, uid int64, rodGUID string) string {
	if s == nil || s.DB == nil || rodGUID == "" || rodGUID == zeroGUID {
		return ""
	}
	ids := s.rigGearIDs(ctx, uid, rodGUID)
	if strings.TrimSpace(ids.ReelGUID) == "" {
		return ""
	}
	row, err := s.DB.GetItemByGUID(ctx, ids.ReelGUID)
	if err != nil || row == nil || row.UID != uid {
		return ""
	}
	if row.Durability > 0 {
		return ""
	}
	return row.GUID
}

func (s *Server) storedRigItemGUID(ctx context.Context, rod *store.Item, csName string) (string, error) {
	if rod == nil {
		return "", nil
	}
	fields, err := FieldsFromJSON(rod.FieldsJSON)
	if err != nil {
		return "", err
	}
	idx := fieldIdxByCS(rod.WireClass, csName)
	if idx < 0 || idx >= len(fields) {
		return "", nil
	}
	return fields[idx].GuidHex(), nil
}

func updateItemFieldsTx(ctx context.Context, db *store.DB, tx *sql.Tx, row *store.Item, fields []proto.Field) error {
	bumpCreatedTicks(fields, row.WireClass)
	fj, err := FieldsToJSON(fields)
	if err != nil {
		return err
	}
	if err := db.UpdateItemFieldsTx(ctx, tx, row.GUID, fj, row.Qty, row.Durability, row.Workmanship, row.RigGUID); err != nil {
		return err
	}
	row.FieldsJSON = fj
	return nil
}

// lineLossPolicy 描述一次主线/子线长度损失的来源与记账方式。
// 断线（17/9）和剪线（17/10）共用同一套"扣主线长度 + 清掉主线以下终端件"的
// 落库逻辑，区别只在扣多少米、以及写哪一类 damage log。
type lineLossPolicy struct {
	// requestedM 本次要扣掉的米数；<=0 时由 randomWhenMissing 决定是随机扣还是不扣。
	requestedM int64
	// randomWhenMissing 断线语义：客户端只上报张力不上报长度，按剩余长度 1..80% 随机扣。
	// 剪线是玩家主动动作，上报多少扣多少，不能随机。
	randomWhenMissing bool
	// eventLine 记录主线长度变化的事件名；eventLost 记录随线丢失的配件。
	eventLine string
	eventLost string
	// reason 写进 damage log detail 的成因标记。
	reason string
}

// breakLineTx 处理 17/9 断线：主线固定损失 5 米，断子线不扣主线长度。
// rodLineLengthLeft 返回竿上主线的剩余米数，仅用于日志；没有主线时返回 0。
func (s *Server) rodLineLengthLeft(ctx context.Context, rodGUID string) int64 {
	if s.DB == nil {
		return 0
	}
	rod, err := s.DB.GetItemByGUID(ctx, rodGUID)
	if err != nil || rod == nil {
		return 0
	}
	lineGUID, _ := s.storedRigItemGUID(ctx, rod, csLine)
	if lineGUID == "" {
		return 0
	}
	lineRow, err := s.DB.GetItemByGUID(ctx, lineGUID)
	if err != nil || lineRow == nil {
		return 0
	}
	return s.itemLength(ctx, lineRow)
}

func (s *Server) breakLineTx(ctx context.Context, uid int64, rodGUID, brokenGUID string, breakCategory int64) (*gearBreakOutcome, error) {
	return s.lineLossTx(ctx, uid, rodGUID, brokenGUID, breakCategory, lineLossPolicy{
		requestedM:        5,
		randomWhenMissing: true,
		eventLine:         damageEventLineBreak,
		eventLost:         damageEventLostOnLineBreak,
		reason:            "line_break",
	})
}

// cutLineTx 处理 17/10 剪线：客户端上报本次放出多少米，竿上主线就少多少米；
// 主线以下的前导、子线、钩、饵、拟饵、浮漂全部随线丢失，主线本体保留。
func (s *Server) cutLineTx(ctx context.Context, uid int64, rodGUID string, cutM int64) (*gearBreakOutcome, error) {
	if cutM <= 0 {
		return nil, errors.New("剪线长度必须大于 0")
	}
	return s.lineLossTx(ctx, uid, rodGUID, "", catLine, lineLossPolicy{
		requestedM: cutM,
		eventLine:  damageEventLineCut,
		eventLost:  damageEventLostOnLineCut,
		reason:     "line_cut",
	})
}

func (s *Server) lineLossTx(ctx context.Context, uid int64, rodGUID, brokenGUID string, breakCategory int64, policy lineLossPolicy) (*gearBreakOutcome, error) {
	tx, err := s.DB.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rod, err := s.DB.GetItemByGUIDTx(ctx, tx, rodGUID)
	if err != nil || rod == nil || rod.UID != uid {
		return nil, sql.ErrNoRows
	}
	rodFields, err := FieldsFromJSON(rod.FieldsJSON)
	if err != nil {
		return nil, err
	}
	brokenRow, _ := s.DB.GetItemByGUIDTx(ctx, tx, brokenGUID)

	lineGUID, _ := s.storedRigItemGUID(ctx, rod, csLine)
	if lineGUID == "" {
		if row, rerr := s.DB.GetItemByGUID(ctx, brokenGUID); rerr == nil && row != nil {
			if def, derr := s.itemDefForRow(ctx, row); derr == nil && def != nil && def.Category == catLine {
				lineGUID = brokenGUID
			}
		}
	}
	var lineRow *store.Item
	var lineFields []proto.Field
	beforeLength, afterLength, lossLength := int64(0), int64(0), int64(0)
	mainLineBreak := breakCategory == catLine
	if lineGUID != "" {
		lineRow, _ = s.DB.GetItemByGUIDTx(ctx, tx, lineGUID)
		if lineRow != nil {
			lineFields, err = FieldsFromJSON(lineRow.FieldsJSON)
			if err != nil {
				return nil, err
			}
			beforeLength = itemLengthField(lineRow, lineFields)
			if beforeLength <= 0 {
				if def, derr := s.itemDefForRow(ctx, lineRow); derr == nil && def != nil {
					beforeLength = lineLengthM(def)
				}
			}
			afterLength = beforeLength
			if mainLineBreak {
				// 断主线固定损失 5 米，剪线按客户端上报的放线米数扣；
				// 断子线不走这个分支，不扣主线长度。
				lossLength = policy.requestedM
				if lossLength <= 0 && policy.randomWhenMissing {
					lossLength = lineBreakLoss(beforeLength, 0)
				}
				if lossLength > beforeLength {
					lossLength = beforeLength
				}
				afterLength = beforeLength - lossLength
				if afterLength < 0 {
					afterLength = 0
				}
				if afterLength > 0 {
					if !setItemLengthField(lineRow, lineFields, afterLength) {
						return nil, errLineLengthField
					}
					if err := updateItemFieldsTx(ctx, s.DB, tx, lineRow, lineFields); err != nil {
						return nil, err
					}
				}
			}
		}
	}

	stored := protoFieldAt(rodFields, rod.WireClass, csRigObject)
	// 主线断了就按"挂在线上的东西全部没了"处理；只断下游某一节时，按该节在
	// 钓组模板中的位置取下游集合，线上方的浮漂、铅坠和铅芯必须留在主线上。
	lossCategories := mainLineLossCategories
	if !mainLineBreak {
		_, rigType, typeOK := rigIdentity(stored)
		if !typeOK {
			rigType = s.defaultRigTypeForRod(ctx, rod.ItemID)
		}
		lossCategories = downstreamLossCategories(rigType, breakCategory)
	}
	var removed []string
	terminalRows := map[string]*store.Item{}
	var terminalGUIDs []string
	if rows, qerr := tx.QueryContext(ctx, `SELECT slot, COALESCE(guid,'') FROM rig_component WHERE rig_guid=?`, rodGUID); qerr == nil {
		for rows.Next() {
			var slot int64
			var guid string
			if err := rows.Scan(&slot, &guid); err != nil {
				_ = rows.Close()
				return nil, err
			}
			if guid != "" && lossCategories[slot] {
				terminalGUIDs = append(terminalGUIDs, guid)
			}
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
	}
	for _, guid := range terminalGUIDs {
		if row, ierr := s.DB.GetItemByGUIDTx(ctx, tx, guid); ierr == nil && row != nil {
			terminalRows[guid] = row
		}
	}
	// f5 中可能还有尚未投影到 rig_component 的组件，按类别一并清理。
	for _, comp := range rigCompsOf(stored) {
		if comp.GUID == "" || !lossCategories[comp.Category] {
			continue
		}
		if _, ok := terminalRows[comp.GUID]; !ok {
			if row, ierr := s.DB.GetItemByGUIDTx(ctx, tx, comp.GUID); ierr == nil && row != nil {
				terminalRows[comp.GUID] = row
			}
		}
	}

	stackNumericIDs := map[int64]bool{}
	stackSourceGUIDs := map[string]bool{}
	terminalCategories := map[string]int64{}
	for _, row := range terminalRows {
		// 事务内不能走 stackableItem（它会另开连接查 item_def，测试库单连接会死锁）。
		category, ok := effectiveCategoryTx(ctx, tx, row.ItemID)
		if !ok {
			continue
		}
		terminalCategories[row.GUID] = category
		if !countableCategories[category] {
			continue
		}
		if srcGUID := splitSourceGUID(row); len(srcGUID) == 32 {
			stackSourceGUIDs[srcGUID] = true
		}
		stackNumericIDs[row.NumericID] = true
	}

	for guid, row := range terminalRows {
		// 最新存档会把浮钓/沉底的一整包饵装进钩饵子实例。剪线只损失其中
		// 一份，剩余数量必须退回背包；直接删子实例会把整包饵一起吞掉。
		category := terminalCategories[guid]
		if policy.reason == "line_cut" &&
			(category == catBait || category == catLure) && row.Container == "rig" && row.RigGUID == rodGUID {
			qty := baitQuantity(row)
			if qty > 1 {
				compFields, ferr := FieldsFromJSON(row.FieldsJSON)
				if ferr != nil {
					return nil, ferr
				}
				if !setItemQty(row.WireClass, compFields, uint64(qty-1)) {
					return nil, errors.New("钩饵实例缺少数量字段")
				}
				row.Qty = qty - 1
				row.Container = "backpack"
				row.Slot = 0
				row.RigGUID = ""
				clearRigFieldsForBackpack(compFields, row.WireClass)
				if err := updateItemFieldsTx(ctx, s.DB, tx, row, compFields); err != nil {
					return nil, err
				}
				if err := s.DB.DetachReturnToBackpackTx(ctx, tx, guid); err != nil {
					return nil, err
				}
			} else {
				if err := s.DB.DeleteItemCascadeTx(ctx, tx, guid); err != nil {
					return nil, err
				}
				removed = append(removed, guid)
			}
		} else {
			if err := s.DB.DeleteItemCascadeTx(ctx, tx, guid); err != nil {
				return nil, err
			}
			removed = append(removed, guid)
		}
		entry := store.ItemDamageLog{
			UID: uid, ItemGUID: guid, ItemID: row.ItemID, Event: policy.eventLost,
			BeforeDurability: ptrFloat64(row.Durability), BrokenGUID: brokenGUID,
			DetailJSON: damageDetailJSON(map[string]any{"rod_guid": rodGUID, "reason": policy.reason}),
		}
		if err := store.AddItemDamageLog(ctx, tx, entry); err != nil {
			return nil, err
		}
	}
	// 线路类断裂会移除所有末端钩/饵/拟饵；对应的自动补饵和饵钩映射
	// 必须与组件删除同事务清掉，不能留到修好或下一次抛竿才碰巧清理。
	if err := clearRodBaitSettingsTx(ctx, tx, rodGUID); err != nil {
		return nil, err
	}

	if lineRow != nil && afterLength <= 0 {
		if err := s.DB.DeleteItemCascadeTx(ctx, tx, lineRow.GUID); err != nil {
			return nil, err
		}
		removed = append(removed, lineRow.GUID)
		if idx := fieldIdxByCS(rod.WireClass, csLine); idx >= 0 && idx < len(rodFields) {
			rodFields[idx].Op = "OPTGUID"
			rodFields[idx].Hex = ""
		}
	}

	comps := rigCompsOf(stored)
	for cat := range lossCategories {
		deleteRigCategory(comps, cat)
	}
	if lineRow != nil && afterLength <= 0 {
		deleteRigCategory(comps, catLine)
	}
	name, rigType, typeOK := rigIdentity(stored)
	if !typeOK {
		rigType = s.defaultRigTypeForRod(ctx, rod.ItemID)
		name = rigNameForType(rigType)
	}
	depth := rigDepthOf(stored, defaultDepthFor(name, rigType))
	if idx := fieldIdxByCS(rod.WireClass, csRigObject); idx >= 0 && idx < len(rodFields) {
		rodFields[idx].Op = "VAL"
		rodFields[idx].Val = buildRig153Like(stored, name, rigType, comps, depth, rigClipOf(stored))
	}
	if err := updateItemFieldsTx(ctx, s.DB, tx, rod, rodFields); err != nil {
		return nil, err
	}

	if mainLineBreak && lineRow != nil {
		entry := store.ItemDamageLog{
			UID: uid, ItemGUID: lineRow.GUID, ItemID: lineRow.ItemID, Event: policy.eventLine,
			BeforeDurability: ptrFloat64(lineRow.Durability), AfterDurability: ptrFloat64(lineRow.Durability),
			BeforeLength: ptrInt64(beforeLength), AfterLength: ptrInt64(afterLength), BrokenGUID: brokenGUID,
			DetailJSON: damageDetailJSON(map[string]any{"rod_guid": rodGUID, "reason": policy.reason, "loss_m": lossLength}),
		}
		if err := store.AddItemDamageLog(ctx, tx, entry); err != nil {
			return nil, err
		}
		if afterLength <= 0 {
			entry.Event = damageEventLineExhausted
			if err := store.AddItemDamageLog(ctx, tx, entry); err != nil {
				return nil, err
			}
		}
	} else {
		if brokenRow != nil {
			entry := store.ItemDamageLog{
				UID: uid, ItemGUID: brokenRow.GUID, ItemID: brokenRow.ItemID, Event: policy.eventLine,
				BeforeDurability: ptrFloat64(brokenRow.Durability), AfterDurability: ptrFloat64(brokenRow.Durability),
				BrokenGUID: brokenGUID,
				DetailJSON: damageDetailJSON(map[string]any{"rod_guid": rodGUID, "reason": "leader_break", "loss_m": 0}),
			}
			if err := store.AddItemDamageLog(ctx, tx, entry); err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	part := "leader"
	if mainLineBreak {
		part = "line"
	}
	out := &gearBreakOutcome{LineBroken: true, BreakPart: part, LineLossM: lossLength, BrokenGUID: brokenGUID, RemovedGUIDs: removed}
	updated := []string{rodGUID}
	if mainLineBreak && lineRow != nil && afterLength > 0 {
		updated = append(updated, lineRow.GUID)
	}
	// 计件配件（饵、拟饵等）来自背包堆的拆分：装配时来源堆数量已经减一，
	// 但当时没有推给客户端，剪线/断线删掉子实例后数量仍显示旧值。
	// 这里把来源堆（含同物品号的其它背包堆）一并放进更新段并推送。
	if stackGUIDs := s.backpackStackGUIDsForItems(ctx, uid, stackNumericIDs, stackSourceGUIDs); len(stackGUIDs) > 0 {
		out.StackRefreshGUIDs = stackGUIDs
		updated = append(updated, stackGUIDs...)
	}
	out.UpdatedTrees = s.loadUpdatedTrees(ctx, updated)
	return out, nil
}

// backpackStackGUIDsForItems 返回背包里这些物品号的堆 GUID：sourceGUIDs 是
// 被删子实例记录的来源堆（仍需校验属于本人、同物品号且在背包，防止误刷别的
// 物品），numericIDs 兜底覆盖来源堆已消耗/合并的情形。
func (s *Server) backpackStackGUIDsForItems(ctx context.Context, uid int64, numericIDs map[int64]bool, sourceGUIDs map[string]bool) []string {
	if len(numericIDs) == 0 && len(sourceGUIDs) == 0 {
		return nil
	}
	keep := map[string]bool{}
	if len(numericIDs) > 0 {
		placeholders := make([]string, 0, len(numericIDs))
		args := make([]any, 0, len(numericIDs)+2)
		args = append(args, uid)
		for id := range numericIDs {
			placeholders = append(placeholders, "?")
			args = append(args, id)
		}
		args = append(args, "backpack")
		query := `SELECT guid FROM item WHERE uid=? AND numeric_id IN (` +
			strings.Join(placeholders, ",") + `) AND container=?`
		rows, err := s.DB.SQL.QueryContext(ctx, query, args...)
		if err == nil {
			for rows.Next() {
				var g string
				if err := rows.Scan(&g); err == nil && len(g) == 32 {
					keep[g] = true
				}
			}
			_ = rows.Err()
			_ = rows.Close()
		}
	}
	for g := range sourceGUIDs {
		row, err := s.DB.GetItemByGUID(ctx, g)
		if err != nil || row == nil || row.UID != uid || row.Container != "backpack" {
			continue
		}
		if len(numericIDs) > 0 && !numericIDs[row.NumericID] {
			continue
		}
		keep[g] = true
	}
	if len(keep) == 0 {
		return nil
	}
	guids := make([]string, 0, len(keep))
	for g := range keep {
		guids = append(guids, g)
	}
	sort.Strings(guids)
	return guids
}

func (s *Server) breakRodTx(ctx context.Context, uid int64, rodGUID, brokenGUID string) (*gearBreakOutcome, error) {
	tx, err := s.DB.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rod, err := s.DB.GetItemByGUIDTx(ctx, tx, rodGUID)
	if err != nil || rod == nil || rod.UID != uid {
		return nil, sql.ErrNoRows
	}
	before := rod.Durability
	fields, err := FieldsFromJSON(rod.FieldsJSON)
	if err != nil {
		return nil, err
	}
	idx := fieldIdxByCS(rod.WireClass, csDurability)
	if idx < 0 || !setWireFloat(fields, rod.WireClass, idx, 0) {
		return nil, errDurabilityField
	}
	rod.Durability = 0
	if err := updateItemFieldsTx(ctx, s.DB, tx, rod, fields); err != nil {
		return nil, err
	}

	// 断竿只报废竿本体：耐久归零、不能取竿也不能抛投，竿上配件与主线全部
	// 摘回背包且耐久不变，可以拆下来装到别的竿上。
	// 离开的组件仍进 11028 的移除列表让客户端清掉 UI 状态；缺少移除列表时
	// 客户端不清线组、鱼仍挂钩，整轮卡死在搏鱼状态，只能换竿逃生。
	var removed, stackRefresh []string
	stored := protoFieldAt(fields, rod.WireClass, csRigObject)
	compGUIDs := map[string]bool{}
	if rows, qerr := tx.QueryContext(ctx,
		`SELECT COALESCE(guid,'') FROM rig_component WHERE rig_guid=?`, rodGUID); qerr == nil {
		for rows.Next() {
			var g string
			if err := rows.Scan(&g); err == nil && g != "" {
				compGUIDs[g] = true
			}
		}
		_ = rows.Err()
		_ = rows.Close()
	}
	for _, c := range rigCompsOf(stored) {
		if c.GUID != "" {
			compGUIDs[c.GUID] = true
		}
	}
	if lineGUID, lerr := s.storedRigItemGUID(ctx, rod, csLine); lerr == nil && lineGUID != "" {
		compGUIDs[lineGUID] = true
	}
	guids := make([]string, 0, len(compGUIDs))
	for g := range compGUIDs {
		guids = append(guids, g)
	}
	sort.Strings(guids)
	for _, g := range guids {
		row, gerr := s.DB.GetItemByGUIDTx(ctx, tx, g)
		if gerr != nil || row == nil {
			continue
		}
		// 组件不销毁：完好组件摘回背包；拆分子饵并回来源堆，避免背包里
		// 出现同种饵两堆。来源堆会进入 4/22 刷新列表。
		srcGUID, derr := s.detachOnBreakTx(ctx, tx, uid, rodGUID, row,
			damageEventDetachedOnRodBreak, "rod_break", brokenGUID)
		if derr != nil {
			return nil, derr
		}
		removed = append(removed, g)
		if srcGUID != "" {
			stackRefresh = append(stackRefresh, srcGUID)
		}
	}
	// 钓组已整体清空：补饵请求和饵-钩映射必须一起清掉，否则修好竿后
	// 第一次抛竿会把饵挂回一条空钓组。
	if err := clearRodBaitSettingsTx(ctx, tx, rodGUID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM rig_component WHERE rig_guid=?`, rodGUID); err != nil {
		return nil, err
	}
	// 清空竿上的钓组存储与主线引用。
	comps := rigCompsOf(stored)
	for k := range comps {
		delete(comps, k)
	}
	name, rigType, typeOK := rigIdentity(stored)
	if !typeOK {
		rigType = s.defaultRigTypeForRod(ctx, rod.ItemID)
		name = rigNameForType(rigType)
	}
	depth := rigDepthOf(stored, defaultDepthFor(name, rigType))
	if i := fieldIdxByCS(rod.WireClass, csRigObject); i >= 0 && i < len(fields) {
		fields[i].Op = "VAL"
		fields[i].Val = buildRig153WithClip(name, rigType, comps, depth, rigClipOf(stored))
	}
	if i := fieldIdxByCS(rod.WireClass, csLine); i >= 0 && i < len(fields) {
		fields[i].Op = "OPTGUID"
		fields[i].Hex = ""
	}
	if i := fieldIdxByCS(rod.WireClass, csReel); i >= 0 && i < len(fields) {
		fields[i].Op = "OPTGUID"
		fields[i].Hex = ""
	}
	if err := updateItemFieldsTx(ctx, s.DB, tx, rod, fields); err != nil {
		return nil, err
	}

	entry := store.ItemDamageLog{
		UID: uid, ItemGUID: rod.GUID, ItemID: rod.ItemID, Event: damageEventRodBreak,
		BeforeDurability: ptrFloat64(before), AfterDurability: ptrFloat64(0), BrokenGUID: brokenGUID,
		DetailJSON: damageDetailJSON(map[string]any{"rod_guid": rodGUID}),
	}
	if err := store.AddItemDamageLog(ctx, tx, entry); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	stackRefresh = uniqueStrings(stackRefresh)
	updated := append([]string{rodGUID}, stackRefresh...)
	return &gearBreakOutcome{
		LineBroken: false, BreakPart: "rod", BrokenGUID: brokenGUID, RemovedGUIDs: removed,
		StackRefreshGUIDs: stackRefresh,
		UpdatedTrees:      s.loadUpdatedTrees(ctx, updated),
	}, nil
}

func (s *Server) loadUpdatedTrees(ctx context.Context, guids []string) []*proto.Value {
	out := make([]*proto.Value, 0, len(guids))
	seen := map[string]bool{}
	for _, guid := range guids {
		if guid == "" || seen[guid] {
			continue
		}
		seen[guid] = true
		if row, err := s.DB.GetItemByGUID(ctx, guid); err == nil && row != nil {
			if tree, terr := s.itemTree(ctx, row); terr == nil {
				out = append(out, tree)
			}
		}
	}
	return out
}

func damageDetailJSON(v map[string]any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func protoFieldAt(fields []proto.Field, class, csName string) *proto.Value {
	idx := fieldIdxByCS(class, csName)
	if idx < 0 || idx >= len(fields) {
		return nil
	}
	return fields[idx].Val
}

func buildGearBreakResult(outcome *gearBreakOutcome) (*proto.Value, error) {
	if outcome == nil {
		outcome = &gearBreakOutcome{}
	}
	updated := proto.Null()
	if len(outcome.UpdatedTrees) > 0 {
		updated = proto.Args(proto.S("61"), outcome.UpdatedTrees...)
	}
	removed := proto.Null()
	if len(outcome.RemovedGUIDs) > 0 {
		removed = proto.GuidArr(outcome.RemovedGUIDs)
	}
	return wiregen.MustMakeObj("ikmdmfoojfg", map[int]*proto.Value{
		0: proto.U32(boolU32(outcome.LineBroken)), 1: proto.Guid(outcome.BrokenGUID),
		2: proto.Null(), 3: updated, 4: removed,
	}), nil
}

// 这些错误只在字段结构异常时返回，调用方可回退到兼容响应。
var (
	errLineLengthField = errors.New("鱼线长度字段不存在")
	errDurabilityField = errors.New("耐久字段不存在")
)
