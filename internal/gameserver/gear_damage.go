package gameserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	damageEventWear               = "wear"
	damageEventLineBreak          = "line_break"
	damageEventLineExhausted      = "line_exhausted"
	damageEventLostOnLineBreak    = "lost_on_line_break"
	damageEventLineCut            = "line_cut"
	damageEventLostOnLineCut      = "lost_on_line_cut"
	damageEventDetachedOnRodBreak = "detached_on_rod_break"
	damageEventRodBreak           = "rod_break"
	damageEventReelOverload       = "reel_overload"
	damageEventComponentBreak     = "component_break"
	damageEventRepair             = "repair"
)

type gearBreakOutcome struct {
	LineBroken      bool
	ReelDamaged     bool
	ComponentBroken bool
	BreakPart       string // leader / line / reel / rod / component
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
}

// lineBreakTerminalCategories 是断线时随线一起丢失的终端组件。
// 路亚也在其中：线断后拟饵、钩、前导和饵都不可能留在竿上。
var lineBreakTerminalCategories = map[int64]bool{
	catHook: true, catLead: true, catLeaderLock: true, catWireLeader: true,
	catLure: true, catBait: true, catFloat: true,
}

// 子线断只损失钩组；浮漂挂在上方主线，不应跟着消失。
var leaderBreakLossCategories = map[int64]bool{
	catHook: true, catLead: true, catLeaderLock: true, catWireLeader: true,
	catLure: true, catBait: true,
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

// sourceLineCategories 包含主线及前导/钢丝前导。断任何一项都按断线处理。
var sourceLineCategories = map[int64]bool{
	catLine: true, catLead: true, catLeaderLock: true, catWireLeader: true,
}

// terminalBreakCategories 是不承力的终端消耗件：钩、拟饵、饵、浮漂。
// 这类件由客户端在打斗中直接上报断裂（如钩被牙磨断、拟饵挂底拉断），
// 上报可以采信：采信终端件损坏没有作弊收益，误判成本也只是消耗件本身。
// 承力件（线/前导/竿/轮）仍走 resolveGearBreak 的物理裁决。
var terminalBreakCategories = map[int64]bool{
	catHook: true, catLure: true, catBait: true, catFloat: true,
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

// breakLineOrRod 处理 17/9：客户端只负责报告“已经超载”，承力件（竿、主线、
// 子线、轮）由服务端重新比较真实强度决定断哪一件，不能直接相信客户端上报的
// 断件。终端件（钩/拟饵/饵/漂）不承力，客户端上报的断裂直接采信。
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
	dec, err := s.resolveGearBreak(ctx, uid, rodGUID, reportedGUID)
	if err != nil {
		return nil, err
	}
	switch dec.Part {
	case "leader", "line":
		return s.breakLineTx(ctx, uid, rodGUID, dec.GUID, dec.Category)
	case "reel":
		return s.damageReelOverload(ctx, uid, rodGUID, dec.GUID)
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
	if !terminal && !sourceLineCategories[def.Category] && def.Category != gearCatReel {
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
		return validatedBreakReport{terminalCategory: def.Category}, nil
	}
	return validatedBreakReport{}, nil
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

	// 从 f5 钓组存储中摘除该组件。
	stored := protoFieldAt(rodFields, rod.WireClass, csRigObject)
	comps := rigCompsOf(stored)
	detached := false
	for key, c := range comps {
		if c.GUID == comp.GUID {
			delete(comps, key)
			detached = true
			break
		}
	}
	if detached {
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
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	out := &gearBreakOutcome{
		ComponentBroken: true, BreakPart: "component", BrokenGUID: comp.GUID,
		UpdatedTrees: s.loadUpdatedTrees(ctx, []string{rodGUID, comp.GUID}),
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
	leaderGUID := s.rigComponentGuidOf(ctx, rodGUID, catWireLeader)
	if leaderGUID == "" {
		leaderGUID = s.rigComponentGuidOf(ctx, rodGUID, gearCatLeader)
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
	if spec.RodPowerKg > 0 {
		cs = append(cs, candidate{part: "rod", guid: rodGUID, category: gearCatRod, kg: spec.RodPowerKg, priority: 3})
	}
	// 渔轮一项按当前摩擦档位的制动力参与比较：1~29 档一旦超过就打滑放线，
	// 竿和线根本承受不到更高的张力，只有第 30 档锁轮才可能压断更硬的件。
	reelKg := spec.ReelCurrentDragKg
	if reelKg <= 0 {
		reelKg = spec.ReelDragKg
	}
	if spec.ReelPresent && ids.ReelGUID != "" && reelKg > 0 {
		cs = append(cs, candidate{part: "reel", guid: ids.ReelGUID, category: gearCatReel,
			kg: reelKg, priority: 2})
	}

	if len(cs) > 0 {
		best := cs[0]
		for _, c := range cs[1:] {
			if c.kg < best.kg-1e-9 || (math.Abs(c.kg-best.kg) <= 1e-9 && c.priority < best.priority) {
				best = c
			}
		}
		setting, hasSetting := s.rodReelDragSetting(ctx, rodGUID)
		if !hasSetting {
			setting = maxReelDragStop
		}
		if s.log != nil {
			kg2 := func(v float64) float64 { return math.Round(v*100) / 100 }
			s.log.Info("17/9 断件裁决", "rod", guidShort(rodGUID), "part", best.part,
				"drag_setting", setting, "kg", kg2(best.kg), "rod_kg", kg2(spec.RodPowerKg),
				"line_kg", kg2(spec.LineStrengthKg), "leader_kg", kg2(spec.LeaderStrengthKg),
				"reel_kg", kg2(reelKg), "reel_lock_kg", kg2(spec.ReelDragKg),
				"reported", guidShort(reportedGUID))
		}
		return gearBreakDecision{Part: best.part, GUID: best.guid, Category: best.category}, nil
	}

	// 物理数据缺失时，按客户端上报品类做兼容回退。
	if reportedGUID == "" || reportedGUID == zeroGUID {
		return gearBreakDecision{Part: "rod", GUID: rodGUID, Category: gearCatRod}, nil
	}
	if row, rerr := s.DB.GetItemByGUID(ctx, reportedGUID); rerr == nil && row != nil {
		if def, derr := s.itemDefForRow(ctx, row); derr == nil && def != nil {
			if sourceLineCategories[def.Category] {
				return gearBreakDecision{Part: "line", GUID: reportedGUID, Category: def.Category}, nil
			}
		}
	}
	return gearBreakDecision{Part: "rod", GUID: rodGUID, Category: gearCatRod}, nil
}

// damageReelOverload 在锁轮成为最弱环时让轮子按 20% 阶段掉耐久，最低保留 20%。
// 轮子不会像线或子线一样从钓组删除；更新后的承力立即写入 reel_performance。
func (s *Server) damageReelOverload(ctx context.Context, uid int64, rodGUID, reelGUID string) (*gearBreakOutcome, error) {
	reel, err := s.DB.GetItemByGUID(ctx, reelGUID)
	if err != nil || reel == nil || reel.UID != uid {
		return nil, sql.ErrNoRows
	}
	before := reel.Durability
	beforePerf, err := s.syncReelPerformance(ctx, reel)
	if err != nil {
		return nil, err
	}
	progress := beforePerf.WearProgress + reelDurabilityStep*beforePerf.OilWearFactor
	after := before
	for progress+1e-9 >= reelDurabilityStep && after > reelDurabilityStep {
		after = math.Max(reelDurabilityStep, math.Round((after-reelDurabilityStep)*100)/100)
		progress -= reelDurabilityStep
	}
	if after <= reelDurabilityStep {
		progress = 0
	}
	if after != before {
		if err := s.persistItemDurability(ctx, reel, after); err != nil {
			return nil, err
		}
	}
	perf, err := s.syncReelPerformance(ctx, reel)
	if err != nil {
		return nil, err
	}
	perf.WearProgress = math.Max(0, progress)
	if err := s.DB.UpsertReelPerformance(ctx, *perf); err != nil {
		return nil, err
	}
	if err := s.DB.AddItemDamageLog(ctx, store.ItemDamageLog{
		UID: uid, ItemGUID: reel.GUID, ItemID: reel.ItemID, Event: damageEventReelOverload,
		BeforeDurability: ptrFloat64(before), AfterDurability: ptrFloat64(after),
		DetailJSON: damageDetailJSON(map[string]any{
			"rod_guid": rodGUID, "base_drag_kg": perf.BaseDragKg,
			"locked_drag_kg": perf.LockedDragKg, "lock_multiplier": reelLockMultiplier(after),
			"bearing_item_id": perf.BearingItemID, "bearing_quality": perf.BearingQuality,
			"bearing_factor": perf.BearingFactor, "oil_item_id": perf.OilItemID,
			"oil_quality": perf.OilQuality, "oil_wear_factor": perf.OilWearFactor,
			"wear_progress": perf.WearProgress,
		}),
	}); err != nil {
		return nil, err
	}
	updated := s.loadUpdatedTrees(ctx, []string{rodGUID, reelGUID})
	return &gearBreakOutcome{
		ReelDamaged: true, BreakPart: "reel", BrokenGUID: reelGUID, UpdatedTrees: updated,
	}, nil
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

	// 主线断了就按"挂在线上的东西全部没了"处理；只断前导时线上方的浮漂和
	// 铅坠仍留在主线那段，沿用较窄的前导损失集合。
	lossCategories := mainLineLossCategories
	if !mainLineBreak {
		lossCategories = leaderBreakLossCategories
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
	stored := protoFieldAt(rodFields, rod.WireClass, csRigObject)
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
	var removed []string
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
		// 组件不销毁：清掉 wire 父引用与槽位后摘回背包，耐久保持原值。
		compFields, ferr := FieldsFromJSON(row.FieldsJSON)
		if ferr != nil {
			return nil, ferr
		}
		row.Container = "backpack"
		row.Slot = 0
		row.RigGUID = ""
		clearRigFieldsForBackpack(compFields, row.WireClass)
		if err := updateItemFieldsTx(ctx, s.DB, tx, row, compFields); err != nil {
			return nil, err
		}
		if err := s.DB.DetachReturnToBackpackTx(ctx, tx, g); err != nil {
			return nil, err
		}
		removed = append(removed, g)
		detachLog := store.ItemDamageLog{
			UID: uid, ItemGUID: g, ItemID: row.ItemID, Event: damageEventDetachedOnRodBreak,
			BeforeDurability: ptrFloat64(row.Durability), AfterDurability: ptrFloat64(row.Durability),
			BrokenGUID: brokenGUID,
			DetailJSON: damageDetailJSON(map[string]any{"rod_guid": rodGUID, "reason": "rod_break"}),
		}
		if err := store.AddItemDamageLog(ctx, tx, detachLog); err != nil {
			return nil, err
		}
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
	return &gearBreakOutcome{
		LineBroken: false, BreakPart: "rod", BrokenGUID: brokenGUID, RemovedGUIDs: removed,
		UpdatedTrees: s.loadUpdatedTrees(ctx, []string{rodGUID}),
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
