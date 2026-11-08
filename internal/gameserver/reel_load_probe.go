package gameserver

// 14/8 负载上报的字段观测器。
//
// 搏鱼结算要靠"真实时长 + 真实负载"才算得准。时长这条已经改用服务端收到上报的
// 真实间隔（见 fightReelTickWear）；负载还不能进结算：命令表里 14/8 没有公开的
// 字段定义，哪个载荷是鱼拉力、它是整数还是浮点位型，都还没有第二份证据。
// 所以本竿先只做观测：把搏鱼期间收到的每条 14/8 按字段路径累计次数、最小值与
// 最大值，一起写进本竿的渔轮损伤日志，作为下一步挑字段的原始凭据。
// 观测不参与任何结算，关掉开关只是少几个诊断字段。
//
// 线型里浮点没有独立 marker：F32 借 u32（marker 0x10）、F64 借 u64（marker 0x11）
// 承载位型（见 proto.F32 / proto.F64），所以整数族同时给按位型的再读法。

import (
	"math"
	"strconv"

	"rf4server/internal/proto"
)

const (
	// loadProbeMaxSlots 是单竿最多跟踪的字段路径数，超出只累计 dropped。
	loadProbeMaxSlots = 12
	// loadProbeMaxLeaves 是单条 14/8 最多走访的叶子数，防止畸形树撑爆日志。
	loadProbeMaxLeaves = 24
	// loadProbeMaxDepth 是走访的最大层级。
	loadProbeMaxDepth = 4
)

// loadProbeKind 是被跟踪读数的种类。
const (
	loadKindU32  = "u32"
	loadKindU64  = "u64"
	loadKindBool = "bool"
	loadKindF64  = "f64"
)

// loadProbeSlot 是一个字段路径在本竿搏鱼期间的累计读数。
type loadProbeSlot struct {
	path  string
	kind  string
	count int
	// 整数族用 raw*，浮点族用 f*。
	rawMin uint64
	rawMax uint64
	first  uint64
	last   uint64
	fMin   float64
	fMax   float64
	// bitsMin/bitsMax 把整数载荷按 F32/F64 位型再读一遍，只在读数落在合理区间
	// 时才更新：小整数会解出 denormal 噪声，全表铺开反而看不清哪个是负载。
	bitsMin float64
	bitsMax float64
}

// fightLoadProbe 累计一竿搏鱼收到的 14/8 负载上报。
// 挂在 fightReelWear 的非导出字段上，因此不会进钓组状态的 JSON 快照。
type fightLoadProbe struct {
	reports   int
	undecoded int
	dropped   int
	slots     []loadProbeSlot
}

// observe 累计一条 14/8 上报。ok 为 false 表示这条报文连值树都解不出来，
// 只记一笔 undecoded，不再猜字段。
func (p *fightLoadProbe) observe(tree *proto.Value, ok bool) {
	if p == nil {
		return
	}
	p.reports++
	if !ok || tree == nil {
		p.undecoded++
		return
	}
	budget := loadProbeMaxLeaves
	p.walk(tree, "", 0, &budget)
}

// walk 按位置编码走访值树，只记数值叶子。
func (p *fightLoadProbe) walk(v *proto.Value, path string, depth int, budget *int) {
	if v == nil || *budget <= 0 || depth > loadProbeMaxDepth {
		return
	}
	switch v.Kind {
	case proto.KindU32:
		p.addNum(path, loadKindU32, v.Num, float64(proto.F32FromBits(uint32(v.Num))))
		return
	case proto.KindU64:
		p.addNum(path, loadKindU64, v.Num, proto.F64FromBits(v.Num))
		return
	case proto.KindDateTime:
		p.addNum(path, "ts", v.Num, math.NaN())
		return
	case proto.KindBool:
		var n uint64
		if v.Bool {
			n = 1
		}
		p.addNum(path, loadKindBool, n, math.NaN())
		return
	case proto.KindObj:
		for i := range v.Fields {
			f := &v.Fields[i]
			at := path + "." + strconv.Itoa(i)
			if f.Val != nil {
				p.walk(f.Val, at, depth+1, budget)
				continue
			}
			if *budget <= 0 {
				return
			}
			*budget--
			switch {
			case f.Num != nil:
				p.addNum(at, probeFieldKind(f.Op), *f.Num, probeBits(f.Op, *f.Num))
			case f.Op == "FLOAT" || f.Op == "DOUBLE":
				p.addFloat(at, f.Float)
			}
		}
		return
	case proto.KindDict:
		for i := range v.Pairs {
			if v.Pairs[i].V != nil {
				p.walk(v.Pairs[i].V, path+"."+strconv.Itoa(i), depth+1, budget)
			}
		}
		return
	case proto.KindArgs, proto.KindValList, proto.KindArray, proto.KindKVList,
		proto.KindU32Arr, proto.KindGuidArr, proto.KindTypedU32Arr:
		for i := range v.Items {
			p.walk(v.Items[i], path+"."+strconv.Itoa(i), depth+1, budget)
		}
		return
	}
}

// addNum 记一个整数族读数，bits 是同一载荷按浮点位型的再读法（NaN 表示不适用）。
func (p *fightLoadProbe) addNum(path, kind string, raw uint64, bits float64) {
	s := p.slot(path, kind)
	if s == nil {
		p.dropped++
		return
	}
	if s.count == 0 {
		s.rawMin, s.rawMax, s.first, s.last = raw, raw, raw, raw
	} else {
		if raw < s.rawMin {
			s.rawMin = raw
		}
		if raw > s.rawMax {
			s.rawMax = raw
		}
		s.last = raw
	}
	s.count++
	if plausibleProbeFloat(bits) {
		if math.IsNaN(s.bitsMin) {
			s.bitsMin, s.bitsMax = bits, bits
		} else {
			if bits < s.bitsMin {
				s.bitsMin = bits
			}
			if bits > s.bitsMax {
				s.bitsMax = bits
			}
		}
	}
}

// addFloat 记一个浮点族读数（对象字段在线型表里已声明 FLOAT/DOUBLE）。
func (p *fightLoadProbe) addFloat(path string, v float64) {
	s := p.slot(path, loadKindF64)
	if s == nil {
		p.dropped++
		return
	}
	if s.count == 0 {
		s.fMin, s.fMax = v, v
	} else {
		if v < s.fMin {
			s.fMin = v
		}
		if v > s.fMax {
			s.fMax = v
		}
	}
	s.count++
}

// slot 按 (路径, 种类) 找累计槽，没有就新建；槽满返回 nil。
func (p *fightLoadProbe) slot(path, kind string) *loadProbeSlot {
	if path == "" {
		path = "$"
	}
	for i := range p.slots {
		if p.slots[i].path == path && p.slots[i].kind == kind {
			return &p.slots[i]
		}
	}
	if len(p.slots) >= loadProbeMaxSlots {
		return nil
	}
	p.slots = append(p.slots, loadProbeSlot{
		path: path, kind: kind, bitsMin: math.NaN(), bitsMax: math.NaN(),
	})
	return &p.slots[len(p.slots)-1]
}

// logFields 给出可写进损伤日志的诊断字段；一条上报都没收到时返回 nil。
func (p fightLoadProbe) logFields() map[string]any {
	if p.reports == 0 {
		return nil
	}
	out := map[string]any{"load_reports": p.reports}
	if p.undecoded > 0 {
		out["load_undecoded"] = p.undecoded
	}
	if p.dropped > 0 {
		out["load_dropped"] = p.dropped
	}
	if len(p.slots) == 0 {
		return out
	}
	fields := make([]map[string]any, 0, len(p.slots))
	for _, s := range p.slots {
		row := map[string]any{"path": s.path, "kind": s.kind, "n": s.count}
		if s.kind == loadKindF64 {
			row["min"] = roundProbeFloat(s.fMin)
			row["max"] = roundProbeFloat(s.fMax)
		} else {
			row["min"] = s.rawMin
			row["max"] = s.rawMax
			if s.rawMin != s.rawMax {
				row["last"] = s.last
			}
		}
		if !math.IsNaN(s.bitsMin) {
			row["bits_min"] = roundProbeFloat(s.bitsMin)
			row["bits_max"] = roundProbeFloat(s.bitsMax)
		}
		fields = append(fields, row)
	}
	out["load_fields"] = fields
	return out
}

// plausibleProbeFloat 判断按位型解出来的读数是否像个数：太小太大都不如不给，
// 免得整数序号解出的 denormal 把真正的负载读数淹没。
func plausibleProbeFloat(v float64) bool {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return false
	}
	abs := math.Abs(v)
	return abs >= 1e-4 && abs <= 1e7
}

// roundProbeFloat 把读数收敛到 4 位小数，避免浮点噪声占满日志。
func roundProbeFloat(v float64) float64 {
	return math.Round(v*10000) / 10000
}

// probeFieldKind 把对象字段的 op 名映射成观测种类。
func probeFieldKind(op string) string {
	switch op {
	case "U64":
		return loadKindU64
	case "BYTE", "U16", "OPT":
		return "int"
	default:
		return loadKindU32
	}
}

// probeBits 给出对象字段整数载荷的浮点位型读法：只有 U32 按 F32、U64 按 F64，
// 其余（BYTE/U16/序号）不给。
func probeBits(op string, raw uint64) float64 {
	switch op {
	case "U32":
		return float64(proto.F32FromBits(uint32(raw)))
	case "U64":
		return proto.F64FromBits(raw)
	default:
		return math.NaN()
	}
}
