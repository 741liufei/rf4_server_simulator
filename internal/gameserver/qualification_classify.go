package gameserver

import (
	"strconv"
	"strings"
)

// 资格任务条件判定 helper。Counted 工厂把「一组布尔过滤 + 一个去重键」组合成
// 规则表里的一行；过滤全是纯函数，只读事件携带的本次数据，不查库。
//
// 拟饵分类按 item_id 前缀（item_def category=17 实测前缀）：spinner→旋转亮片、
// spoon→勺子路亚、crank/plug/shad→鱼形硬饵(wobbler)、topwater/popper→表水路亚、
// jerkbait→抽停硬饵。与成就系统 baitPrefix 同源，前缀匹配兼容复数形式
// （spinnerbaits/crankbaits/jerkbaits/poppers）。

// lureFamily 返回拟饵 item_id 所属的拟饵族；非上述硬饵/亮片族返回空串。
func lureFamily(bait string) string {
	switch {
	case strings.HasPrefix(bait, "spinner"):
		return "spinner"
	case strings.HasPrefix(bait, "spoon"):
		return "spoon"
	case strings.HasPrefix(bait, "topwater"), strings.HasPrefix(bait, "popper"):
		return "topwater"
	case strings.HasPrefix(bait, "jerkbait"):
		return "jerkbait"
	case strings.HasPrefix(bait, "crank"), strings.HasPrefix(bait, "plug"), strings.HasPrefix(bait, "shad"):
		return "wobbler"
	default:
		return ""
	}
}

type qFilter func(qualificationEvent) bool

func qWeightAtLeast(g int64) qFilter {
	return func(ev qualificationEvent) bool { return ev.WeightG >= g }
}

func qWeightAtMost(g int64) qFilter {
	return func(ev qualificationEvent) bool { return ev.WeightG > 0 && ev.WeightG <= g }
}

// qRarityAtLeast 物种稀有度（loc_wiki_fish/species_rarity：0 普通 1 稀有 2 超稀有）。
// 文案「稀有鱼」按物种稀有度 >=1 判，「超级稀有/罕见」按 >=2 判。
func qRarityAtLeast(n int64) qFilter {
	return func(ev qualificationEvent) bool { return ev.Rarity >= n }
}

func qSpeciesIn(keys ...string) qFilter {
	return func(ev qualificationEvent) bool {
		for _, k := range keys {
			if ev.Species == k {
				return true
			}
		}
		return false
	}
}

func qBaitCategory(cat int64) qFilter {
	return func(ev qualificationEvent) bool { return ev.BaitCat == cat }
}

func qLocLevelIn(levels ...string) qFilter {
	return func(ev qualificationEvent) bool {
		for _, lv := range levels {
			if ev.LocLevel == lv {
				return true
			}
		}
		return false
	}
}

func qLureFamilyIn(families ...string) qFilter {
	return func(ev qualificationEvent) bool {
		f := lureFamily(ev.Bait)
		for _, want := range families {
			if f == want {
				return true
			}
		}
		return false
	}
}

// qArtificialLure 人造拟饵钓组：本杆钓组属收索拟饵族（rigmeta.go:rigTypeLureFamily）。
func qArtificialLure() qFilter {
	return func(ev qualificationEvent) bool { return rigTypeLureFamily[ev.RigType] }
}

// jigRigTypes 汲钩钓族（铅头钩/软饵系钓组）。分支 2 文案里的「任何汲钩钓钓组」
// 覆盖 SimpleJig/Texas/DropShot/Carolina/ThreeWay/Wacky/HairyJig；2601 显式要求
// 「除了维基钩组」，说明维基本就属于该族。钓组中文名取自客户端本地化表。
var jigRigTypes = map[int64]bool{
	6: true, 7: true, 8: true, 9: true, 10: true, 11: true, 35: true,
}

func qJigRig() qFilter {
	return func(ev qualificationEvent) bool { return jigRigTypes[ev.RigType] }
}

func qRigIn(rts ...int64) qFilter {
	return func(ev qualificationEvent) bool {
		for _, rt := range rts {
			if ev.RigType == rt {
				return true
			}
		}
		return false
	}
}

func qDepthAtLeast(cm int64) qFilter {
	return func(ev qualificationEvent) bool { return ev.DepthCM >= cm }
}

func qFromShore() qFilter {
	return func(ev qualificationEvent) bool { return ev.IsShore }
}

// rodPowerClass 取竿 item_id 尾缀的硬度档（UL/L/ML/M/MH/H/XH…）。
// 厂商命名即以此区分超轻/轻型；无尾缀（手竿/浮钓/底钓/海竿）返回空串。
func rodPowerClass(itemID string) string {
	i := len(itemID)
	for i > 0 && itemID[i-1] >= 'A' && itemID[i-1] <= 'Z' {
		i--
	}
	return itemID[i:]
}

func qUltralight() qFilter {
	return func(ev qualificationEvent) bool {
		if ev.RodPower == "UL" || ev.RodPower == "ULL" {
			return true
		}
		// 手竿等没有硬度尾缀，按测试上限 <=5g 视为超轻。
		return ev.RodTestMax > 0 && ev.RodTestMax <= 5
	}
}

// qLight 轻型渔具：含超轻（UL/ULL/测试上限 <=5g）与轻（L）。
func qLight() qFilter {
	ul := qUltralight()
	return func(ev qualificationEvent) bool { return ul(ev) || ev.RodPower == "L" }
}

// qLightSpinning / qULSpinning 轻型/超轻型纺车竿（竿型 1）。
func qLightSpinning() qFilter {
	light := qLight()
	return func(ev qualificationEvent) bool { return ev.RodType == 1 && light(ev) }
}

func qULSpinning() qFilter {
	ul := qUltralight()
	return func(ev qualificationEvent) bool { return ev.RodType == 1 && ul(ev) }
}

// qLureLenAtMost 拟饵长度上限（厘米）；「小拟饵」按不超过 4cm 判。
func qLureLenAtMost(cm float64) qFilter {
	return func(ev qualificationEvent) bool {
		return ev.BaitCat == 17 && ev.LureLenCM > 0 && ev.LureLenCM <= cm
	}
}

// qBaitClassIn 天然饵饵类（2/7=昆虫饵）。
func qBaitClassIn(classes ...int64) qFilter {
	return func(ev qualificationEvent) bool {
		for _, c := range classes {
			if ev.BaitClass == c {
				return true
			}
		}
		return false
	}
}

func qXPAtLeast(xp int64) qFilter {
	return func(ev qualificationEvent) bool { return ev.XP >= xp }
}

func qIsNight() qFilter {
	return func(ev qualificationEvent) bool { return ev.IsNight }
}

// qDrifting 顺水漂流：咬钩时客户端上报过 0x40 StreamMoving。
func qDrifting() qFilter {
	return func(ev qualificationEvent) bool { return ev.Drift }
}

// qLineMaterialIn 主线或前导线任一命中材质档（1 尼龙 2 碳线）。
func qLineMaterialIn(materials ...int64) qFilter {
	return func(ev qualificationEvent) bool {
		for _, m := range materials {
			if ev.MainLine == m || ev.LeaderMat == m {
				return true
			}
		}
		return false
	}
}

// qRodIn 竿型命中（博洛尼亚=4、竞赛=5、Picker=9、飞德=10）。
func qRodIn(types ...int64) qFilter {
	return func(ev qualificationEvent) bool {
		for _, t := range types {
			if ev.RodType == t {
				return true
			}
		}
		return false
	}
}

func qBaitPrefix(prefix string) qFilter {
	return func(ev qualificationEvent) bool { return strings.HasPrefix(ev.Bait, prefix) }
}

// qBaitSizeAtLeast / qBaitSizeAtMost 天然饵尺寸（毫米）。
func qBaitSizeAtLeast(mm float64) qFilter {
	return func(ev qualificationEvent) bool { return ev.BaitSizeMM >= mm }
}

func qBaitSizeAtMost(mm float64) qFilter {
	return func(ev qualificationEvent) bool { return ev.BaitSizeMM > 0 && ev.BaitSizeMM <= mm }
}

func qCastDistanceAtLeast(m float64) qFilter {
	return func(ev qualificationEvent) bool { return ev.CastM >= m }
}

func qPassAll(ev qualificationEvent, filters []qFilter) bool {
	for _, f := range filters {
		if !f(ev) {
			return false
		}
	}
	return true
}

// qCountSpecies 过滤通过则按鱼种去重计数（「钓获 N 种鱼」）。
func qCountSpecies(filters ...qFilter) func(qualificationEvent) string {
	return func(ev qualificationEvent) string {
		if !qPassAll(ev, filters) {
			return ""
		}
		return ev.Species
	}
}

// qCountSingle 过滤通过则计一尾（单尾条件型，目标值通常为 1）；按本尾鱼 GUID 去重。
func qCountSingle(filters ...qFilter) func(qualificationEvent) string {
	return func(ev qualificationEvent) string {
		if !qPassAll(ev, filters) || ev.CatchGUID == "" {
			return ""
		}
		return ev.CatchGUID
	}
}

// qCountLureItem 过滤通过则按拟饵 item_id 去重计数（「用 N 种不同拟饵钓到鱼」）。
func qCountLureItem(filters ...qFilter) func(qualificationEvent) string {
	return func(ev qualificationEvent) string {
		if !qPassAll(ev, filters) || ev.Bait == "" {
			return ""
		}
		return ev.Bait
	}
}

// qCountBaitItem 过滤通过则按饵 item_id 去重计数（「用 N 种不同的鱼饵」）。
func qCountBaitItem(filters ...qFilter) func(qualificationEvent) string {
	return func(ev qualificationEvent) string {
		if !qPassAll(ev, filters) || ev.Bait == "" {
			return ""
		}
		return ev.Bait
	}
}

// qCountBaitFlavor 过滤通过则按味型去重计数（「N 种不同味道的颗粒」）。
func qCountBaitFlavor(filters ...qFilter) func(qualificationEvent) string {
	return func(ev qualificationEvent) string {
		if !qPassAll(ev, filters) || ev.BaitFlavor <= 0 {
			return ""
		}
		return "flavor:" + strconv.FormatInt(ev.BaitFlavor, 10)
	}
}

// qCountRig 过滤通过则按钓组类型去重计数（「使用每种 X 钓组」）。
func qCountRig(filters ...qFilter) func(qualificationEvent) string {
	return func(ev qualificationEvent) string {
		if !qPassAll(ev, filters) || ev.RigType <= 0 {
			return ""
		}
		return "rig:" + strconv.FormatInt(ev.RigType, 10)
	}
}

// qCountLureFamily 过滤通过则按拟饵族去重计数（「用每种以下拟饵钓获稀有鱼」）。
func qCountLureFamily(filters ...qFilter) func(qualificationEvent) string {
	return func(ev qualificationEvent) string {
		if !qPassAll(ev, filters) {
			return ""
		}
		fam := lureFamily(ev.Bait)
		if fam == "" {
			return ""
		}
		return fam
	}
}
