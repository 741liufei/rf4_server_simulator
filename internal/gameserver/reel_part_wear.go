package gameserver

// 渔轮内部零件的耐久消耗与本体耐久聚合。
//
// 外面那一格耐久不是独立账本：它是轮内六件零件剩余寿命的加权结果，最弱的一件还会额外
// 拉低整体。零件按"用了多久"缓慢消耗，锁轮过载、泄力打滑、泡水、断件冲击各再加一点，
// 品质越低的件越不耐用。摩擦片不参与这里的每竿基准：它是烧件，寿命由泄力打滑与锁轮
// 过载曲线决定，再叠一层基准就会变成第二个摩擦片。
// 口径见 specs/changes/reel-friction-life-model/spec.md §5/§6/§7。

import (
	"context"
	"math"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// reelPartWearRates 是单个零件的消耗速率，单位是百分点（1 = 1% 剩余寿命）。
type reelPartWearRates struct {
	perRod         float64 // 每竿正常使用的基准：抛投、收线、溜鱼都折算在这里
	perOverloadSec float64 // 锁轮过载每秒
	perSlip        float64 // 泄力打滑每次
	perSoak        float64 // 泡水每竿一次
	perBreak       float64 // 断件冲击每次：只在真断线/断竿那一次扣
}

// reelPartWearMatrix 是各件每竿的消耗速率。速度差是刻意的：摩擦片一竿约 5%，最慢的
// 结构件一竿只有 0.008%，摩擦片比它们快 25 倍以上，玩家的日常成本始终压在摩擦片和
// 轮油这两件"保养品"上，其余配件是长期账。
var reelPartWearMatrix = map[int64]reelPartWearRates{
	reelPartCatOil:     {perRod: 0.320, perOverloadSec: 0.015, perSlip: 0.010, perSoak: 0.040},
	reelPartCatGear:    {perRod: 0.030, perOverloadSec: 0.010, perSlip: 0.002, perBreak: 0.05},
	reelPartCatBearing: {perRod: 0.030, perOverloadSec: 0.012, perSlip: 0.004, perSoak: 0.010, perBreak: 0.05},
	reelPartCatHandle:  {perRod: 0.030, perOverloadSec: 0.004},
	reelPartCatSpool:   {perRod: 0.008, perOverloadSec: 0.006, perBreak: 0.20},
}

// reelPartCounts 是本竿攒下来要结算的应力事件计数。
type reelPartCounts struct {
	Rods         int     // 本竿是否算一次正常使用（抛过竿就算，与是否中鱼无关）
	OverloadSecs float64 // 锁轮过载累计秒数（与摩擦片发热曲线同一个数）
	SlipTicks    int     // 泄力打滑判定次数
	Soaks        int     // 泡水次数（每竿一次）
	BreakHits    int     // 断件冲击次数
}

// total 报告除了摩擦片之外还有没有要结算的账。
func (c reelPartCounts) total() float64 {
	return float64(c.Rods) + c.OverloadSecs + float64(c.SlipTicks) + float64(c.Soaks) + float64(c.BreakHits)
}

// reelPartLifeFactor 是品质寿命系数 Fq = clamp(0.25 + 0.75 x 品质/100, 0.35, 1)：
// 品质 0/15/40/100 分别约 0.35/0.36/0.55/1.00。写库前除以它，低档货寿命明显更短。
func reelPartLifeFactor(quality float64) float64 {
	if quality < 0 {
		quality = 0
	}
	if quality > 100 {
		quality = 100
	}
	factor := 0.25 + 0.75*quality/100
	if factor < 0.35 {
		factor = 0.35
	}
	if factor > 1 {
		factor = 1
	}
	return factor
}

// reelPartWearFor 按消耗矩阵算出本竿该零件要扣掉的寿命比例（0~1）。摩擦片不在矩阵里，
// 返回 0：它的账由泄力打滑与过载曲线单独结。
func reelPartWearFor(category int64, quality float64, counts reelPartCounts) float64 {
	rates, ok := reelPartWearMatrix[category]
	if !ok {
		return 0
	}
	points := rates.perRod*float64(counts.Rods) +
		rates.perOverloadSec*counts.OverloadSecs +
		rates.perSlip*float64(counts.SlipTicks) +
		rates.perSoak*float64(counts.Soaks) +
		rates.perBreak*float64(counts.BreakHits)
	if points <= 0 {
		return 0
	}
	return clampUnit(points / 100 / reelPartLifeFactor(quality))
}

// reelChildDurability 读零件子实例的剩余耐久。字段缺失与 FLOAT 的 0 都按满耐久
// （历史存档没写过该字段），只有 U32 承载的 0 才是真的磨完。
func reelChildDurability(child *proto.Value) float64 {
	if child == nil || child.Kind != proto.KindObj {
		return 1
	}
	idx := fieldIdxByCS(child.Class, csDurability)
	if idx < 0 || idx >= child.FieldCount() {
		return 1
	}
	switch fieldOpAt(child.Class, idx) {
	case "FLOAT", "DOUBLE":
		if v := child.Fields[idx].Float; v > 0 {
			return math.Min(1, v)
		}
		return 1
	default:
		raw := child.Fields[idx].Num
		if raw == nil {
			return 1
		}
		return clampUnit(float64(proto.F32FromBits(uint32(*raw))))
	}
}

// setReelChildDurability 写零件子实例耐久，一律夹进 0~1。
func setReelChildDurability(child *proto.Value, v float64) bool {
	if child == nil || child.Kind != proto.KindObj {
		return false
	}
	idx := fieldIdxByCS(child.Class, csDurability)
	if idx < 0 {
		return false
	}
	return setWireFloat(child.Fields, child.Class, idx, clampUnit(v))
}

// reelPartQuality 从零件定义读静态品质。读不出时按 100 结算，与品质口径一致。
func reelPartQuality(def *store.ItemDef) float64 {
	if def == nil {
		return 100
	}
	return math.Max(0, math.Min(100, float64(itemDefInt(def, reelPartQualityAttr))))
}

// reelWearPartCategories 是参与每竿消耗的结构件与保养品（摩擦片除外）。
var reelWearPartCategories = []int64{
	reelPartCatOil, reelPartCatGear, reelPartCatBearing, reelPartCatHandle, reelPartCatSpool,
}

// wearReelPartsInFields 按消耗矩阵扣轮内零件耐久，返回零件定义 ID 到本次扣掉寿命比例的
// 映射（供损伤日志）。已经磨完的件不再扣；缺件的轮子什么都不做，老档回退由调用方决定。
func (s *Server) wearReelPartsInFields(ctx context.Context, fields []proto.Field, counts reelPartCounts) map[string]float64 {
	out := map[string]float64{}
	if s == nil || counts.total() <= 0 {
		return out
	}
	for _, category := range reelWearPartCategories {
		child, def, _, ok := s.selectedReelPart(ctx, fields, category)
		if !ok || child == nil || def == nil {
			continue
		}
		cur := reelChildDurability(child)
		if cur <= 0 {
			continue
		}
		worn := reelPartWearFor(category, reelPartQuality(def), counts)
		if worn <= 0 {
			continue
		}
		next := cur - worn
		if next < 0 {
			next = 0
		}
		if !setReelChildDurability(child, next) {
			continue
		}
		out[def.ItemID] = cur - next
	}
	return out
}

// reelWasherLife 摩擦片剩余寿命 = 当前制动力 / 额定制动力（含母轮工艺）。
// 摩擦片的权威是制动力，耐久字段只是等比镜像，用来让背包里能看到损耗。
func reelWasherLife(currentDrag, ratedDrag float64) float64 {
	if ratedDrag <= 0 {
		return 0
	}
	if currentDrag <= 0 {
		return 0
	}
	return clampUnit(currentDrag / ratedDrag)
}

// 本体耐久聚合权重：摩擦片与齿轮最重，保养品轮油与其余件较轻。
const (
	reelBodyWeightWasher  = 0.25
	reelBodyWeightGear    = 0.25
	reelBodyWeightBearing = 0.20
	reelBodyWeightSpool   = 0.10
	reelBodyWeightHandle  = 0.10
	reelBodyWeightOil     = 0.10
	// reelBodyWeakSpotTolerance 是最弱熔断件对整体耐久的容差：外面那格不能比里面的
	// 短板好太多，"综合体检"才有意义。
	reelBodyWeakSpotTolerance = 0.15
)

// reelBodyFuseCategories 是能把渔轮本体直接拉到损坏的熔断件。轮油只进加权不熔断：
// 油干了是变差，不是坏。
var reelBodyFuseCategories = []int64{
	reelPartCatWasher, reelPartCatGear, reelPartCatBearing, reelPartCatSpool, reelPartCatHandle,
}

// reelPartLivesInFields 收集轮内六件零件的剩余寿命。ok=false 表示这只轮子读不出摩擦片
// 额定制动力（老档或品类不认识），这时不做聚合，免得把读不懂的库存误判成损坏。
func (s *Server) reelPartLivesInFields(ctx context.Context, reel *store.Item, fields []proto.Field) (map[int64]float64, bool) {
	lives := map[int64]float64{}
	if s == nil || reel == nil {
		return nil, false
	}
	child, def, _, ok := s.selectedReelPart(ctx, fields, reelPartCatWasher)
	if !ok || child == nil || def == nil {
		return nil, false
	}
	rated := itemDefF32(def, gearAttrDragMax) * reel.Workmanship
	if rated <= 0 {
		return nil, false
	}
	current, present, _ := frictionWasherDrag(child)
	if !present {
		// 字段缺失是新物品或历史存档：按满寿命，不能当成已烧穿。
		current = rated
	}
	lives[reelPartCatWasher] = reelWasherLife(current, rated)
	for _, category := range []int64{reelPartCatOil, reelPartCatGear, reelPartCatBearing, reelPartCatHandle, reelPartCatSpool} {
		part, _, _, found := s.selectedReelPart(ctx, fields, category)
		if !found || part == nil {
			lives[category] = 1
			continue
		}
		lives[category] = reelChildDurability(part)
	}
	return lives, true
}

// reelBodyDurabilityInFields 按"本体耐久 = 内部零件综合"算出渔轮本体此刻的耐久：
// 任一熔断件寿命归零就是 0；否则取 min(加权寿命, 最弱熔断件 + 容差)。
// 缺件按满寿命处理，不会因为老存档少了某个件就误判成损坏。
func (s *Server) reelBodyDurabilityInFields(ctx context.Context, reel *store.Item, fields []proto.Field) (float64, bool) {
	lives, ok := s.reelPartLivesInFields(ctx, reel, fields)
	if !ok {
		return 0, false
	}
	weighted := reelBodyWeightWasher*lives[reelPartCatWasher] +
		reelBodyWeightGear*lives[reelPartCatGear] +
		reelBodyWeightBearing*lives[reelPartCatBearing] +
		reelBodyWeightSpool*lives[reelPartCatSpool] +
		reelBodyWeightHandle*lives[reelPartCatHandle] +
		reelBodyWeightOil*lives[reelPartCatOil]
	weakest := math.Inf(1)
	for _, category := range reelBodyFuseCategories {
		life := lives[category]
		if life <= 0 {
			return 0, true
		}
		if life < weakest {
			weakest = life
		}
	}
	return clampUnit(math.Min(weighted, weakest+reelBodyWeakSpotTolerance)), true
}

// recalcReelBodyDurability 换件或修理之后重算本体耐久，并允许上调（不走单调保护）。
// 本体是里面零件的综合：换掉烧穿的摩擦片必须能把外面那格抬起来，否则永远修不好。
// 返回是否真的抬起来了。
func (s *Server) recalcReelBodyDurability(ctx context.Context, row *store.Item) bool {
	if s == nil || s.DB == nil || row == nil {
		return false
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return false
	}
	body, ok := s.reelBodyDurabilityInFields(ctx, row, fields)
	if !ok || body <= row.Durability {
		return false
	}
	if err := s.DB.SetItemDurability(ctx, row.GUID, body); err != nil {
		return false
	}
	row.Durability = body
	return true
}

// restoreReelInternals 进店修理渔轮时把轮内零件一并恢复：摩擦片制动力回到额定、其余件
// 耐久回满。本体修好了但摩擦片还是 0 的话，轮子照样收不了线，修理就成了白扣钱。
// 只改零件字段，本体耐久由调用方按修理流程写。老档没有轮内零件时 changed=false 且
// err=nil，修理照常生效；写库失败必须让调用方放弃这次修理，别只把本体刷回满值。
func (s *Server) restoreReelInternals(ctx context.Context, row *store.Item) (bool, error) {
	if s == nil || s.DB == nil || row == nil {
		return false, nil
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return false, err
	}
	changed := false
	child, def, _, ok := s.selectedReelPart(ctx, fields, reelPartCatWasher)
	if ok && child != nil && def != nil {
		if _, present, idx := frictionWasherDrag(child); present && idx >= 0 && idx < len(child.Fields) {
			if rated := itemDefF32(def, gearAttrDragMax) * row.Workmanship; rated > 0 &&
				setWireFloat(child.Fields, child.Class, idx, rated) {
				changed = true
			}
		}
		if setReelChildDurability(child, 1) {
			changed = true
		}
	}
	for _, category := range reelWearPartCategories {
		part, _, _, found := s.selectedReelPart(ctx, fields, category)
		if !found || part == nil {
			continue
		}
		if setReelChildDurability(part, 1) {
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	bumpCreatedTicks(fields, row.WireClass)
	fj, err := FieldsToJSON(fields)
	if err != nil {
		return false, err
	}
	snapshot := *row
	snapshot.FieldsJSON = fj
	if s.canonicalizeReelStrength(ctx, &snapshot, fields) {
		if fj, err = FieldsToJSON(fields); err != nil {
			return false, err
		}
	}
	if err := s.DB.UpdateItemFields(ctx, row.GUID, fj, row.Qty, row.Durability, row.Workmanship, row.RigGUID); err != nil {
		return false, err
	}
	row.FieldsJSON = fj
	return true, nil
}
