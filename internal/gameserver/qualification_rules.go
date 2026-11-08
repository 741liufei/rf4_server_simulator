package gameserver

import (
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"rf4server/internal/store"
)

// 资格规则：每条任务把「已计数渔获」累积成一个集合，客户端进度分子就是集合
// 长度（5004.Guid[] 长度），分母是规则 5007.Idx3；计数器完成时刻 > 0 时直接判满。
//
//	上鱼事件 → Counted(事件) 得到去重键 → 并入集合 → 写 player_qualifications
//
// 与成就系统同构：事件只携带本次判断需要的数据，由玩法入口在成功路径上装配；
// Counted 是纯函数，不查库、不访问 ctx。判定在 14/4 上鱼结算时发生，达标当尾
// 的结算响应即带上完成（避免慢一拍）；达标后封顶不再计数，14/4 只增量下发本次
// 变化的计数器（避免完成后每尾鱼重复弹完成窗），28/2 登录发全量快照。
//
// 条件来源：任务文案（客户端本地化键 qlfctn_t_<id>）。分支只是分组，具体条件
// 全在任务文案里（如分支 4 任务 4303「使用超轻钓具」），因此 1001「钓获 3 种
// 不同的鱼」按鱼种去重计数，不附加钓具过滤。

type qualificationEventKind uint8

const (
	qevFishLanded qualificationEventKind = iota + 1 // 14/4 上鱼结算
)

// qualificationEvent 只携带判断需要的本次事件数据。
type qualificationEvent struct {
	Kind       qualificationEventKind
	Species    string  // 鱼种 key（去重计数用）
	NameZh     string  // 中文名（鱼记录渲染用）
	WeightG    int64   // 体重（克）
	LengthM    float64 // 体长（米）
	RigType    int64   // 本杆钓组类型（rigmeta.go:rigTypeNames）
	RodType    int64   // 竿型（celffbjpmma）
	RodPower   string  // 竿硬度档尾缀（UL/L/ML/…，rodPowerClass）
	Bait       string  // 钩上饵/拟饵 item_id
	BaitCat    int64   // 饵品类（17 拟饵 / 27 天然饵）
	BaitClass  int64   // 天然饵饵类（item_def fopbhlmkcpe：2/7=昆虫，9=颗粒，13=面团）
	BaitFlavor int64   // 天然饵味型（item_def acclagajcdn）
	LureLenCM  float64 // 拟饵长度（厘米，item_def bbhhojphpfa）
	BaitSizeMM float64 // 饵尺寸/直径（毫米，item_def hkgndoipdlc）
	Grade      int64   // 档位（1..4，4=蓝冠）
	CatchGUID  string  // 本尾鱼 echo GUID
	Rarity     int64   // 物种稀有度（species_rarity：0 普通 1 稀有 2 超稀有）
	DepthCM    int64   // 钓深（厘米）
	IsShore    bool    // 岸钓（非船上）
	IsNight    bool    // 夜间（19:00–06:00，worldDayAndHour）
	Drift      bool    // 咬钩时钓组处于顺流移动（14/7 0x40 StreamMoving）
	LocLevel   string  // 水域 key（地点过滤用）
	XP         int64   // 本尾鱼经验（pending.XP，settleCatchXP 写入）
	MainLine   int64   // 主线材质（1 尼龙 2 碳线 3 编织…）
	LeaderMat  int64   // 前导线材质（1 尼龙 2 碳线…）
	CastM      float64 // 本杆抛投距离（米）
	RodTestMax float64 // 竿测试上限（克，rodTestG）
}

type qualificationRuleDef struct {
	ID    int64
	Event qualificationEventKind
	// Counted 返回本次事件应计入的去重键；空串表示本次不计入。
	Counted func(qualificationEvent) string
}

// 竿型枚举（celffbjpmma）：1=纺车 2=枪柄 3=手竿 4=博洛尼亚 5=竞赛
// 9=Picker 10=飞德 11=鲤竿 15=海钓铁板 16=海钓船竿。
const (
	rodTypeSpinning  = 1
	rodTypeCasting   = 2
	rodTypeTelescope = 3
	rodTypeBolognese = 4
	rodTypeMatch     = 5
	rodTypePicker    = 9
	rodTypeFeeder    = 10
	rodTypeCarp      = 11
	rodTypePilker    = 15
	rodTypeMarine    = 16
)

// qualificationRodTypesFor 返回任务允许的竿型（celffbjpmma）；空=任意。
// 同一份表同时用于服务端过滤与 5007.Idx5 下发，保证「鱼竿」文案与判定一致。
func qualificationRodTypesFor(ruleID int64) []int64 {
	switch ruleID / 1000 {
	case 1:
		return []int64{rodTypeSpinning}
	case 2:
		return []int64{rodTypeSpinning, rodTypeCasting}
	case 3:
		switch ruleID {
		case 3302, 3501, 3601: // 明确写「纺车竿」
			return []int64{rodTypeSpinning}
		default:
			return []int64{rodTypeSpinning, rodTypeCasting}
		}
	case 4:
		return []int64{rodTypeTelescope}
	case 5:
		switch ruleID {
		case 5001, 5002, 5003, 5101, 5102, 5103, 5201, 5202, 5203, 5401:
			return []int64{rodTypeBolognese}
		case 5301, 5302, 5303, 5601, 5602:
			return []int64{rodTypeBolognese, rodTypeMatch}
		default: // 5402/5403/5501/5502/5503/5603 竞赛竿/竞赛钓组
			return []int64{rodTypeMatch}
		}
	case 6:
		switch ruleID {
		case 6001, 6002, 6003, 6101, 6103:
			return []int64{rodTypeFeeder}
		case 6202, 6303, 6402, 6501:
			return []int64{rodTypePicker}
		default:
			return []int64{rodTypePicker, rodTypeFeeder}
		}
	case 7:
		return []int64{rodTypeCarp}
	case 8:
		return []int64{rodTypePilker, rodTypeMarine}
	default:
		return nil
	}
}

// qualificationRodPowerFor 返回任务的竿硬度档限制（befnkekakei：1=UL 2=L）。
func qualificationRodPowerFor(ruleID int64) []int64 {
	if ruleID/1000 == 3 {
		return []int64{1, 2}
	}
	if ruleID == 4303 || ruleID == 4502 {
		return []int64{1}
	}
	return nil
}

// qualificationRigClassFor 返回任务的钓组大类（efgbdocjcan：1 浮钓 2 路亚 3 底钓）。
func qualificationRigClassFor(ruleID int64) int64 {
	switch ruleID / 1000 {
	case 1, 2, 3:
		return 2
	case 4, 5:
		return 1
	case 6, 7:
		return 3
	default:
		return 0
	}
}

// driftRuleIDs 是要求「顺水漂流」的任务（5007.Idx27=64 StreamMoving）。
var driftRuleIDs = map[int64]bool{
	4403: true, 5002: true, 5101: true, 5102: true, 5103: true,
	5201: true, 5301: true, 5303: true, 5401: true,
}

// qualificationRodAllowed 在事件带竿型时校验任务竿型；竿型未知（0）不拦。
func qualificationRodAllowed(types []int64, rodType int64) bool {
	if len(types) == 0 || rodType == 0 {
		return true
	}
	for _, t := range types {
		if rodType == t {
			return true
		}
	}
	return false
}

// 鲤鱼分支常用集合：人面鲤（各体型 ghost 变种）与白化鲤鱼。
var (
	ghostCarpKeys = []string{
		"carp_ghost", "l_carp_ghost", "m_carp_ghost", "n_carp_ghost", "r_carp_ghost",
	}
	albinoCarpKeys = []string{
		"carp_albino", "l_carp_albino", "m_carp_albino", "n_carp_albino", "r_carp_albino",
	}
)

// 每增加一条任务，只加一行：绑定事件 + 一个去重键函数。
// 「稀有」按物种稀有度 >=1；「鱼形硬饵」按拟饵族 wobbler；「人造拟饵钓组」按
// 收索拟饵族；「汲钩钓钓组」按铅头钩/软饵族。水域与钓组中文名均取自客户端本地化表。
var qualificationRules = []qualificationRuleDef{
	// ---- 分支 1 传统纺车竿（阶段 1-7）----
	// 阶段 1
	{ID: 1001, Event: qevFishLanded, Counted: qCountSpecies()},
	{ID: 1002, Event: qevFishLanded, Counted: qCountSingle(qWeightAtLeast(3000))},
	{ID: 1003, Event: qevFishLanded, Counted: qCountLureItem(qBaitCategory(17))},
	// 阶段 2
	{ID: 1101, Event: qevFishLanded, Counted: qCountSingle(qLureFamilyIn("spinner"), qWeightAtLeast(5000))},
	{ID: 1102, Event: qevFishLanded, Counted: qCountSpecies(qLureFamilyIn("spoon", "spinner"))},
	{ID: 1103, Event: qevFishLanded, Counted: qCountSpecies(qLureFamilyIn("wobbler"))},
	// 阶段 3
	{ID: 1201, Event: qevFishLanded, Counted: qCountSingle(qArtificialLure(), qLocLevelIn("level_010_belaya"), qWeightAtLeast(5000), qSpeciesIn("taimen"), qRarityAtLeast(1))},
	{ID: 1202, Event: qevFishLanded, Counted: qCountSingle(qArtificialLure(), qWeightAtLeast(7000), qSpeciesIn("sevan_trout"))},
	{ID: 1203, Event: qevFishLanded, Counted: qCountSingle(qArtificialLure(), qSpeciesIn("gc_smelt"), qRarityAtLeast(1))},
	// 阶段 4
	{ID: 1301, Event: qevFishLanded, Counted: qCountSingle(qLureFamilyIn("wobbler"), qDepthAtLeast(300), qRarityAtLeast(1))},
	{ID: 1302, Event: qevFishLanded, Counted: qCountLureFamily(qLureFamilyIn("spoon", "spinner", "wobbler", "topwater"), qRarityAtLeast(1))},
	{ID: 1303, Event: qevFishLanded, Counted: qCountSingle(qSpeciesIn("a_char"), qRarityAtLeast(1))},
	// 阶段 5
	{ID: 1401, Event: qevFishLanded, Counted: qCountSpecies(qArtificialLure())},
	{ID: 1402, Event: qevFishLanded, Counted: qCountSingle(qLureFamilyIn("jerkbait"), qSpeciesIn("n.pike"), qRarityAtLeast(1))},
	{ID: 1403, Event: qevFishLanded, Counted: qCountSingle(qFromShore(), qLureFamilyIn("spoon"), qSpeciesIn("asp"), qRarityAtLeast(1))},
	// 阶段 6
	{ID: 1501, Event: qevFishLanded, Counted: qCountSingle(qLureFamilyIn("topwater"), qSpeciesIn("lenok_o"), qRarityAtLeast(1))},
	{ID: 1502, Event: qevFishLanded, Counted: qCountSingle(qSpeciesIn("chartus_kuoriius"), qRarityAtLeast(1))},
	{ID: 1503, Event: qevFishLanded, Counted: qCountSingle(qArtificialLure(), qSpeciesIn("a_salmon"), qRarityAtLeast(1))},
	// 阶段 7
	{ID: 1601, Event: qevFishLanded, Counted: qCountSingle(qFromShore(), qArtificialLure(), qSpeciesIn("nelma", "wfish"), qRarityAtLeast(1))},
	{ID: 1602, Event: qevFishLanded, Counted: qCountSingle(qArtificialLure(), qSpeciesIn("k_salmon"), qRarityAtLeast(1))},
	{ID: 1603, Event: qevFishLanded, Counted: qCountSingle(qArtificialLure(), qSpeciesIn("taimen"), qRarityAtLeast(1))},

	// ---- 分支 2 汲钩钓(jigging)（阶段 1-7）----
	// 阶段 1
	{ID: 2001, Event: qevFishLanded, Counted: qCountSpecies(qJigRig())},
	{ID: 2002, Event: qevFishLanded, Counted: qCountSingle(qJigRig(), qWeightAtLeast(2000))},
	{ID: 2003, Event: qevFishLanded, Counted: qCountSingle(qJigRig(), qSpeciesIn("e.pikeperch"))},
	// 阶段 2
	{ID: 2101, Event: qevFishLanded, Counted: qCountSpecies(qJigRig())},
	{ID: 2102, Event: qevFishLanded, Counted: qCountSingle(qJigRig(), qWeightAtLeast(5000))},
	{ID: 2103, Event: qevFishLanded, Counted: qCountSingle(qRigIn(10), qSpeciesIn("perch"), qRarityAtLeast(1))},
	// 阶段 3
	{ID: 2201, Event: qevFishLanded, Counted: qCountSpecies(qJigRig())},
	{ID: 2202, Event: qevFishLanded, Counted: qCountSingle(qJigRig(), qSpeciesIn("n.pike"), qWeightAtLeast(7000))},
	{ID: 2203, Event: qevFishLanded, Counted: qCountSingle(qJigRig(), qSpeciesIn("barsch"), qRarityAtLeast(1))},
	// 阶段 4
	{ID: 2301, Event: qevFishLanded, Counted: qCountSingle(qJigRig(), qLocLevelIn("level_004_torfyanoe_ozero"), qSpeciesIn("w_catfish"), qWeightAtLeast(10000))},
	{ID: 2302, Event: qevFishLanded, Counted: qCountSingle(qJigRig(), qSpeciesIn("burbot"), qRarityAtLeast(1))},
	{ID: 2303, Event: qevFishLanded, Counted: qCountSingle(qRigIn(8), qSpeciesIn("e.pikeperch"), qRarityAtLeast(1))},
	// 阶段 5
	{ID: 2401, Event: qevFishLanded, Counted: qCountSpecies(qJigRig())},
	{ID: 2402, Event: qevFishLanded, Counted: qCountSingle(qRigIn(9, 7), qSpeciesIn("salvel_l"), qRarityAtLeast(1))},
	{ID: 2403, Event: qevFishLanded, Counted: qCountSingle(qRigIn(11), qSpeciesIn("palia_kr"), qRarityAtLeast(1))},
	// 阶段 6
	{ID: 2501, Event: qevFishLanded, Counted: qCountSpecies(qRigIn(11), qWeightAtLeast(5000))},
	{ID: 2502, Event: qevFishLanded, Counted: qCountSingle(qJigRig(), qSpeciesIn("a_catfish"), qRarityAtLeast(1))},
	{ID: 2503, Event: qevFishLanded, Counted: qCountSingle(qJigRig(), qSpeciesIn("w_catfish"), qRarityAtLeast(1))},
	// 阶段 7（2601「除了维基钩组」→ 汲钩族排除 rig_Wacky(11)）
	{ID: 2601, Event: qevFishLanded, Counted: qCountSpecies(qRigIn(6, 7, 8, 9, 10, 35), qRarityAtLeast(1))},
	{ID: 2602, Event: qevFishLanded, Counted: qCountSingle(qJigRig(), qSpeciesIn("eel"), qRarityAtLeast(1))},
	{ID: 2603, Event: qevFishLanded, Counted: qCountSingle(qRigIn(11), qSpeciesIn(
		"l_sturgeon", "osetr_balt", "osetr_ship", "p_sturgeon", "r_sturgeon",
		"st_sturgeon", "sterlet", "syb_sterlet", "zs_sturgeon",
	), qRarityAtLeast(1))},

	// ---- 分支 3 轻型、超轻渔具（阶段 1-7）----
	// 轻型=UL/ULL/L，超轻=UL/ULL（竿 item_id 尾缀）；纺车竿=竿型 1。
	// 「小拟饵」按不超过 4cm 判（与 3301/3402 的 4cm 上限同源）。
	// 阶段 1
	{ID: 3001, Event: qevFishLanded, Counted: qCountSpecies(qLight())},
	{ID: 3002, Event: qevFishLanded, Counted: qCountSingle(qLight(), qWeightAtLeast(1500))},
	{ID: 3003, Event: qevFishLanded, Counted: qCountSingle(qUltralight(), qLureFamilyIn("spinner"), qRarityAtLeast(1))},
	// 阶段 2
	{ID: 3101, Event: qevFishLanded, Counted: qCountSingle(qLight(), qWeightAtLeast(3000))},
	{ID: 3102, Event: qevFishLanded, Counted: qCountSingle(qLight(), qLureLenAtMost(4), qSpeciesIn("c.roach", "ruffe", "s.bream", "wa_bream"))},
	{ID: 3103, Event: qevFishLanded, Counted: qCountSingle(qLight(), qJigRig(), qSpeciesIn("a.sleeper"), qRarityAtLeast(1))},
	// 阶段 3
	{ID: 3201, Event: qevFishLanded, Counted: qCountSingle(qLight(), qWeightAtLeast(5000))},
	{ID: 3202, Event: qevFishLanded, Counted: qCountSingle(qUltralight(), qSpeciesIn("cha_herring"), qRarityAtLeast(1))},
	{ID: 3203, Event: qevFishLanded, Counted: qCountSingle(qUltralight(), qSpeciesIn("pumps_sunfish"), qRarityAtLeast(1))},
	// 阶段 4
	{ID: 3301, Event: qevFishLanded, Counted: qCountSingle(qFromShore(), qUltralight(), qLureFamilyIn("wobbler"), qLureLenAtMost(4), qRarityAtLeast(1))},
	{ID: 3302, Event: qevFishLanded, Counted: qCountSingle(qLightSpinning(), qWeightAtLeast(10000), qRarityAtLeast(1))},
	{ID: 3303, Event: qevFishLanded, Counted: qCountSingle(qUltralight(), qSpeciesIn("a_grayling"), qRarityAtLeast(1))},
	// 阶段 5
	{ID: 3401, Event: qevFishLanded, Counted: qCountSingle(qUltralight(), qWeightAtLeast(2500), qRarityAtLeast(1))},
	{ID: 3402, Event: qevFishLanded, Counted: qCountSingle(qFromShore(), qUltralight(), qLureFamilyIn("topwater"), qLureLenAtMost(4), qRarityAtLeast(1))},
	{ID: 3403, Event: qevFishLanded, Counted: qCountSingle(qUltralight(), qSpeciesIn("b_herring"), qRarityAtLeast(1))},
	// 阶段 6
	{ID: 3501, Event: qevFishLanded, Counted: qCountSpecies(qLightSpinning(), qRarityAtLeast(1))},
	{ID: 3502, Event: qevFishLanded, Counted: qCountSingle(qUltralight(), qSpeciesIn("zs_grayling", "os_grayling"), qRarityAtLeast(1))},
	{ID: 3503, Event: qevFishLanded, Counted: qCountSingle(qUltralight(), qSpeciesIn("a_omul"), qRarityAtLeast(1))},
	// 阶段 7
	{ID: 3601, Event: qevFishLanded, Counted: qCountSpecies(qULSpinning())},
	{ID: 3602, Event: qevFishLanded, Counted: qCountSingle(qUltralight(), qSpeciesIn("nelma", "wfish"), qRarityAtLeast(1))},
	{ID: 3603, Event: qevFishLanded, Counted: qCountSingle(qUltralight(), qSpeciesIn("riv_brown_trout"), qRarityAtLeast(1))},

	// ---- 分支 4 手竿(伸缩竿)捕钓（阶段 1-7）----
	// 文案本身未限定手竿（4303/4502 反而要求超轻钓具），因此按文案条件判定，
	// 不额外加竿型过滤。鱼饵按天然饵（cat27）计；昆虫饵按饵类 2/7。
	// 阶段 1
	{ID: 4001, Event: qevFishLanded, Counted: qCountSpecies()},
	{ID: 4002, Event: qevFishLanded, Counted: qCountSingle(qWeightAtLeast(1000))},
	{ID: 4003, Event: qevFishLanded, Counted: qCountBaitItem(qBaitCategory(27))},
	// 阶段 2
	{ID: 4101, Event: qevFishLanded, Counted: qCountSpecies()},
	{ID: 4102, Event: qevFishLanded, Counted: qCountSingle(qWeightAtLeast(3000))},
	{ID: 4103, Event: qevFishLanded, Counted: qCountSingle(qSpeciesIn("c.carp", "crucian"), qRarityAtLeast(1))},
	// 阶段 3
	{ID: 4201, Event: qevFishLanded, Counted: qCountSingle(qSpeciesIn("a_grayling", "riv_brown_trout"), qWeightAtLeast(1000))},
	{ID: 4202, Event: qevFishLanded, Counted: qCountSingle(qSpeciesIn("bream"), qWeightAtLeast(2000))},
	{ID: 4203, Event: qevFishLanded, Counted: qCountSingle(qSpeciesIn("c.roach", "s.bream"), qRarityAtLeast(1))},
	// 阶段 4
	{ID: 4301, Event: qevFishLanded, Counted: qCountSpecies()},
	{ID: 4302, Event: qevFishLanded, Counted: qCountSingle(qIsNight(), qWeightAtLeast(6000))},
	{ID: 4303, Event: qevFishLanded, Counted: qCountSingle(qUltralight(), qSpeciesIn("dace"), qRarityAtLeast(1))},
	// 阶段 5
	{ID: 4401, Event: qevFishLanded, Counted: qCountSingle(qWeightAtMost(150), qRarityAtLeast(1))},
	{ID: 4402, Event: qevFishLanded, Counted: qCountSingle(qXPAtLeast(7000))},
	{ID: 4403, Event: qevFishLanded, Counted: qCountSingle(qDrifting(), qSpeciesIn("sabrefish", "rudd"), qRarityAtLeast(1))},
	// 阶段 6
	{ID: 4501, Event: qevFishLanded, Counted: qCountSpecies(qBaitClassIn(2, 7))},
	{ID: 4502, Event: qevFishLanded, Counted: qCountSingle(qUltralight(), qLineMaterialIn(1, 2), qWeightAtLeast(500), qRarityAtLeast(1))},
	{ID: 4503, Event: qevFishLanded, Counted: qCountSingle(qWeightAtLeast(5000), qRarityAtLeast(1))},
	// 阶段 7
	{ID: 4601, Event: qevFishLanded, Counted: qCountSpecies(qRarityAtLeast(1))},
	{ID: 4602, Event: qevFishLanded, Counted: qCountSpecies()},
	{ID: 4603, Event: qevFishLanded, Counted: qCountSingle(qDepthAtLeast(400), qSpeciesIn(
		"a_omul", "b_omul", "chir", "muksun", "peled", "ripus", "sig", "sig_black",
		"sig_kuor", "sig_ludoga", "sig_o_l", "sig_ostr", "sig_p", "sig_valaam",
		"sig_vouksa", "sv_sig", "syb_vendace", "tugun", "vendace", "wfish",
	), qRarityAtLeast(1))},

	// ---- 分支 5 博洛尼亚和赛竿（阶段 1-7）----
	// 博洛尼亚竿=竿型 4，竞赛竿=竿型 5；博洛尼亚钓组=rig 2，竞赛钓组=rig 21。
	// 「顺水漂流」按 14/7 StreamMoving(0x40) 咬钩快照判定。
	// 幼虫饵=饵类 2；苍蝇饵=fly 前缀；昆虫饵=饵类 2/7。
	// 阶段 1
	{ID: 5001, Event: qevFishLanded, Counted: qCountSpecies(qRigIn(2))},
	{ID: 5002, Event: qevFishLanded, Counted: qCountSingle(qRigIn(2), qDrifting(), qWeightAtLeast(1000))},
	{ID: 5003, Event: qevFishLanded, Counted: qCountBaitItem(qRigIn(2), qBaitClassIn(2))},
	// 阶段 2
	{ID: 5101, Event: qevFishLanded, Counted: qCountSingle(qBaitPrefix("worm"), qDrifting(), qWeightAtLeast(3000))},
	{ID: 5102, Event: qevFishLanded, Counted: qCountSpecies(qRigIn(2), qBaitPrefix("fly"), qDrifting())},
	{ID: 5103, Event: qevFishLanded, Counted: qCountSingle(qDrifting(), qSpeciesIn("c_bleak"), qRarityAtLeast(1))},
	// 阶段 3
	{ID: 5201, Event: qevFishLanded, Counted: qCountSingle(qDrifting(), qSpeciesIn("wa_bream"), qWeightAtLeast(500))},
	{ID: 5202, Event: qevFishLanded, Counted: qCountSingle(qBaitClassIn(2, 7), qWeightAtLeast(3000))},
	{ID: 5203, Event: qevFishLanded, Counted: qCountSpecies(qRarityAtLeast(1))},
	// 阶段 4
	{ID: 5301, Event: qevFishLanded, Counted: qCountSpecies(qDrifting())},
	{ID: 5302, Event: qevFishLanded, Counted: qCountSingle(qWeightAtLeast(10000))},
	{ID: 5303, Event: qevFishLanded, Counted: qCountSingle(qDrifting(), qSpeciesIn("bream"), qRarityAtLeast(1))},
	// 阶段 5
	{ID: 5401, Event: qevFishLanded, Counted: qCountSingle(qIsNight(), qDrifting(), qRarityAtLeast(1))},
	{ID: 5402, Event: qevFishLanded, Counted: qCountSingle(qCastDistanceAtLeast(30), qXPAtLeast(10000))},
	{ID: 5403, Event: qevFishLanded, Counted: qCountSpecies(qRarityAtLeast(1))},
	// 阶段 6
	{ID: 5501, Event: qevFishLanded, Counted: qCountSingle(qRodIn(5), qRigIn(30, 31), qRarityAtLeast(1))},
	{ID: 5502, Event: qevFishLanded, Counted: qCountSingle(qRigIn(21), qSpeciesIn("b_carp"), qWeightAtLeast(20000))},
	{ID: 5503, Event: qevFishLanded, Counted: qCountSpecies(qRodIn(5), qRarityAtLeast(1))},
	// 阶段 7
	{ID: 5601, Event: qevFishLanded, Counted: qCountSpecies(qRodIn(4, 5), qRarityAtLeast(1))},
	{ID: 5602, Event: qevFishLanded, Counted: qCountSpecies(qRodIn(4, 5))},
	{ID: 5603, Event: qevFishLanded, Counted: qCountSingle(qRigIn(21), qSpeciesIn("carp"), qWeightAtLeast(32000))},

	// ---- 分支 6 飞德钓竿（阶段 1-7）----
	// 飞德竿=竿型 10，Picker=竿型 9；面团=饵类 13；红虫=bloodworm 前缀；
	// 飞德钓组（6502）取 {12,13,14,15,16}（保守目标值 1，见 targets.ambiguous）。
	// 阶段 1
	{ID: 6001, Event: qevFishLanded, Counted: qCountSpecies(qRodIn(10))},
	{ID: 6002, Event: qevFishLanded, Counted: qCountSingle(qRodIn(10), qWeightAtLeast(1500))},
	{ID: 6003, Event: qevFishLanded, Counted: qCountSingle(qRodIn(10), qSpeciesIn("bream"))},
	// 阶段 2
	{ID: 6101, Event: qevFishLanded, Counted: qCountSingle(qRodIn(10), qWeightAtLeast(3000))},
	{ID: 6102, Event: qevFishLanded, Counted: qCountSpecies(qRodIn(9, 10))},
	{ID: 6103, Event: qevFishLanded, Counted: qCountSingle(qRodIn(10), qRarityAtLeast(1))},
	// 阶段 3
	{ID: 6201, Event: qevFishLanded, Counted: qCountSpecies(qRodIn(9, 10), qBaitClassIn(13))},
	{ID: 6202, Event: qevFishLanded, Counted: qCountSingle(qRodIn(9), qBaitPrefix("bloodworm"), qWeightAtLeast(2000))},
	{ID: 6203, Event: qevFishLanded, Counted: qCountSingle(qRodIn(9, 10), qSpeciesIn("wa_bream"), qRarityAtLeast(1))},
	// 阶段 4
	{ID: 6301, Event: qevFishLanded, Counted: qCountSingle(qRodIn(9, 10), qSpeciesIn("eel"), qWeightAtLeast(2000))},
	{ID: 6302, Event: qevFishLanded, Counted: qCountSingle(qRodIn(9, 10), qSpeciesIn("ruffe"), qRarityAtLeast(1))},
	{ID: 6303, Event: qevFishLanded, Counted: qCountSingle(qRodIn(9), qSpeciesIn("ruffe_n"), qRarityAtLeast(1))},
	// 阶段 5
	{ID: 6401, Event: qevFishLanded, Counted: qCountSpecies(qRodIn(9, 10))},
	{ID: 6402, Event: qevFishLanded, Counted: qCountSingle(qRodIn(9), qXPAtLeast(15000))},
	{ID: 6403, Event: qevFishLanded, Counted: qCountSingle(qRodIn(9, 10), qSpeciesIn("b_bream"), qRarityAtLeast(1))},
	// 阶段 6
	{ID: 6501, Event: qevFishLanded, Counted: qCountSingle(qRodIn(9), qWeightAtLeast(30000))},
	{ID: 6502, Event: qevFishLanded, Counted: qCountRig(qRigIn(12, 13, 14, 15, 16), qRarityAtLeast(1))},
	{ID: 6503, Event: qevFishLanded, Counted: qCountSingle(qRodIn(9, 10), qSpeciesIn("sterlet"), qRarityAtLeast(1))},
	// 阶段 7
	{ID: 6601, Event: qevFishLanded, Counted: qCountSpecies(qRodIn(9, 10), qRarityAtLeast(1))},
	{ID: 6602, Event: qevFishLanded, Counted: qCountSpecies(qRodIn(9, 10))},
	{ID: 6603, Event: qevFishLanded, Counted: qCountSingle(qRodIn(9, 10), qSpeciesIn("kutum", "vyrezub"), qRarityAtLeast(1))},

	// ---- 分支 7 鲤鱼捕钓（阶段 1-7）----
	// 发丝钓组取底部发丝族 {24,25,26,27,28,29,32,34} 与浮钓发丝 {30,31}。
	// 颗粒成饵=饵类 9，颗粒拟饵=饵类 17；尺寸取 hkgndoipdlc（毫米），味型 acclagajcdn。
	// 阶段 1
	{ID: 7001, Event: qevFishLanded, Counted: qCountSpecies(qRigIn(24, 25, 26, 27, 28, 29, 30, 31, 32, 34))},
	{ID: 7002, Event: qevFishLanded, Counted: qCountBaitFlavor(qBaitClassIn(9, 17))},
	{ID: 7003, Event: qevFishLanded, Counted: qCountSingle(qRigIn(24, 25, 26, 27, 28, 29, 30, 31, 32, 34), qWeightAtLeast(10000))},
	// 阶段 2
	{ID: 7101, Event: qevFishLanded, Counted: qCountSpecies(qRigIn(24, 25, 26, 27, 28, 29, 30, 31, 32, 34))},
	{ID: 7102, Event: qevFishLanded, Counted: qCountSingle(qBaitClassIn(9, 17), qBaitSizeAtLeast(25), qWeightAtLeast(20000))},
	{ID: 7103, Event: qevFishLanded, Counted: qCountSingle(qRigIn(24, 25, 26, 27, 28, 29, 30, 31, 32, 34), qSpeciesIn("c_barbel"), qRarityAtLeast(1))},
	// 阶段 3
	{ID: 7201, Event: qevFishLanded, Counted: qCountSingle(qBaitClassIn(9, 17), qBaitSizeAtMost(10), qRarityAtLeast(1))},
	// 7202 人造玉米=zbs 前缀（待实机核对）。
	{ID: 7202, Event: qevFishLanded, Counted: qCountSingle(qBaitPrefix("zbs"), qWeightAtLeast(20000))},
	{ID: 7203, Event: qevFishLanded, Counted: qCountSingle(qRigIn(24, 25, 26, 27, 28, 29, 30, 31, 32, 34), qSpeciesIn("carp", "m_carp"), qRarityAtLeast(1))},
	// 阶段 4
	{ID: 7301, Event: qevFishLanded, Counted: qCountSingle(qRigIn(29), qRarityAtLeast(1))},
	{ID: 7302, Event: qevFishLanded, Counted: qCountSingle(qRigIn(24, 25, 26, 27, 28, 29, 30, 31, 32, 34), qXPAtLeast(40000))},
	{ID: 7303, Event: qevFishLanded, Counted: qCountSingle(qRigIn(24, 25, 26, 27, 28, 29, 30, 31, 32, 34), qSpeciesIn("g_carp", "b_carp"), qRarityAtLeast(1))},
	// 阶段 5
	{ID: 7401, Event: qevFishLanded, Counted: qCountSpecies(qSpeciesIn(ghostCarpKeys...))},
	{ID: 7402, Event: qevFishLanded, Counted: qCountSingle(qRigIn(24, 25, 26, 27, 28, 29, 30, 31, 32, 34), qSpeciesIn(albinoCarpKeys...), qWeightAtLeast(15000))},
	{ID: 7403, Event: qevFishLanded, Counted: qCountSingle(qRigIn(24, 25, 26, 27, 28, 29, 30, 31, 32, 34), qSpeciesIn("s_carp"), qRarityAtLeast(1))},
	// 阶段 6
	{ID: 7501, Event: qevFishLanded, Counted: qCountSingle(qSpeciesIn(ghostCarpKeys...), qRarityAtLeast(1))},
	{ID: 7502, Event: qevFishLanded, Counted: qCountRig(qRigIn(24, 25, 26, 27, 28, 29, 32, 34), qRarityAtLeast(1))},
	{ID: 7503, Event: qevFishLanded, Counted: qCountSingle(qRigIn(24, 25, 26, 27, 28, 29, 30, 31, 32, 34), qSpeciesIn("common_carp"), qRarityAtLeast(1))},
	// 阶段 7
	{ID: 7601, Event: qevFishLanded, Counted: qCountSpecies(qRigIn(24, 25, 26, 27, 28, 29, 30, 31, 32, 34), qRarityAtLeast(1))},
	{ID: 7602, Event: qevFishLanded, Counted: qCountSingle(qRigIn(24, 25, 26, 27, 28, 29, 30, 31, 32, 34), qSpeciesIn("common_carp"), qRarityAtLeast(2))},
	{ID: 7603, Event: qevFishLanded, Counted: qCountSpecies(qSpeciesIn("n_carp", "r_carp", "l_carp", "m_carp_red", "carp_red"))},

	// ---- 分支 8 海洋钓鱼（阶段 1-7）----
	// 柳叶亮片=rig 50，海洋汲钩=51，海洋底部=55，带响珠=56，链斗式=57，
	// 天平=58，底部死鱼=59。「罕见或极其罕见」按稀有度 >=2。
	// 海钓深度取 17/12 设置的钓深（咬钩快照），未设置时深度任务不计数。
	// 阶段 1
	{ID: 8001, Event: qevFishLanded, Counted: qCountSpecies()},
	{ID: 8002, Event: qevFishLanded, Counted: qCountSingle(qWeightAtLeast(7000))},
	{ID: 8003, Event: qevFishLanded, Counted: qCountBaitItem()},
	// 阶段 2
	{ID: 8101, Event: qevFishLanded, Counted: qCountSingle(qRigIn(50), qWeightAtLeast(15000))},
	{ID: 8102, Event: qevFishLanded, Counted: qCountSpecies(qRigIn(50))},
	{ID: 8103, Event: qevFishLanded, Counted: qCountSingle(qRarityAtLeast(1))},
	// 阶段 3
	{ID: 8201, Event: qevFishLanded, Counted: qCountSingle(qWeightAtLeast(35000))},
	{ID: 8202, Event: qevFishLanded, Counted: qCountSpecies(qRigIn(51))},
	{ID: 8203, Event: qevFishLanded, Counted: qCountSingle(qWeightAtMost(5000), qRarityAtLeast(1))},
	// 阶段 4
	{ID: 8301, Event: qevFishLanded, Counted: qCountSingle(qWeightAtLeast(50000))},
	{ID: 8302, Event: qevFishLanded, Counted: qCountSingle(qRigIn(58), qRarityAtLeast(1))},
	{ID: 8303, Event: qevFishLanded, Counted: qCountSpecies(qRigIn(55, 56))},
	// 阶段 5
	{ID: 8401, Event: qevFishLanded, Counted: qCountSpecies(qDepthAtLeast(12000))},
	{ID: 8402, Event: qevFishLanded, Counted: qCountSingle(qSpeciesIn("m_zubatka", "d_zubatka"), qRarityAtLeast(1))},
	{ID: 8403, Event: qevFishLanded, Counted: qCountSingle(qRigIn(57), qSpeciesIn("red_fish"), qRarityAtLeast(1))},
	// 阶段 6
	{ID: 8501, Event: qevFishLanded, Counted: qCountSingle(qSpeciesIn("atl_cod"), qRarityAtLeast(1))},
	{ID: 8502, Event: qevFishLanded, Counted: qCountSpecies(qRigIn(59), qRarityAtLeast(1))},
	{ID: 8503, Event: qevFishLanded, Counted: qCountSingle(qDepthAtLeast(12000), qRarityAtLeast(1))},
	// 阶段 7
	{ID: 8601, Event: qevFishLanded, Counted: qCountSingle(qSpeciesIn("halibut_w"), qRarityAtLeast(1))},
	{ID: 8602, Event: qevFishLanded, Counted: qCountSingle(qSpeciesIn("m_molva", "molva"), qRarityAtLeast(1))},
	{ID: 8603, Event: qevFishLanded, Counted: qCountSingle(qRarityAtLeast(2))},
}

// ---- 未实装：限时活动分支 9..20（273 条，留给后续实现）----
//
// 上表只覆盖永久资格分支 1..8（168 条 / 文案 171 条，缺 3 条）。活动分支 9..20
// 目前一条判定都没有，所以面板里这些任务进度恒为 0、永远无法完成。链路本身已
// 全部打通（活动节点 event_switches.go、阶段 5012/5013 与倒计时、5007 下发），
// 差的只是条件判断。
//
// 全部 444 条任务文案（分支 1..20）已从客户端挖出，见
// assets/qualification_texts_zh.json 的 tasks 字段：键=RuleID，值=简中描述。
// 条件一律以文案为准反推（分支只是分组，条件全在文字里）。id 规则同
// qualificationRuleID()：分支*1000+(阶段-1)*100+任务序号。
//
// 各分支任务数：9/10/11/12/13/15/16/17 各 21，14、18 各 30，19 为 22，20 为 23。
//
// 实装方式与上表完全一致，每加一条只加一行：
//
//	{ID: 19001, Event: qevFishLanded, Counted: qCountSingle(qLocLevelIn("level_..."), ...)}
//
// 需要同步改的地方：
//   - qualificationRodTypesFor()：9..20 走 default 返回 nil（任意竿型）。文案里
//     写了具体竿型的（如 20103「手竿」、20104「中轻型/轻型/超轻型路亚竿」）要把
//     分支/任务号补进 switch，否则 5007.Idx5 下发与判定都会放宽。
//   - qualificationTargets.json：只有 1..8，未收录编号回退 1，所以活动任务分母
//     恒显示 1；要显示真实分母（3 种鱼、10 种鱼…）得把编号补进该表。
//
// 前置：19/20 有两条条件现有事件字段判不了，需要先扩 qualificationEvent 与 14/4
// 装配（其余条件——水域、竿型/硬度、饵与饵类、重量、稀有度、去重计数、岸钓、
// 夜间、钓深、抛投距离、竿测试上限——现有字段已够）：
//   - 鱼线强度：19 的 5 公斤线、20 的 3 公斤线 → 加 LineStrengthKG，取主线/前导
//     线 item_def 的强度字段，取法同 qualificationLineMaterials()。
//   - 渔线轮尺寸：20 的「大小不超过 3000 的渔线轮」→ 加 ReelSize，取轮 item_def。

// qualificationCountedGUID 把去重键映射成一个稳定 GUID。同键同 GUID（去重）、
// 异键异 GUID；该 GUID 同时作为 5003 鱼记录列表里对应记录的键。
func qualificationCountedGUID(key string) string {
	sum := md5.Sum([]byte(key))
	return hex.EncodeToString(sum[:])
}

// evaluateQualifications 是测试与独立调用入口：在自己的事务里判定并写入。
// 14/4 结算走 evaluateQualificationsTx，与经验/回执同一事务提交。
func (s *Server) evaluateQualifications(ctx *Ctx, ev qualificationEvent) map[int64]store.QualificationState {
	changed := make(map[int64]store.QualificationState)
	if ctx == nil || ctx.Sess == nil || s.DB == nil {
		return changed
	}
	uid := ctx.Sess.Userid
	tx, err := beginEconomyTx(s.dbCtx(), s.DB)
	if err != nil {
		ctx.Log.Error("资格进度事务开启失败", "uid", uid, "err", err)
		return changed
	}
	defer tx.Rollback()
	out, err := evaluateQualificationsTx(s.dbCtx(), tx, uid, ev, float64(time.Now().Unix()))
	if err != nil {
		ctx.Log.Error("资格进度判定失败", "uid", uid, "err", err)
		return changed
	}
	if err := tx.Commit(); err != nil {
		ctx.Log.Error("资格进度提交失败", "uid", uid, "err", err)
		return changed
	}
	return out
}

// evaluateQualificationsTx 在调用方事务内判定并写入全部变化。
// 任一条写失败都返回错误，由调用方回滚整笔结算，重放时再补记；
// 每尾鱼的去重键保证重复判定不会重复计数。
func evaluateQualificationsTx(ctx context.Context, tx *sql.Tx, uid int64, ev qualificationEvent, now float64) (map[int64]store.QualificationState, error) {
	changed := make(map[int64]store.QualificationState)
	states, err := store.QualificationStatesTx(ctx, tx, uid)
	if err != nil {
		return nil, err
	}
	for _, r := range qualificationRules {
		if r.Event != ev.Kind {
			continue
		}
		if !qualificationRodAllowed(qualificationRodTypesFor(r.ID), ev.RodType) {
			continue // 竿型不符（事件未带竿型时不拦）
		}
		st := states[r.ID]
		if st.CompletedTS > 0 {
			continue // 已达标封顶，不再计数
		}
		key := r.Counted(ev)
		if key == "" {
			continue
		}
		guid := qualificationCountedGUID(key)
		if qualificationHasGUID(st.Counted, guid) {
			continue
		}
		st.RuleID = r.ID
		st.Counted = append(st.Counted, store.QualificationFish{
			GUID: guid, Species: ev.Species, NameZh: ev.NameZh,
			WeightG: ev.WeightG, LengthM: ev.LengthM, Grade: ev.Grade,
			RigType: ev.RigType, RodType: ev.RodType,
		})
		if uint32(len(st.Counted)) >= qualificationTarget(uint64(r.ID)) {
			st.CompletedTS = now
		}
		if err := store.UpsertQualificationStateTx(ctx, tx, uid, st); err != nil {
			return nil, fmt.Errorf("资格进度写入失败 id=%d: %w", r.ID, err)
		}
		changed[r.ID] = st
	}
	return changed, nil
}

// worldIsNight 按世界时钟判断夜间（19:00–06:00）。资格事件在咬钩时快照，
// 不能等 14/4 结算时再取当前时间，否则跨昼夜搏鱼会判错。
func worldIsNight(unix int64) bool {
	_, hour := worldDayAndHour(unix)
	return hour >= 19 || hour < 6
}

// qualificationEventForCatch 用咬钩快照装配 14/4 上鱼事件。
// 饵属性、抛投距离、夜间标记和竿型都取本尾鱼咬钩时的值；竿/线从原竿
// （f.GearGUID）读取，避免玩家中途换竿后把新竿条件算到旧鱼上。
func (s *Server) qualificationEventForCatch(ctx context.Context, f *RolledFish, rigType int64) qualificationEvent {
	ev := qualificationEvent{Kind: qevFishLanded}
	if f == nil {
		return ev
	}
	if rt := s.rigTypeForWrite(ctx, f.GearGUID); rt > 0 {
		rigType = rt
	}
	rodType, rodPower, rodTestMax := s.qualificationRod(ctx, f.GearGUID)
	mainLine, leaderMat := s.qualificationLineMaterials(ctx, f.GearGUID)
	rarity := int64(0)
	if s.DB != nil {
		rarity = s.DB.SpeciesRarity(ctx, f.Species)
	}
	bait := f.QualBait
	ev.Species, ev.NameZh = f.Species, f.Name
	ev.WeightG, ev.LengthM = f.WeightG, f.LengthM
	ev.RigType, ev.RodType, ev.RodPower, ev.RodTestMax = rigType, rodType, rodPower, rodTestMax
	ev.Grade, ev.CatchGUID, ev.Rarity = f.Grade, f.FishGUID, rarity
	ev.Bait, ev.BaitCat = bait.ID, bait.Cat
	ev.BaitClass, ev.BaitFlavor = bait.Class, bait.Flavor
	ev.LureLenCM, ev.BaitSizeMM = bait.LenCM, bait.SizeMM
	ev.DepthCM, ev.IsShore = f.DepthCM, !f.OnBoat
	ev.IsNight, ev.Drift = f.IsNight, f.Drift
	ev.LocLevel, ev.XP = f.LocLevel, f.XP
	ev.MainLine, ev.LeaderMat = mainLine, leaderMat
	ev.CastM = f.CastM
	return ev
}

func qualificationHasGUID(fish []store.QualificationFish, guid string) bool {
	for _, f := range fish {
		if f.GUID == guid {
			return true
		}
	}
	return false
}

// qualificationBait 是从咬钩挂载快照解析出的饵信息。
type qualificationBait struct {
	ID     string
	Cat    int64
	Class  int64
	Flavor int64
	LenCM  float64
	SizeMM float64
}

// lureAttrLengthCM / baitAttrSizeMM / baitAttrFlavor 是饵定义属性键。
// 拟饵长度与天然饵尺寸都是 F32 位型，味型是整型。
const (
	lureAttrLengthCM = "bbhhojphpfa"
	baitAttrSizeMM   = "hkgndoipdlc"
	baitAttrFlavor   = "acclagajcdn"
)

// qualificationMountedBait 从咬钩时的挂载快照解析饵信息。
// 拟饵（17）优先，天然饵（27）兜底；解析不到时返回零值。
func (s *Server) qualificationMountedBait(ctx context.Context, baitGUIDs string) qualificationBait {
	var natural qualificationBait
	for _, guid := range strings.Fields(baitGUIDs) {
		row, err := s.DB.GetItemByGUID(ctx, guid)
		if err != nil || row == nil {
			continue
		}
		def, derr := s.DB.GetItemDef(ctx, row.NumericID)
		if derr != nil || def == nil {
			continue
		}
		switch def.Category {
		case 17:
			return qualificationBait{
				ID: def.ItemID, Cat: 17,
				LenCM: itemDefF32(def, lureAttrLengthCM),
			}
		case 27:
			if natural.ID == "" {
				natural = qualificationBait{
					ID: def.ItemID, Cat: 27,
					Class:  itemDefInt(def, "fopbhlmkcpe"),
					Flavor: itemDefInt(def, baitAttrFlavor),
					SizeMM: itemDefF32(def, baitAttrSizeMM),
				}
			}
		}
	}
	return natural
}

// qualificationRod 返回本竿的竿型、硬度档尾缀与测试上限，供竿型/轻型判定。
func (s *Server) qualificationRod(ctx context.Context, rodGUID string) (int64, string, float64) {
	if rodGUID == "" {
		return 0, "", 0
	}
	row, err := s.DB.GetItemByGUID(ctx, rodGUID)
	if err != nil || row == nil {
		return 0, "", 0
	}
	def, derr := s.DB.GetItemDef(ctx, row.NumericID)
	if derr != nil || def == nil {
		return 0, rodPowerClass(row.ItemID), 0
	}
	_, testMax := rodTestG(def)
	return rodDefRodType(def), rodPowerClass(def.ItemID), testMax
}

// qualificationLineMaterials 返回竿上主线与前导线的材质档。
// 主线(cat11)读 fopbhlmkcpe，前导线(cat15)读 ocmbfkebgpp：1=尼龙 2=碳线。
func (s *Server) qualificationLineMaterials(ctx context.Context, rodGUID string) (main, leader int64) {
	if rodGUID == "" {
		return 0, 0
	}
	comps := s.storedRigComponents(ctx, rodGUID)
	materialOf := func(category int64, key string) int64 {
		for _, k := range rigCompKeysOfCategory(comps, category) {
			row, err := s.DB.GetItemByGUID(ctx, comps[k].GUID)
			if err != nil || row == nil {
				continue
			}
			def, derr := s.DB.GetItemDef(ctx, row.NumericID)
			if derr != nil || def == nil {
				continue
			}
			return itemDefInt(def, key)
		}
		return 0
	}
	return materialOf(11, "fopbhlmkcpe"), materialOf(15, "ocmbfkebgpp")
}
