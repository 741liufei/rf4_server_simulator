package gameserver

// 窝料「按实际用料现算食性亲和」的回归测试。
//
// 关键校验：把离线探针算好的逐鱼种亲和表搬到 Go 里重算一遍，两款算法必须对得上。
// 对不上就说明搬公式时走样了（关键词口径、归一化、双票阈值任一漂移都会立刻露馅）。

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

// groundbaitEcologyProbe 是离线探针读生态档案的口径：食性向量 + 鱼种中文名
// （攻略吃饵表按中文名查）。fishEcologyProfile 不解析 source，所以测试单独读一份。
type groundbaitEcologyProbe struct {
	FishKey  string `json:"fish_key"`
	DietJSON string `json:"diet_json"`
	Source   struct {
		Name string `json:"name"`
	} `json:"source"`
}

func groundbaitProbeEcology(t *testing.T) map[string]groundbaitEcologyProbe {
	t.Helper()
	var rows []groundbaitEcologyProbe
	if err := json.Unmarshal(fishEcologyDefsJSON, &rows); err != nil {
		t.Fatalf("生态档案解析失败: %v", err)
	}
	out := map[string]groundbaitEcologyProbe{}
	for _, row := range rows {
		out[row.FishKey] = row
	}
	if len(out) < 200 {
		t.Fatalf("生态档案应当覆盖全部鱼种，实得 %d 条", len(out))
	}
	return out
}

// groundbaitProbeAffinity 读离线探针导出的逐鱼种亲和表（item -> fish -> 行）。
func groundbaitProbeAffinity(t *testing.T) map[string]map[string]groundbaitAffinityRow {
	t.Helper()
	var doc struct {
		Rows []groundbaitAffinityRow `json:"rows"`
	}
	if err := json.Unmarshal(groundbaitAffinityJSON, &doc); err != nil {
		t.Fatalf("亲和表解析失败: %v", err)
	}
	out := map[string]map[string]groundbaitAffinityRow{}
	for _, row := range doc.Rows {
		if out[row.ItemID] == nil {
			out[row.ItemID] = map[string]groundbaitAffinityRow{}
		}
		out[row.ItemID][row.FishKey] = row
	}
	return out
}

// groundbaitProbeFish 按探针口径给出某条鱼的生态食性与攻略可接受食性族。
func groundbaitProbeFish(ecology map[string]groundbaitEcologyProbe, fishKey string) (map[string]float64, bool, map[string]bool) {
	row, ok := ecology[fishKey]
	if !ok {
		return nil, false, nil
	}
	rule, hasRule := fishBaitRules[ruleName(strings.TrimSpace(row.Source.Name))]
	accepted := map[string]bool{}
	if hasRule {
		accepted = groundbaitAcceptedFamilies(rule)
	}
	return fishEcologyJSONMap(row.DietJSON), hasRule, accepted
}

// 逐鱼种契合度搬到 Go 之后必须复现离线探针：252 条鱼 x 14 款窝料全跑一遍。
func TestGroundbaitRuntimeAffinityMatchesOfflineProbe(t *testing.T) {
	defs := groundbaitDefsLoad()
	if defs.badErr != "" {
		t.Fatalf("窝料资产解析失败: %s", defs.badErr)
	}
	ecology := groundbaitProbeEcology(t)
	table := groundbaitProbeAffinity(t)
	const appetiteTolerance = 0.005
	const specialTolerance = 0.02
	checked, frozen, missing, extra := 0, 0, 0, 0
	var drift []string

	for itemID, item := range defs.items {
		prof, ok := defs.profileOf(item.Materials)
		if !ok {
			t.Fatalf("%s 的成分算不出食性向量（用料 %v）", itemID, item.Materials)
		}
		byFish := table[itemID]
		if len(prof.Diet) == 0 {
			// PVA 棒默认只有水溶网，没有固定饵料；吸引由制作实例里的实际填充
			// 决定，所以它不该在固定亲和表里占任何鱼种。
			if len(byFish) != 0 {
				t.Errorf("%s 是纯载体，固定亲和表不该有 %d 行", itemID, len(byFish))
			}
			continue
		}
		if len(byFish) == 0 {
			t.Fatalf("亲和表里缺少 %s", itemID)
		}
		seen := map[string]bool{}
		for fishKey := range ecology {
			fish, hasRule, accepted := groundbaitProbeFish(ecology, fishKey)
			if groundbaitPredatorFrozen(fish, hasRule, accepted, 1.5, 0.8, 0.8) {
				if _, ok := byFish[fishKey]; ok {
					t.Errorf("%s x %s：运行时判成掠食冻结，可预烘亲和表里有这一行", itemID, fishKey)
				}
				frozen++
				continue
			}
			app, spec := groundbaitAppetite(prof.Diet, fish, accepted, 0.08)
			row, ok := byFish[fishKey]
			if !ok {
				// 探针只落「没被冻结且契合度超过饱和下限」的行；其余行本来就不该出现。
				if app > 1.05 {
					t.Errorf("%s x %s：运行时算出契合度 %.3f，预烘亲和表却没有这一行", itemID, fishKey, app)
					extra++
				} else {
					missing++
				}
				continue
			}
			seen[fishKey] = true
			checked++
			if math.Abs(app-row.Appetite) > appetiteTolerance {
				drift = append(drift, itemID+" x "+fishKey)
				t.Errorf("%s x %s：契合度 %.4f，离线探针 %.4f", itemID, fishKey, app, row.Appetite)
			}
			if math.Abs(spec-row.Special) > specialTolerance {
				t.Errorf("%s x %s：专食性 %.3f，离线探针 %.3f", itemID, fishKey, spec, row.Special)
			}
		}
		for fishKey := range byFish {
			if !seen[fishKey] {
				t.Errorf("%s x %s：预烘亲和表里有这一行，运行时算不出来", itemID, fishKey)
			}
		}
	}
	if len(drift) > 0 {
		t.Fatalf("离线表与运行时算法对不上 %d 行，例如 %s", len(drift), strings.Join(drift[:min(5, len(drift))], ", "))
	}
	t.Logf("复算 %d 行亲和（掠食冻结 %d 行、无加成 %d 行），契合度与专食性全部对上", checked, frozen, missing)
}

// 自制窝料才是这次改造的意义：两团属性不同的料必须吸引到不同的鱼。
// 冬季动物配（碎麦米+红虫+蛆蛹+黏土+鱼肝油）与夏季植物配（饼干屑+玉米+颗粒+蜂蜜）。
func TestGroundbaitHomeMadeRecipesDivergeBySpecies(t *testing.T) {
	defs := groundbaitDefsLoad()
	ecology := groundbaitProbeEcology(t)
	animal := []string{"mix_semolina", "feedadds_bloodworms", "feedadds_casters", "feedadds_clay", "aroma_fishoil"}
	plant := []string{"mix_groundcrackers", "feedadds_corn", "feedadds_pelets", "aroma_honey"}

	aProf, ok := defs.profileOf(animal)
	if !ok {
		t.Fatal("冬季动物配算不出食性向量")
	}
	pProf, ok := defs.profileOf(plant)
	if !ok {
		t.Fatal("夏季植物配算不出食性向量")
	}
	if math.Abs(aProf.Diet["worm"]-pProf.Diet["worm"]) < 0.2 {
		t.Fatalf("两团料的动物性占比应当明显不同：动物配 %.3f vs 植物配 %.3f",
			aProf.Diet["worm"], pProf.Diet["worm"])
	}

	// 同一条鱼：植物口强的鱼吃夏季植物配更香，动物口执着的鱼吃冬季动物配更香。
	type probe struct{ key, name string }
	for _, want := range []struct {
		fish     probe
		better   string
		minDelta float64
	}{
		{probe{"crucian", "银鲫"}, "plant", 0.2},
		{probe{"bream", "欧鳊"}, "plant", 0.15},
		{probe{"dace", "雅罗鱼"}, "animal", 0.2},
		{probe{"syb_gudgeon", "犬首鮈"}, "animal", 0.3},
	} {
		fish, hasRule, accepted := groundbaitProbeFish(ecology, want.fish.key)
		if len(fish) == 0 || !hasRule {
			t.Fatalf("鱼种 %s 缺少生态档案或攻略吃饵记录，换一条再测", want.fish.key)
		}
		if groundbaitPredatorFrozen(fish, hasRule, accepted, 1.5, 0.8, 0.8) {
			t.Fatalf("%s 被判成掠食冻结，这条不该是商品窝料的目标鱼", want.fish.name)
		}
		appA, _ := groundbaitAppetite(aProf.Diet, fish, accepted, 0.08)
		appP, _ := groundbaitAppetite(pProf.Diet, fish, accepted, 0.08)
		delta := appP - appA
		if want.better == "animal" {
			delta = -delta
		}
		if delta < want.minDelta {
			t.Errorf("%s：动物配 %.3f / 植物配 %.3f，应当更吃%s那一团（差距至少 %.2f）",
				want.fish.name, appA, appP, want.better, want.minDelta)
		}
	}
}

// PVA 水溶网是载体，不是植物饵：纯网不成窝，棒里填什么才决定吸引哪种鱼。
func TestGroundbaitPvaCarrierUsesFilling(t *testing.T) {
	defs := groundbaitDefsLoad()
	pure, ok := defs.profileOf([]string{"hvz_pva_material"})
	if !ok || len(pure.Diet) != 0 {
		t.Fatalf("纯 PVA 载体应是已知空饵，实得 ok=%v diet=%v", ok, pure.Diet)
	}

	plantFill := []string{"feed_dry", "hvz_pva_material"}
	animalFill := []string{"mix_semolina", "feedadds_bloodworms", "feedadds_casters", "hvz_pva_material"}
	plantProf, ok := defs.profileOf(plantFill)
	if !ok {
		t.Fatal("PVA 植物填充配算不出档案")
	}
	animalProf, ok := defs.profileOf(animalFill)
	if !ok {
		t.Fatal("PVA 动物填充配算不出档案")
	}
	dryProf, ok := defs.profileOf([]string{"feed_dry"})
	if !ok {
		t.Fatal("干混合档案算不出来")
	}
	for fam, want := range dryProf.Diet {
		if math.Abs(plantProf.Diet[fam]-want) > 1e-9 {
			t.Fatalf("PVA 网不该稀释填充料：%s 实得 %.4f，期望 %.4f", fam, plantProf.Diet[fam], want)
		}
	}
	if animalProf.Diet["worm"] <= plantProf.Diet["worm"] {
		t.Fatalf("动物填充配的蠕虫占比应更高：动物 %.3f vs 植物 %.3f",
			animalProf.Diet["worm"], plantProf.Diet["worm"])
	}
	if animalProf.Diet["plant"] >= plantProf.Diet["plant"] {
		t.Fatalf("动物填充配的植物占比应更低：动物 %.3f vs 植物 %.3f",
			animalProf.Diet["plant"], plantProf.Diet["plant"])
	}

	// 同一套食性公式：雅罗鱼更吃动物填充，银鲫更吃植物填充。
	ecology := groundbaitProbeEcology(t)
	for _, probe := range []struct {
		fishKey string
		first   groundbaitProfile
		second  groundbaitProfile
	}{
		{"dace", animalProf, plantProf},
		{"crucian", plantProf, animalProf},
	} {
		fish, hasRule, accepted := groundbaitProbeFish(ecology, probe.fishKey)
		if len(fish) == 0 || !hasRule {
			t.Fatalf("%s 缺少生态或攻略资料", probe.fishKey)
		}
		a, _ := groundbaitAppetite(probe.first.Diet, fish, accepted, 0.08)
		b, _ := groundbaitAppetite(probe.second.Diet, fish, accepted, 0.08)
		if a <= b {
			t.Errorf("%s 应更吃对应的 PVA 填充配：%.3f vs %.3f", probe.fishKey, a, b)
		}
	}

	// 入水链路也要遵守同一口径：纯 PVA 不改权重，填了干混合才开始吸引鱼。
	s, _ := newRigTestServer(t)
	setWeatherTestTunable(t, s, "groundbait_enabled", "1")
	pvaRoll := func(uid int64) float64 {
		t.Helper()
		c := fishRollContext{
			uid: uid, loc: "gb-test-loc", x: 100, z: 100, hasPos: true, spot: 7,
			now: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC), rigType: 1, depthCM: 100,
			cache:   newFishingRollCache(),
			weather: fishingWeather{known: true, kind: fishingWeatherClear, airTemp: 18, previousAirTemp: 18, wind: 3},
		}
		wts := []float64{100}
		s.applyGroundbaitWeight(t.Context(), &c, []string{"crucian"}, wts)
		return wts[0] / 100
	}

	const uidEmpty = int64(1000004)
	s.groundbaitDeposit(t.Context(), uidEmpty, "gb-test-loc", 100, 100, true, 7,
		"pva_stick_m", 6, "pva", "gb:pva-empty", "")
	if got := pvaRoll(uidEmpty); got != 1 {
		t.Fatalf("纯 PVA 网不该吸引鱼，实得 x%.3f", got)
	}

	const uidFilled = int64(1000005)
	tx, err := s.DB.SQL.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := groundbaitRecordInstanceMaterialsTx(t.Context(), tx, "gb-pva-fill", plantFill); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	s.groundbaitDeposit(t.Context(), uidFilled, "gb-test-loc", 100, 100, true, 7,
		"pva_stick_m", 6, "pva", "gb:pva-fill", "gb-pva-fill")
	if got := pvaRoll(uidFilled); got <= 1 {
		t.Fatalf("填了干混合的 PVA 棒应吸引银鲫，实得 x%.3f", got)
	}
}

// 「属性差不多的两团料不能一个天上一个地下」：主力食性只比次族多一点点时，
// 不算专攻某一口，专食加成归零；契合度本身则必须跟着占比平滑变化。
func TestGroundbaitSpecialNeedsClearDominance(t *testing.T) {
	fish := map[string]float64{"plant": 2.03, "worm": 1.5, "boilie": 0.4}
	accepted := map[string]bool{"plant": true, "boilie": true}
	clear := map[string]float64{"plant": 0.60, "boilie": 0.40}
	coin := map[string]float64{"plant": 0.52, "boilie": 0.48}

	appClear, specClear := groundbaitAppetite(clear, fish, accepted, 0.08)
	appCoin, specCoin := groundbaitAppetite(coin, fish, accepted, 0.08)
	if specClear <= 0 {
		t.Fatalf("主力族压过次族 0.20 时应给专食加成，实得 %.2f", specClear)
	}
	if specCoin != 0 {
		t.Fatalf("主力族只比次族多 0.04，不该算专攻一口，实得专食性 %.2f", specCoin)
	}
	if math.Abs(appClear-appCoin) > 0.2 {
		t.Fatalf("两团料只是占比差一点，契合度不该差很多：%.3f vs %.3f", appClear, appCoin)
	}

	// 阈值调 0 之后，半斤八两的组合重新拿到加成：留给实机单变量验证。
	if _, specOff := groundbaitAppetite(coin, fish, accepted, 0); specOff <= 0 {
		t.Fatalf("阈值调成 0 后应当恢复专食加成，实得 %.2f", specOff)
	}
}

// 掠食者冻结必须两票齐全：生态表说它几乎不吃素，且攻略里也没有植物/煮饵的记录。
func TestGroundbaitPredatorFreezeNeedsBothVotes(t *testing.T) {
	pure := map[string]float64{"lure": 1.9, "fish": 1.8, "plant": 0.2}
	if !groundbaitPredatorFrozen(pure, true, map[string]bool{"fish": true}, 1.5, 0.8, 0.8) {
		t.Fatal("两票都不认素饵，应当冻结")
	}
	if groundbaitPredatorFrozen(pure, false, map[string]bool{}, 1.5, 0.8, 0.8) {
		t.Fatal("攻略里压根没收录这条鱼，第二票不成立，不能只靠生态表单票冻结")
	}
	if groundbaitPredatorFrozen(pure, true, map[string]bool{"plant": true}, 1.5, 0.8, 0.8) {
		t.Fatal("攻略里认植物饵，第二票不成立，不能冻结")
	}
	if groundbaitPredatorFrozen(map[string]float64{"lure": 1.9, "plant": 1.2}, true, map[string]bool{}, 1.5, 0.8, 0.8) {
		t.Fatal("生态表里它确实吃素，第一票不成立，不能冻结")
	}
	if groundbaitPredatorFrozen(map[string]float64{"lure": 1.2, "plant": 0.2}, true, map[string]bool{}, 1.9, 0.8, 0.8) {
		t.Fatal("lure 不到阈值上限不能冻结")
	}
}

// 食性向量必须归一成百分比，并且和原料表里的分类一致。
func TestGroundbaitProfileNormalisesDiet(t *testing.T) {
	defs := groundbaitDefsLoad()
	prof, ok := defs.profileOf([]string{"mix_semolina", "feedadds_bloodworms"})
	if !ok {
		t.Fatal("两种原料算不出食性向量")
	}
	total := 0.0
	for _, w := range prof.Diet {
		if w <= 0 || w > 1 {
			t.Fatalf("食性占比必须落在 (0,1]：%v", prof.Diet)
		}
		total += w
	}
	if math.Abs(total-1) > 1e-9 {
		t.Fatalf("食性占比之和必须是 100%%，实得 %.6f", total)
	}
	if prof.Diet["plant"] <= 0 || prof.Diet["worm"] <= 0 {
		t.Fatalf("碎麦米该算植物、红虫该算动物，实得 %v", prof.Diet)
	}
	// 香精只点缀：同一种基底加不加香精，动物性占比不该变向另一边。
	plain, _ := defs.profileOf([]string{"mix_semolina", "feedadds_bloodworms"})
	spiked, _ := defs.profileOf([]string{"mix_semolina", "feedadds_bloodworms", "aroma_fishoil"})
	if spiked.Diet["plant"] >= plain.Diet["plant"] {
		t.Fatalf("加了鱼肝油应当更偏动物（植物占比更低）：加前 %.3f 加后 %.3f",
			plain.Diet["plant"], spiked.Diet["plant"])
	}
	// 没登记的原料退回保守档案，不能整团料消失。
	unknown, ok := defs.profileOf([]string{"mix_不存在的料"})
	if !ok || unknown.Diet["plant"] <= 0 {
		t.Fatalf("未知原料应当按普通素食料兜底，实得 ok=%v %v", ok, unknown.Diet)
	}
}

// 自制窝料的整条链路：制作时记下用料 → 投放时冻进账本行 → 判鱼按实际用料抬权重。
// 两袋料在客户端都叫 feedcompleted，能区分它们的只有实例上记的那份用料。
func TestGroundbaitHomeMadeBagJudgedByRecordedMaterials(t *testing.T) {
	s, _ := newRigTestServer(t)
	setWeatherTestTunable(t, s, "groundbait_enabled", "1")
	const uidAnimal = int64(1000001)
	const uidPlant = int64(1000002)
	const uidNoRecord = int64(1000003)

	animal := []string{"mix_semolina", "feedadds_bloodworms", "feedadds_casters", "feedadds_clay", "aroma_fishoil"}
	plant := []string{"mix_groundcrackers", "feedadds_corn", "feedadds_pelets", "aroma_honey"}
	tx, err := s.DB.SQL.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range []struct {
		guid string
		mats []string
	}{{"gb-animal-instance", animal}, {"gb-plant-instance", plant}} {
		if err := groundbaitRecordInstanceMaterialsTx(t.Context(), tx, pair.guid, pair.mats); err != nil {
			t.Fatalf("写入 %s 的用料失败: %v", pair.guid, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// 空清单不该留行：那时没什么可说，读的人会退回 item_id 的固定档案。
	tx, err = s.DB.SQL.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := groundbaitRecordInstanceMaterialsTx(t.Context(), tx, "gb-empty-instance", nil); err != nil {
		t.Fatalf("空用料写入应当直接跳过: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB.SQL.QueryRow(`SELECT COUNT(*) FROM groundbait_recipe`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("应当只记下两袋料的用料，实得 %d 条", n)
	}

	deposit := func(uid int64, guid, txn string) {
		t.Helper()
		s.groundbaitDeposit(t.Context(), uid, "gb-test-loc", 100, 100, true, 7, "feedcompleted", 6, "hand", txn, guid)
	}
	deposit(uidAnimal, "gb-animal-instance", "gb:hm-animal")
	deposit(uidPlant, "gb-plant-instance", "gb:hm-plant")
	deposit(uidNoRecord, "gb-missing-instance", "gb:hm-none")

	// 投放时必须把当时的用料冻进账本行：实例之后会被吃掉删掉，窝还要活几小时。
	var stored string
	if err := s.DB.SQL.QueryRow(`SELECT materials_json FROM groundbait_deposit WHERE uid=?`, uidAnimal).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if got := groundbaitDecodeMaterials(stored); len(got) != len(animal) {
		t.Fatalf("账本行里应当冻住 %d 种用料，实得 %q", len(animal), stored)
	}

	roll := func(uid int64, fishKey string) float64 {
		t.Helper()
		c := fishRollContext{
			uid: uid, loc: "gb-test-loc", x: 100, z: 100, hasPos: true, spot: 7,
			now: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC), rigType: 1, depthCM: 100,
			cache:   newFishingRollCache(),
			weather: fishingWeather{known: true, kind: fishingWeatherClear, airTemp: 18, previousAirTemp: 18, wind: 3},
		}
		wts := []float64{100}
		s.applyGroundbaitWeight(t.Context(), &c, []string{fishKey}, wts)
		return wts[0] / 100
	}
	// 雅罗鱼吃动物口更香，银鲫吃植物口更香：两袋同名不同料的窝必须分出高下。
	if m := roll(uidAnimal, "dace"); m <= roll(uidPlant, "dace") {
		t.Errorf("雅罗鱼应当更吃动物配那格：动物 %.3f vs 植物 %.3f", m, roll(uidPlant, "dace"))
	}
	if m := roll(uidPlant, "crucian"); m <= roll(uidAnimal, "crucian") {
		t.Errorf("银鲫应当更吃植物配那格：植物 %.3f vs 动物 %.3f", m, roll(uidAnimal, "crucian"))
	}
	// 没记用料的老口袋退回中性档案，判定不该整格失效。
	if m := roll(uidNoRecord, "crucian"); m <= 1 {
		t.Errorf("没有用料记录的自制窝料应当按中性档案生效，实得 %.3f", m)
	}
	for _, uid := range []int64{uidAnimal, uidPlant, uidNoRecord} {
		if m := roll(uid, "n.pike"); m != 1 {
			t.Errorf("掠食性鱼种不该蹭自制窝，实得 %.3f (uid %d)", m, uid)
		}
	}
}
