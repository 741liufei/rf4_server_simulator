package gameserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

type craftedMaterial struct {
	row *store.Item
	// def 决定成品里嵌套子物品的协议类别与模型 numeric_id。
	def *store.ItemDef
	// physical 是玩家真正消耗的材料定义。强度按它计算，避免"最接近的
	// 前导线模型"把 60kg 编织线悄悄折算成 36kg。
	physical *store.ItemDef
}

// strengthDef 返回计算强度应采用的定义：真实材料优先，其次模型定义。
func (m craftedMaterial) strengthDef() *store.ItemDef {
	if m.physical != nil {
		return m.physical
	}
	return m.def
}

type craftedLeaderMaterials struct {
	line      craftedMaterial
	connector craftedMaterial
}

// 引线的 f3 接受普通主线，f1 接受前导线，按实际材料类型选择。
const craftedMainLineKey = "baadhdmaonp"

func isCraftedShockLeader(itemID string) bool {
	return strings.HasPrefix(itemID, "leader_crafted_shock_")
}

var leaderWireMatchAttrs = []string{
	"kngaaefkpka", // 最大拉力
	"ciichedlnjh", // 线径
	"aomkdnjcgji", // 延展/材质参数
	"lgjeahdobch", // 线材等级参数
}

func (s *Server) leaderWireCatalog(ctx context.Context) ([]*store.ItemDef, error) {
	s.leaderWireMu.Lock()
	defer s.leaderWireMu.Unlock()
	if s.leaderWireLoaded {
		return s.leaderWireDefs, s.leaderWireErr
	}
	defs, err := s.DB.ListItemDefsByCategory(ctx, gearCatLeaderWire)
	s.leaderWireLoaded = err == nil
	s.leaderWireDefs = defs
	s.leaderWireErr = err
	return defs, err
}

// leaderWireDefForLine 找到线材对应的前导线定义。
//
// 客户端把前导内线渲染成前导线模型，普通主线是另一套模型；只换协议外壳
// 而保留普通主线的 numeric_id，会在制作结果页触发 InvalidCastException，
// 所以成品内线必须落在真实的 category 15 定义上。
//
// 目录里只有 193 条前导线定义，而主线有 1044 条，其中 890 条没有四项参数
// 逐一相同的孪生前导线（编织线几乎都没有）。旧实现只接受精确匹配，玩家用
// 这些线做引线就直接失败。现在按"参数完全一致 → 拉力与线径最接近"分级选择，
// 只有目录为空时才失败；成品强度另按玩家真实消耗的线材计算。
func (s *Server) leaderWireDefForLine(ctx context.Context, lineDef *store.ItemDef) (*store.ItemDef, error) {
	if lineDef == nil {
		return nil, errors.New("缺少主线定义")
	}
	if lineDef.Category == gearCatLeaderWire {
		return lineDef, nil
	}
	if lineDef.Category != gearCatLine {
		return nil, fmt.Errorf("线材品类=%d，不是普通主线或前导线", lineDef.Category)
	}
	candidates, err := s.leaderWireCatalog(ctx)
	if err != nil {
		return nil, err
	}
	best := pickLeaderWireDef(lineDef, candidates)
	if best == nil {
		return nil, fmt.Errorf("前导线目录为空，主线 %s 无法映射", lineDef.ItemID)
	}
	return best, nil
}

// leaderWirePick 是前导线映射中的一个候选及其评分。
type leaderWirePick struct {
	def      *store.ItemDef
	exact    bool
	sameKind bool
	distance float64
}

// leaderWireDistance 用对数距离衡量两根线材在拉力和线径上的差距。
// 线材参数以 F32 位型存放，必须先还原成物理量再比较。
func leaderWireDistance(lineKg, lineDia, wireKg, wireDia float64) float64 {
	score := 0.0
	if lineKg > 0 && wireKg > 0 {
		score += math.Abs(math.Log(wireKg / lineKg))
	}
	if lineDia > 0 && wireDia > 0 {
		score += math.Abs(math.Log(wireDia / lineDia))
	}
	return score
}

// betterLeaderWirePick 报告 cand 是否优于 best：参数一致优先，其次物理距离
// 更小，再用同厂牌和 numeric_id 保证结果稳定可复现。
func betterLeaderWirePick(best, cand *leaderWirePick) bool {
	if cand == nil {
		return false
	}
	if best == nil {
		return true
	}
	if best.exact != cand.exact {
		return cand.exact
	}
	if best.distance != cand.distance {
		return cand.distance < best.distance
	}
	if best.sameKind != cand.sameKind {
		return cand.sameKind
	}
	return cand.def.NumericID < best.def.NumericID
}

// pickLeaderWireDef 在候选里挑一根最贴近线材的前导线定义。
// 零拉力定义只会做出强度为 0 的引线，只有在没有其它候选时才会被选中。
func pickLeaderWireDef(lineDef *store.ItemDef, candidates []*store.ItemDef) *store.ItemDef {
	lineAttrs := defAttrs(lineDef)
	targetComplete := true
	for _, key := range leaderWireMatchAttrs {
		if _, ok := lineAttrs[key]; !ok {
			targetComplete = false
			break
		}
	}
	lineKg := itemDefF32(lineDef, gearAttrMaxLoad)
	lineDia := itemDefF32(lineDef, gearAttrDiameter)

	var usable, fallback *leaderWirePick
	for _, candidate := range candidates {
		if candidate == nil || candidate.Category != gearCatLeaderWire {
			continue
		}
		attrs := defAttrs(candidate)
		exact := targetComplete
		if exact {
			for _, key := range leaderWireMatchAttrs {
				if lineAttrs[key] != attrs[key] {
					exact = false
					break
				}
			}
		}
		wireKg := itemDefF32(candidate, gearAttrMaxLoad)
		pick := &leaderWirePick{
			def:      candidate,
			exact:    exact,
			sameKind: candidate.Kind == lineDef.Kind,
			distance: leaderWireDistance(lineKg, lineDia, wireKg,
				itemDefF32(candidate, gearAttrDiameter)),
		}
		if betterLeaderWirePick(fallback, pick) {
			fallback = pick
		}
		if lineKg <= 0 || wireKg > 0 {
			if betterLeaderWirePick(usable, pick) {
				usable = pick
			}
		}
	}
	if usable != nil {
		return usable.def
	}
	if fallback != nil {
		return fallback.def
	}
	return nil
}

// consumeLeaderCraftMaterialsTx 按配方需求扣除真实材料，并保留线材和连接件
// 的实例定义。客户端提交的临时 GUID 找不到时必须回退到请求里的需求 ID，
// 不能把任意背包物品当材料，也不能只扣一份而忽略 demand 数量。
func (s *Server) consumeLeaderCraftMaterialsTx(
	ctx context.Context,
	tx *sql.Tx,
	uid int64,
	slots []craftSlot,
	demands []craftDemand,
	times int64,
) ([]string, craftedLeaderMaterials, error) {
	var selected craftedLeaderMaterials
	rows, err := listItemsTx(ctx, tx, uid, []string{"backpack", "rig"})
	if err != nil {
		return nil, selected, err
	}
	if times < 1 {
		times = 1
	}

	byGUID := make(map[string]*store.Item, len(rows))
	for _, row := range rows {
		byGUID[row.GUID] = row
	}
	preferred := make(map[string][]string)
	for _, slot := range slots {
		if slot.GUID == "" {
			continue
		}
		preferred[slot.ItemID] = append(preferred[slot.ItemID], slot.GUID)
	}

	consumed := make(map[string]bool)
	var touched []string
	for _, demand := range demands {
		need := demand.need() * times
		if need <= 0 {
			continue
		}
		var pick *craftedMaterial

		tryRow := func(row *store.Item) (bool, error) {
			if row == nil || row.Qty <= 0 || consumed[row.GUID] {
				return false, nil
			}
			match, err := s.craftDemandMatchesTx(ctx, tx, demand, row)
			if err != nil || !match {
				return false, err
			}
			if pick == nil {
				def, err := itemDefByItemIDTx(ctx, tx, row.ItemID)
				if err != nil || def == nil {
					return false, fmt.Errorf("读取材料定义失败: %s", row.ItemID)
				}
				pick = &craftedMaterial{row: row, def: def}
			} else if demand.DemandID == "craft_mat_line" && pick.row.ItemID != row.ItemID {
				return false, fmt.Errorf("同一条引线不能混用不同线材: %s / %s", pick.row.ItemID, row.ItemID)
			}

			have := stackCountTx(ctx, tx, row)
			if have < 1 {
				have = 1
			}
			take := need
			if have < take {
				take = have
			}
			got, err := consumeItemQtyTx(ctx, tx, row, take)
			if err != nil {
				return false, err
			}
			if got > 0 && !consumed[row.GUID] {
				consumed[row.GUID] = true
				touched = append(touched, row.GUID)
			}
			need -= got
			return got > 0, nil
		}

		// 客户端给出的真实实例 GUID 优先，但必须通过需求类目校验。
		for _, guid := range preferred[demand.DemandID] {
			if need <= 0 {
				break
			}
			if _, err := tryRow(byGUID[guid]); err != nil {
				return nil, selected, err
			}
		}
		// 临时 GUID 或指定实例数量不足时，按需求类目从真实背包继续补扣。
		for _, row := range rows {
			if need <= 0 {
				break
			}
			if _, err := tryRow(row); err != nil {
				return nil, selected, err
			}
		}
		if need > 0 {
			return nil, selected, fmt.Errorf("%s 还缺 %d 份", demand.DemandID, need)
		}
		if pick == nil {
			continue
		}
		switch demand.DemandID {
		case "craft_mat_line":
			selected.line = *pick
		case "craft_mat_swivels":
			selected.connector = *pick
		}
	}
	if selected.line.row == nil {
		return nil, selected, errors.New("配方没有扣除到线材")
	}
	return touched, selected, nil
}

func (s *Server) craftDemandMatchesTx(
	ctx context.Context,
	tx *sql.Tx,
	demand craftDemand,
	row *store.Item,
) (bool, error) {
	for _, alt := range demand.Alternatives {
		if alt != "" && alt == row.ItemID {
			return true, nil
		}
	}
	if len(demand.Categories) == 0 {
		return false, nil
	}
	realCat, ok := effectiveCategoryTx(ctx, tx, row.ItemID)
	if !ok {
		return false, nil
	}
	for _, cat := range demand.Categories {
		if cat == realCat {
			return true, nil
		}
	}
	return false, nil
}

// materializeCraftedLeaderTx 将真实线材与连接件写入成品引线的嵌套配置。
// 根物品 ID 保持配方指定的引线家族/长度；内线模型取最接近真实线材的前导线
// 定义，强度仍按玩家真正消耗的那根线计算，既不套固定模板也不被模型削弱。
func (s *Server) materializeCraftedLeaderTx(
	ctx context.Context,
	tx *sql.Tx,
	row *store.Item,
	materials craftedLeaderMaterials,
) error {
	if row == nil || materials.line.row == nil || materials.line.def == nil {
		return errors.New("引线缺少线材")
	}
	lineKey := gearAttrLeaderIn
	switch materials.line.def.Category {
	case gearCatLine:
		lineKey = craftedMainLineKey
	case gearCatLeaderWire:
		if isCraftedShockLeader(row.ItemID) {
			return errors.New("减震引线需要普通主线")
		}
	default:
		return errors.New("引线材料不是主线或前导线")
	}
	def, err := itemDefByItemIDTx(ctx, tx, row.ItemID)
	if err != nil || def == nil {
		return fmt.Errorf("引线成品定义不存在: %s", row.ItemID)
	}
	leaderLengthCM := int64(math.Round(itemDefF32(def, gearAttrLength)))

	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return err
	}
	setSub := func(cs string, sub *proto.Value) {
		idx := fieldIdxByCS(row.WireClass, cs)
		if idx >= 0 && idx < len(fields) && sub != nil {
			fields[idx].Val = sub
		}
	}
	lineObj, err := s.craftedMaterialObject(row, materials.line, lineKey, leaderLengthCM)
	if err != nil {
		return err
	}
	var connectorObj *proto.Value
	if materials.connector.row != nil && materials.connector.def != nil {
		connectorObj, err = s.craftedMaterialObject(row, materials.connector, "lbadjgllglo", 0)
		if err != nil {
			return err
		}
	}
	setSub(lineKey, lineObj)
	setSub("lbadjgllglo", connectorObj)

	lineStrength := applyCraftedMaterialState(lineObj, materials.line, leaderLengthCM)
	connectorStrength := applyCraftedMaterialState(connectorObj, materials.connector, 0)
	strength := lineStrength
	if connectorStrength > 0 && (strength <= 0 || connectorStrength < strength) {
		strength = connectorStrength
	}
	if strength <= 0 {
		return errors.New("真实材料的强度都为 0")
	}
	if !setWireFloat(fields, row.WireClass, fieldIdxByCS(row.WireClass, csStrength), strength) {
		return errors.New("成品引线没有可写入的强度字段")
	}

	fj, err := FieldsToJSON(fields)
	if err != nil {
		return err
	}
	row.FieldsJSON = fj
	return nil
}

func (s *Server) craftedMaterialObject(
	host *store.Item,
	material craftedMaterial,
	key string,
	lengthCM int64,
) (*proto.Value, error) {
	if host == nil || material.row == nil || material.def == nil {
		return nil, errors.New("空材料")
	}
	wireCategory := material.def.Category
	if key == gearAttrLeaderIn {
		// f1 的协议类型固定为内嵌前导线(cat15)。原材料即使来自主线(cat11)，
		// 也只能搬运其 numeric_id、强度和线径，不能沿用主线对象类。
		wireCategory = gearCatLeaderWire
	}
	cls, ok := categoryWireClass[wireCategory]
	if !ok {
		cls = wireClassEntry{fallbackWireClass, fallbackTypeID}
	}
	if wiregen.ClassOps[cls.Class] == nil {
		return nil, fmt.Errorf("材料类没有 wire reader: %s", material.def.ItemID)
	}
	obj, err := wiregen.MakeObj(cls.Class, nil)
	if err != nil {
		return nil, err
	}
	s.applyDefAttrs(material.def, cls.Class, obj)
	if idx := fieldIdxByCS(cls.Class, csGUID); idx >= 0 {
		obj.Fields[idx].Hex = uuidLike(host.GUID, key)
	}
	if idx := fieldIdxByCS(cls.Class, csNumericID); idx >= 0 {
		obj.Fields[idx].Num = ptrU64(uint64(material.def.NumericID))
	}
	if idx := fieldIdxByCS(cls.Class, csParent); idx >= 0 {
		obj.Fields[idx].Hex = host.GUID
	}
	if idx := fieldIdxByCS(cls.Class, csSlotEnum); idx >= 0 {
		obj.Fields[idx].Num = ptrU64(4)
	}
	if idx := fieldIdxByCS(cls.Class, csStateEnum); idx >= 0 {
		state := defAttrU64OrZero(material.def, "hgaijieklek")
		obj.Fields[idx].Num = ptrU64(state | subMountFlag)
	}
	applyCraftedMaterialState(obj, material, lengthCM)
	return obj, nil
}

func applyCraftedMaterialState(obj *proto.Value, material craftedMaterial, lengthCM int64) float64 {
	if obj == nil || material.row == nil || material.def == nil {
		return 0
	}
	setWireFloat(obj.Fields, obj.Class, fieldIdxByCS(obj.Class, csWorkmanship), material.row.Workmanship)
	setWireFloat(obj.Fields, obj.Class, fieldIdxByCS(obj.Class, csDurability), material.row.Durability)
	if lengthCM > 0 {
		if idx := fieldIdxByCS(obj.Class, gearAttrLineLen); idx >= 0 && idx < len(obj.Fields) {
			obj.Fields[idx].Num = ptrU64(uint64(lengthCM))
		}
	}
	strength := itemDefF32(material.strengthDef(), gearAttrMaxLoad) * material.row.Durability * material.row.Workmanship
	if strength > 0 {
		setWireFloat(obj.Fields, obj.Class, fieldIdxByCS(obj.Class, csStrength), strength)
	}
	return strength
}

// repairCraftedLeaderWire 修复旧版本自制引线中内嵌的普通主线对象。
// 返回 invalid 表示这件引线根本没有可搬运的内嵌线材（或前导线目录为空），
// 必须从背包隔离；参数不一致已不再算无效。
func (s *Server) repairCraftedLeaderWire(
	ctx context.Context,
	it *store.Item,
	fields []proto.Field,
) (changed bool, invalid bool, err error) {
	if it == nil || it.WireClass != "oldfpclaabo" || !strings.HasPrefix(it.ItemID, "leader_crafted_") {
		return false, false, nil
	}
	if idx := fieldIdxByCS(it.WireClass, craftedMainLineKey); idx >= 0 && idx < len(fields) {
		line := fields[idx].Val
		if line != nil && line.Kind == proto.KindObj && line.Class == categoryWireClass[gearCatLine].Class {
			return false, false, nil
		}
	}
	lineIdx := fieldIdxByCS(it.WireClass, gearAttrLeaderIn)
	if lineIdx < 0 || lineIdx >= len(fields) {
		return false, true, nil
	}
	lineObj := fields[lineIdx].Val
	if lineObj == nil || lineObj.Kind != proto.KindObj {
		return false, true, nil
	}
	numericIdx := fieldIdxByCS(lineObj.Class, csNumericID)
	if numericIdx < 0 || numericIdx >= len(lineObj.Fields) || lineObj.Fields[numericIdx].Num == nil {
		return false, true, nil
	}
	lineDef, err := s.DB.GetItemDef(ctx, int64(u64Of(lineObj.Fields[numericIdx].Num)))
	if err != nil {
		return false, true, err
	}
	if lineDef == nil {
		return false, true, fmt.Errorf("内线定义不存在")
	}
	if lineDef.Category == gearCatLeaderWire {
		return false, false, nil
	}
	wireDef, err := s.leaderWireDefForLine(ctx, lineDef)
	if err != nil {
		return false, true, nil
	}

	readFloat := func(obj *proto.Value, cs string) float64 {
		if obj == nil {
			return 0
		}
		idx := fieldIdxByCS(obj.Class, cs)
		if idx < 0 || idx >= len(obj.Fields) || obj.Fields[idx].Num == nil {
			return 0
		}
		return float64(proto.F32FromBits(uint32(u64Of(obj.Fields[idx].Num))))
	}
	childGUID := ""
	if idx := fieldIdxByCS(lineObj.Class, csGUID); idx >= 0 && idx < len(lineObj.Fields) {
		childGUID = lineObj.Fields[idx].Hex
	}
	material := craftedMaterial{
		row: &store.Item{
			GUID:        childGUID,
			Workmanship: readFloat(lineObj, csWorkmanship),
			Durability:  readFloat(lineObj, csDurability),
		},
		def: wireDef,
		// 强度仍按玩家原本那根线材计算。
		physical: lineDef,
	}
	if material.row.Workmanship <= 0 {
		material.row.Workmanship = it.Workmanship
	}
	if material.row.Durability <= 0 {
		material.row.Durability = it.Durability
	}

	rootDef, err := s.DB.GetItemDef(ctx, it.NumericID)
	if err != nil {
		return false, false, err
	}
	lengthCM := int64(0)
	if rootDef != nil {
		lengthCM = int64(math.Round(itemDefF32(rootDef, gearAttrLength)))
	}
	if lengthCM <= 0 {
		if idx := fieldIdxByCS(lineObj.Class, gearAttrLineLen); idx >= 0 && idx < len(lineObj.Fields) &&
			lineObj.Fields[idx].Num != nil {
			lengthCM = int64(u64Of(lineObj.Fields[idx].Num))
		}
	}
	replacement, err := s.craftedMaterialObject(it, material, gearAttrLeaderIn, lengthCM)
	if err != nil {
		return false, false, err
	}
	lineStrength := applyCraftedMaterialState(replacement, material, lengthCM)
	fields[lineIdx].Val = replacement

	strength := lineStrength
	connectorIdx := fieldIdxByCS(it.WireClass, "lbadjgllglo")
	if connectorIdx >= 0 && connectorIdx < len(fields) {
		connector := fields[connectorIdx].Val
		if connector != nil && connector.Kind == proto.KindObj {
			numericIdx := fieldIdxByCS(connector.Class, csNumericID)
			if numericIdx >= 0 && numericIdx < len(connector.Fields) && connector.Fields[numericIdx].Num != nil {
				connectorDef, derr := s.DB.GetItemDef(ctx, int64(u64Of(connector.Fields[numericIdx].Num)))
				if derr == nil && connectorDef != nil {
					value := itemDefF32(connectorDef, gearAttrMaxLoad) *
						readFloat(connector, csDurability) * readFloat(connector, csWorkmanship)
					if value > 0 && (strength <= 0 || value < strength) {
						strength = value
					}
				}
			}
		}
	}
	if strength > 0 {
		setWireFloat(fields, it.WireClass, fieldIdxByCS(it.WireClass, csStrength), strength)
	}
	return true, false, nil
}

func defAttrU64OrZero(def *store.ItemDef, key string) uint64 {
	value, _ := defAttrU64(def, key)
	return value
}
