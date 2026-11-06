package gameserver

// 窝料吸引系统 v1.1（设计稿 docs/groundbait-design.md）。
//
// 一句话：往一个 5 米格里投窝料，这份料按「1 份 = 0.6 游戏小时」活着，
// 期间按鱼种食性把对口鱼的候选权重抬起来（最多 x2.60），并且能稍微顶掉
// 坏天气/低温的惩罚；投超过 15 份开始压开口率，22 份以上这个格子基本不开口。
// 掠食性鱼种一律 x1.00，不蹭窝。
//
// 判定用的食性亲和（appetite/专食性/掠食冻结）由离线探针从配方表 + 生态表 +
// 攻略吃饵表算好后随包发布（assets/groundbait_*.json），运行时只做习性门与
// 天气软化，避免两套实现漂移。

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

//go:embed assets/groundbait_item_def.json
var groundbaitItemDefJSON []byte

//go:embed assets/groundbait_affinity.json
var groundbaitAffinityJSON []byte

const (
	// groundbaitCellSizeM 窝点格子边长（米）。落点取 5 米桶，相邻两窝互不影响。
	groundbaitCellSizeM = 5.0
	// groundbaitRealSecPerGameHour 1 游戏小时 = 150 真实秒。两条独立证据：
	// spawn.go 的 realSecPerGameHour=150，world_clock.go 的 24 游戏小时/真实小时。
	groundbaitRealSecPerGameHour = 150.0
	// groundbaitMaxTickRealSec 单次结算最多推进的真实秒数。判鱼/投放都会碰到
	// 游戏钟，正常间隔只有几秒；跨度超过这里说明是登录或掉线后的第一次结算，
	// 这段离线时间不该扣窝时。
	groundbaitMaxTickRealSec = 30.0
	// groundbaitSatExponent 契合饱和曲线的指数（设计稿第 4 节）。
	groundbaitSatExponent = 1.35
)

type groundbaitItemDef struct {
	ItemID     string             `json:"item_id"`
	Diet       map[string]float64 `json:"diet"`
	Potency    float64            `json:"potency"`
	Dispersion float64            `json:"dispersion"`
	Particle   float64            `json:"particle"`
	Scent      float64            `json:"scent"`
	Hold       float64            `json:"hold"`
	TargetFish string             `json:"target_fish_key"`
	Portions   float64            `json:"portions"`
}

type groundbaitAffinityRow struct {
	ItemID   string  `json:"item_id"`
	FishKey  string  `json:"fish_key"`
	Appetite float64 `json:"appetite"`
	Affinity float64 `json:"affinity"`
	Special  float64 `json:"special"`
}

// groundbaitDefs 是离线探针产出的窝料档案与逐鱼种亲和表。
type groundbaitDefs struct {
	version int64
	source  string
	items   map[string]groundbaitItemDef
	// affinity: 物品 -> 鱼种 -> 亲和行。掠食者已被冻结，压根不在表里。
	affinity map[string]map[string]groundbaitAffinityRow
	badErr   string
}

var (
	groundbaitDefsOnce   sync.Once
	groundbaitDefsShared *groundbaitDefs
)

// groundbaitDefsLoad 解析包内资产。解析失败时返回空表（判定退化成「没有窝」），
// 不能因为资产坏了就把判鱼一起带崩。
func groundbaitDefsLoad() *groundbaitDefs {
	groundbaitDefsOnce.Do(func() {
		d := &groundbaitDefs{items: map[string]groundbaitItemDef{}, affinity: map[string]map[string]groundbaitAffinityRow{}}
		var itemsDoc struct {
			ProfileVersion int64               `json:"profile_version"`
			Source         string              `json:"source"`
			Items          []groundbaitItemDef `json:"items"`
		}
		if err := json.Unmarshal(groundbaitItemDefJSON, &itemsDoc); err != nil {
			d.badErr = "item_def: " + err.Error()
		}
		var affDoc struct {
			Rows []groundbaitAffinityRow `json:"rows"`
		}
		if err := json.Unmarshal(groundbaitAffinityJSON, &affDoc); err != nil {
			d.badErr = "affinity: " + err.Error()
		}
		for _, it := range itemsDoc.Items {
			if it.ItemID != "" {
				d.items[it.ItemID] = it
			}
		}
		for _, row := range affDoc.Rows {
			if row.ItemID == "" || row.FishKey == "" {
				continue
			}
			byFish, ok := d.affinity[row.ItemID]
			if !ok {
				byFish = map[string]groundbaitAffinityRow{}
				d.affinity[row.ItemID] = byFish
			}
			byFish[row.FishKey] = row
		}
		d.version, d.source = itemsDoc.ProfileVersion, itemsDoc.Source
		groundbaitDefsShared = d
	})
	return groundbaitDefsShared
}

// itemDef 取窝料档案；未知物品返回 ok=false，由调用方按中性档案处理。
func (d *groundbaitDefs) itemDef(itemID string) (groundbaitItemDef, bool) {
	it, ok := d.items[itemID]
	return it, ok
}

// affinityOf 取某款窝料对某鱼种的亲和行。掠食者不在表里，返回 false＝x1.00。
func (d *groundbaitDefs) affinityOf(itemID, fishKey string) (groundbaitAffinityRow, bool) {
	byFish, ok := d.affinity[itemID]
	if !ok {
		return groundbaitAffinityRow{}, false
	}
	row, ok := byFish[fishKey]
	return row, ok
}

func groundbaitClamp(x, lo, hi float64) float64 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

// groundbaitEnabled 总开关。关掉之后判鱼流程与加这套系统之前完全一致。
func (s *Server) groundbaitEnabled() bool {
	return s.DB != nil && s.tunableF("groundbait_enabled", 0) == 1
}

// groundbaitAffinityValue 把食性契合度换算成饱和后的加成系数 A∈[0,1]。
// 低于饱和下限（默认 appetite 1.05）完全不加成，避免「一撒窝全员受益」。
func (s *Server) groundbaitAffinityValue(appetite float64) float64 {
	floor := s.tunableF("groundbait_sat_floor", 1.05)
	ceil := s.tunableF("groundbait_sat_cap", 2.05)
	if ceil <= floor {
		ceil = floor + 1
	}
	if appetite <= floor {
		return 0
	}
	return math.Pow(groundbaitClamp((appetite-floor)/(ceil-floor), 0, 1), groundbaitSatExponent)
}

// groundbaitCell 落点转 5 米网格桶。
func groundbaitCell(x, z float64) (int64, int64) {
	return int64(math.Floor(x / groundbaitCellSizeM)), int64(math.Floor(z / groundbaitCellSizeM))
}

// groundbaitCellFor 没拿到落点坐标时退化成「整个钓点一格」，只给中性窝效。
func groundbaitCellFor(x, z float64, hasPos bool, spot uint64) (int64, int64, int) {
	if hasPos {
		bx, bz := groundbaitCell(x, z)
		return bx, bz, 1
	}
	return int64(spot), -1, 0
}

// groundbaitGameHours 返回该玩家的在线游戏钟读数（游戏小时），并顺手推进它。
// 只有服务端被请求碰到才推进，单次跨度封顶 30 秒，所以离线不扣窝时。
func (s *Server) groundbaitGameHours(ctx context.Context, uid int64) float64 {
	if s.DB == nil || uid == 0 {
		return 0
	}
	now := float64(time.Now().Unix())
	var realTS, acc float64
	err := s.DB.SQL.QueryRowContext(ctx, `SELECT real_ts, game_hours FROM groundbait_clock WHERE uid=?`, uid).Scan(&realTS, &acc)
	if errors.Is(err, sql.ErrNoRows) {
		if _, ierr := s.DB.SQL.ExecContext(ctx, `INSERT INTO groundbait_clock(uid,real_ts,game_hours,updated_ts)
			VALUES(?,?,?,?) ON CONFLICT(uid) DO UPDATE SET real_ts=excluded.real_ts, updated_ts=excluded.updated_ts`,
			uid, now, 0.0, now); ierr != nil {
			s.log.Warn("窝料游戏钟初始化失败", "err", ierr)
		}
		return 0
	}
	if err != nil {
		return acc
	}
	dt := now - realTS
	if dt <= 0 {
		return acc
	}
	if dt > groundbaitMaxTickRealSec {
		dt = groundbaitMaxTickRealSec
	}
	acc += dt / groundbaitRealSecPerGameHour
	if _, uerr := s.DB.SQL.ExecContext(ctx, `UPDATE groundbait_clock SET real_ts=?, game_hours=?, updated_ts=? WHERE uid=?`,
		now, acc, now, uid); uerr != nil {
		s.log.Warn("窝料游戏钟推进失败", "err", uerr)
	}
	return acc
}

// groundbaitDeposit 是四种投放方式的唯一入口：手抛/打窝竿/食笼 PVA/弹弓都调它，
// 通道只作为标签留账，方便以后分别调参。txnGuid 唯一约束提供幂等保护。
func (s *Server) groundbaitDeposit(ctx context.Context, uid int64, loc string, x, z float64, hasPos bool,
	spot uint64, itemID string, portions float64, channel, txnGuid string) {
	if !s.groundbaitEnabled() || uid == 0 || loc == "" || portions <= 0 || txnGuid == "" {
		return
	}
	defs := groundbaitDefsLoad()
	if defs.badErr != "" {
		return
	}
	_, known := defs.itemDef(itemID)
	// 配方表没登记的窝料（比如自制窝料实例）：照样入账，用中性档案，
	// 只是吃不到食性加成。宁可保守，不能让它凭空消失。
	if portions > 60 {
		portions = 60
	}
	per := s.tunableF("groundbait_portion_hours", 0.6)
	if per <= 0 {
		return
	}
	hours := portions * per
	// 单批窝时封顶：1 份 0.6 游戏小时，但一批最多 6 小时（少爷指示：
	// 「打 10 份饵 = 游戏里 6 小时有效」）。再多份数只抬强度 S，不再拖长时效。
	capHours := s.tunableF("groundbait_hours_cap", 6.0)
	if capHours > 0 && hours > capHours {
		hours = capHours
	}
	bx, bz, hasPosInt := groundbaitCellFor(x, z, hasPos, spot)
	acc := s.groundbaitGameHours(ctx, uid)
	tx, err := s.DB.SQL.BeginTx(ctx, nil)
	if err != nil {
		s.log.Warn("窝料投放开事务失败", "err", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO groundbait_deposit
		(uid,loc,bx,bz,has_pos,channel,item_id,portions,units_left,hours0,deposit_hour,txn_guid,created_ts)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		uid, loc, bx, bz, hasPosInt, channel, itemID, portions, portions, hours, acc, txnGuid, float64(time.Now().Unix()))
	if err != nil {
		s.log.Warn("窝料投放写账本失败", "err", err, "item", itemID, "channel", channel)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// 幂等命中：这条上报已经投过。
		return
	}
	if err := tx.Commit(); err != nil {
		s.log.Warn("窝料投放提交失败", "err", err)
		return
	}
	s.log.Info("窝料投放", "uid", uid, "loc", loc, "cell", fmt.Sprintf("%d,%d", bx, bz),
		"item", itemID, "channel", channel, "portions", portions, "hours", hours,
		"known", known, "txn", txnGuid)
}

// groundbaitSpotState 是一个窝格子的即时状态。
type groundbaitSpotState struct {
	units     float64            // N：净窝份数（已按剩余窝时等比稀释）
	hoursLeft float64            // T：剩余窝时（游戏小时）
	batches   int                // 未过期批次数
	diffuse   int                // 其中落点未知的批数（按衰减系数计入）
	hold      float64            // 按份数加权的留窝系数，用于流速门
	affinity  map[string]float64 // 鱼种 -> 按份数加权的契合度 A
	special   map[string]float64 // 鱼种 -> 按份数加权的专食性
}

// groundbaitSpot 汇总某格子当前还活着的投放批次，顺手删掉已过期的行。
func (s *Server) groundbaitSpot(ctx context.Context, uid int64, loc string, bx, bz int64) groundbaitSpotState {
	st := groundbaitSpotState{affinity: map[string]float64{}, special: map[string]float64{}}
	if s.DB == nil || uid == 0 || loc == "" {
		return st
	}
	defs := groundbaitDefsLoad()
	if defs.badErr != "" {
		return st
	}
	acc := s.groundbaitGameHours(ctx, uid)
	prior := s.tunableF("groundbait_prior", 0.35)
	// 同格投放全额生效；落点未知的投放（has_pos=0）按衰减系数计入，
	// 免得「先打窝后抛竿」的窝在玩家抛竿后凭空消失。
	fallDiffuse := s.groundbaitDiffuseFall()
	rows, err := s.DB.SQL.QueryContext(ctx, `SELECT id,item_id,portions,units_left,hours0,deposit_hour,has_pos
		FROM groundbait_deposit
		WHERE uid=? AND loc=? AND ((bx=? AND bz=?) OR has_pos=0)
		ORDER BY has_pos DESC, deposit_hour`, uid, loc, bx, bz)
	if err != nil {
		s.log.Warn("窝料账本读取失败", "err", err)
		return st
	}
	var deadRows []int64
	type live struct {
		id     int64
		itemID string
		units  float64
		remain float64
		ratio  float64
		fall   float64
	}
	var alive []live
	for rows.Next() {
		var (
			id                     int64
			itemID                 string
			portions, units, hours float64
			depositAt              float64
			hasPosRow              int
		)
		if serr := rows.Scan(&id, &itemID, &portions, &units, &hours, &depositAt, &hasPosRow); serr != nil {
			continue
		}
		remain := hours - (acc - depositAt)
		if remain <= 0 || units <= 0 {
			deadRows = append(deadRows, id)
			continue
		}
		ratio := groundbaitClamp(remain/hours, 0, 1)
		fall := 1.0
		if hasPosRow == 0 {
			fall = fallDiffuse
		}
		alive = append(alive, live{id: id, itemID: itemID, units: units * ratio * fall,
			remain: remain * fall, ratio: ratio, fall: fall})
	}
	if cerr := rows.Close(); cerr != nil {
		s.log.Warn("窝料账本游标关闭失败", "err", cerr)
	}
	for _, dead := range deadRows {
		if _, derr := s.DB.SQL.ExecContext(ctx, `DELETE FROM groundbait_deposit WHERE id=?`, dead); derr != nil {
			s.log.Warn("窝料过期批次清理失败", "err", derr)
		}
	}
	if len(alive) == 0 {
		return st
	}
	// 同一格内多批投放按剩余份数加权合并；先累加再加权，避免逐批重复算。
	affSum := map[string]float64{}
	specSum := map[string]float64{}
	holdSum := 0.0
	for _, b := range alive {
		st.units += b.units
		st.hoursLeft += b.remain
		st.batches++
		if b.fall < 1 {
			st.diffuse++
		}
		it, known := defs.itemDef(b.itemID)
		if !known {
			it.Hold = 1.0
		}
		holdSum += b.units * it.Hold
		for fish, row := range defs.affinity[b.itemID] {
			app := row.Appetite
			if it.TargetFish != "" && fish == it.TargetFish {
				app += prior // 商品包装写明目标鱼的先验
			}
			affSum[fish] += b.units * s.groundbaitAffinityValue(app)
			specSum[fish] += b.units * row.Special
		}
	}
	if st.units <= 0 {
		return groundbaitSpotState{affinity: map[string]float64{}, special: map[string]float64{}}
	}
	for fish, v := range affSum {
		st.affinity[fish] = v / st.units
		st.special[fish] = specSum[fish] / st.units
	}
	st.hold = holdSum / st.units
	return st
}

// groundbaitStrength 窝点强度 S：份数越多越强，但收益递减（默认 4 份上下趋饱）。
func (s *Server) groundbaitStrength(units float64) float64 {
	sat := s.tunableF("groundbait_sat", 4.0)
	if sat <= 0 {
		sat = 4
	}
	if units <= 0 {
		return 0
	}
	return 1 - math.Exp(-units/sat)
}

// groundbaitOverdoseP 死窝闸门：净窝份数超过阈值后线性压开口率，22 份以上归零。
func (s *Server) groundbaitOverdoseP(units float64) float64 {
	from := s.tunableF("groundbait_overdose_from", 15.0)
	step := s.tunableF("groundbait_overdose_step", 0.15)
	if units <= from || step <= 0 {
		return 1
	}
	return groundbaitClamp(1-step*(units-from), 0, 1)
}

// groundbaitDiffuseFall 落点未知投放的衰减系数。弹弓与手抛只上报距离、不上报坐标，
// 这种窝记在「整个钓点一格」里；等玩家抛竿后判鱼用的是浮标落点格子，两者对不上。
// 与其让这份窝凭空失效，不如让它按衰减系数继续生效（实测：同一钓点打窝后抛竿，
// 本格 0 批 + 落点未知 1 批，全靠这条通道生效）。缺省 0.40，调 0 即只认同格。
func (s *Server) groundbaitDiffuseFall() float64 {
	return groundbaitClamp(s.tunableF("groundbait_diffuse_fall", 0.40), 0, 1)
}

// groundbaitGates 算习性门：硬门（水层/昼夜/季节/底质/流速/稀有度）与
// 软门（水温/低温/水色）。两者都夹在 [0.05,1]，软门稍后被天气软化往 1 拉近。
func (s *Server) groundbaitGates(ctx context.Context, c fishRollContext, fishKey string, hold float64) (float64, float64) {
	p := s.cachedFishEcology(ctx, c, fishKey)
	if !p.Enabled {
		return 0.05, 0.05
	}
	water := fishWaterConditionsFor(p, c)
	_, hour := worldDayAndHour(c.now.Unix())
	layer := groundbaitClamp(s.fishLayerWeightProfile(p, c), 0.05, 1)
	diel := groundbaitClamp(fishDielWeight(p, hour), 0.05, 1)
	season := groundbaitClamp(fishSeasonWeight(p, water.Season), 0.05, 1)
	rarity := groundbaitClamp(1-0.12*float64(p.RarityTier), 0.4, 1)
	substrate := 1.0
	if c.spotEnv.Known {
		if prefs := fishEcologyJSONMap(p.SubstrateJSON); len(prefs) > 0 {
			substrate = groundbaitClamp(0.85+prefs[c.spotEnv.Substrate]*0.18, 0.7, 1)
		}
	}
	flow := 1.0
	if c.spotEnv.Known {
		if hold <= 0 {
			hold = 1
		}
		flow = 1 + (hold-1)*0.30 - math.Max(0, c.spotEnv.Current-0.25)*(1.25-hold*0.35)*0.9
	}
	hard := groundbaitClamp(layer*diel*season*rarity*substrate*flow, 0.05, 1)

	tol := math.Max(2.2, (p.TempMaxC-p.TempMinC)/2)
	z := (water.WaterTempC - p.TempPrefC) / tol
	temp := 0.55 + 0.45*math.Exp(-0.5*z*z)
	cold := 1.0
	if water.WaterTempC < 10 {
		cold = groundbaitClamp(0.55+(water.WaterTempC/10)*0.45, 0.55, 1)
	}
	clarity := groundbaitClamp(1-math.Abs(p.ClarityPref-water.Clarity)*0.16, 0.05, 1)
	soft := groundbaitClamp(temp*cold*clarity, 0.05, 1)
	return hard, soft
}

// groundbaitSpeciesMultiplier 单个鱼种的窝料乘子 M。掠食性鱼种（亲和表里
// 已被冻结）压根不进来，恒为 1.00。
func (s *Server) groundbaitSpeciesMultiplier(ctx context.Context, c fishRollContext, st groundbaitSpotState,
	fishKey string, S, P float64) float64 {
	a, ok := st.affinity[fishKey]
	if !ok || a <= 0 {
		return 1
	}
	gain := s.tunableF("groundbait_gain", 1.50)
	cap := s.tunableF("groundbait_cap", 2.60)
	specialGain := s.tunableF("groundbait_special_gain", 0.30)
	hard, soft := s.groundbaitGates(ctx, c, fishKey, st.hold)
	// 天气软化：只有对口的窝料才配把坏天气往 1 拉近，且永远不超过硬门。
	softMax := s.tunableF("groundbait_soft_max", 0.60)
	beta := s.tunableF("groundbait_soft_beta", 0.45)
	softened := soft
	if beta > 0 {
		beta = groundbaitClamp(beta*S*a, 0, softMax)
		softened = 1 + (soft-1)*(1-beta)
	}
	m := 1 + gain*S*a*(1+specialGain*st.special[fishKey])*math.Sqrt(hard*groundbaitClamp(softened, 0.05, 1))
	m = math.Min(cap, m)
	return 1 + (m-1)*P
}

// applyGroundbaitWeight 在判鱼权重定稿前插入窝料乘子。开关关掉时一行都不执行。
func (s *Server) applyGroundbaitWeight(ctx context.Context, c *fishRollContext, keys []string, wts []float64) {
	if !s.groundbaitEnabled() || c.uid == 0 || len(keys) == 0 {
		return
	}
	bx, bz, _ := groundbaitCellFor(c.x, c.z, c.hasPos, c.spot)
	st := s.groundbaitSpot(ctx, c.uid, c.loc, bx, bz)
	if st.units <= 0 {
		return
	}
	S := s.groundbaitStrength(st.units)
	P := s.groundbaitOverdoseP(st.units)
	for i, key := range keys {
		if wts[i] <= 0 {
			continue
		}
		if m := s.groundbaitSpeciesMultiplier(ctx, *c, st, key, S, P); m > 1 {
			wts[i] *= m
		}
	}
}

// groundbaitBiteGate 咬钩闸门：这个格子腻到 22 份以上就直接不开口。
// 路亚族可以豁免（拟饵不是被窝料喂出来的口），由 tunable 控制。
func (s *Server) groundbaitBiteGate(ctx context.Context, uid int64, loc string, x, z float64,
	hasPos bool, spot uint64, rigType int64) float64 {
	if !s.groundbaitEnabled() || uid == 0 || loc == "" {
		return 1
	}
	if s.tunableF("groundbait_overdose_lure_exempt", 1) == 1 && rigTypeLureFamily[rigType] {
		return 1
	}
	bx, bz, _ := groundbaitCellFor(x, z, hasPos, spot)
	st := s.groundbaitSpot(ctx, uid, loc, bx, bz)
	if st.units <= 0 {
		return 1
	}
	P := s.groundbaitOverdoseP(st.units)
	// 这条日志是「窝到底有没有生效」的现场证据：投了多少份、还剩几小时、
	// 强度多少、开口率被压到多少。只在真的投过窝的格子上打，量不会大。
	s.log.Info("窝料影响咬钩", "loc", loc, "cell", fmt.Sprintf("%d,%d", bx, bz),
		"portions", st.units, "hours_left", st.hoursLeft, "batches", st.batches,
		"strength", s.groundbaitStrength(st.units), "bite_gate", P,
		"on_spot", st.batches-st.diffuse, "no_pos", st.diffuse)
	return P
}

// groundbaitConsumeBite 鱼吃口扣份：窝被吃薄，死窝也就自己缓过来了。
func (s *Server) groundbaitConsumeBite(ctx context.Context, uid int64, loc string, x, z float64,
	hasPos bool, spot uint64, units float64) {
	if !s.groundbaitEnabled() || uid == 0 || loc == "" || units <= 0 {
		return
	}
	acc := s.groundbaitGameHours(ctx, uid)
	bx, bz, _ := groundbaitCellFor(x, z, hasPos, spot)
	rows, err := s.DB.SQL.QueryContext(ctx, `SELECT id, units_left, hours0, deposit_hour
		FROM groundbait_deposit
		WHERE uid=? AND loc=? AND ((bx=? AND bz=?) OR has_pos=0)
		ORDER BY has_pos DESC, deposit_hour`, uid, loc, bx, bz)
	if err != nil {
		return
	}
	type batch struct {
		id      int64
		units   float64
		hours   float64
		deposit float64
	}
	var list []batch
	for rows.Next() {
		var b batch
		if serr := rows.Scan(&b.id, &b.units, &b.hours, &b.deposit); serr != nil {
			continue
		}
		list = append(list, b)
	}
	_ = rows.Close()
	left := units
	for _, b := range list {
		if left <= 0 {
			break
		}
		if b.hours-(acc-b.deposit) <= 0 {
			continue
		}
		take := math.Min(left, b.units)
		left -= take
		if _, uerr := s.DB.SQL.ExecContext(ctx,
			`UPDATE groundbait_deposit SET units_left=MAX(0, units_left-?) WHERE id=?`, take, b.id); uerr != nil {
			s.log.Warn("窝料咬口扣份失败", "err", uerr)
		}
	}
}

// depositCastGroundbait 把「钓组入水时消耗掉的窝料」投进落点格子。
// 食笼/PVA 棒/打窝器都走这条：扣料与投放同一次事件，幂等键沿用饵料事件键。
func (s *Server) depositCastGroundbait(sess *Session, st *FishState, change baitChange, eventKey string) {
	if !s.groundbaitEnabled() || len(change.Groundbait) == 0 || sess == nil || st == nil {
		return
	}
	st.mu.Lock()
	loc, x, z, has, spot := st.Loc, st.CastPosX, st.CastPosZ, st.HasCastPos, st.Spot
	st.mu.Unlock()
	ctx := sess.pushCtx()
	for i, use := range change.Groundbait {
		channel := "feeder"
		if use.Category == 30 {
			channel = "pva"
		}
		s.groundbaitDeposit(ctx, sess.Userid, loc, x, z, has, spot, use.ItemID, use.Units,
			channel, fmt.Sprintf("%s#%d", eventKey, i))
	}
}
