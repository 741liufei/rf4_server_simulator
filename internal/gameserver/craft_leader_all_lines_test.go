package gameserver

import (
	"context"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// 全量引线覆盖使用的固定配方、产物与连接件。
const (
	leaderSweepRecipe    = "craft_leader_mar_40"
	leaderSweepOutput    = "leader_crafted_mar_40"
	leaderSweepConnector = "swlock_40009"
	leaderSweepLengthCM  = 40
)

// 真实制作只抽样覆盖不同线种、材质、拉力区间和特殊 ID；
// 全目录映射与强度检查仍由 TestLeaderWireMappingCoversEveryLine 覆盖。
var representativeLeaderCraftItems = []string{
	// 主线（cat 11）：覆盖 express / syberia / simmons / snake / pomor /
	// solid_mats / brand_ussr，以及低、中、高拉力和特殊历史 ID。
	"mono_15061", "braid_15060",
	"mono_15138", "braid_15125",
	"mono_15208", "braid_15207",
	"fluoro_15823", "braid_15794",
	"pom_mono_02", "braid_40284",
	"fluoro_40298", "braid_40907",
	"surv_f_line", "braid_15765",
	// 前导线（cat 15）：覆盖 simmons / syberia / atami / express / solid_mats。
	"wrl_sim_monorig_4LB", "wrl_40051",
	"wrl_syb_sline_5LB", "br_leader_40195",
	"mn_lm_40797", "mn_lm_40809",
	"wrl_40001", "mn_lm_40283", "wireline_40817",
}

// craftLeaderOnce 用一根指定线材走一次真实 9/1 制作，返回入背包的成品引线。
// 调用方负责清空背包，避免每次扫描都要重扫越来越多的物品行。
func craftLeaderOnce(t *testing.T, srv *Server, db *store.DB, uid int64, lineItemID string) *store.Item {
	t.Helper()
	ctx := context.Background()

	line, err := srv.CreateItem(ctx, uid, lineItemID, "backpack", 1)
	if err != nil {
		t.Fatalf("主线 %s 无法入包: %v", lineItemID, err)
	}
	connector, err := srv.CreateItem(ctx, uid, leaderSweepConnector, "backpack", 1)
	if err != nil {
		t.Fatalf("连接件入包失败: %v", err)
	}

	sess := &Session{ID: 1, Userid: uid, Srv: srv, log: logging.Discard()}
	tree := proto.Args524(craftObj8(leaderSweepRecipe, []craftSlot{
		{ItemID: "craft_mat_line", GUID: line.GUID},
		{ItemID: "craft_mat_swivels", GUID: connector.GUID},
	}, 1))
	hctx := &Ctx{
		Sess: sess, Main: 9, Sub: 1, CallID: 1, Tree: tree, DecodeOK: true,
		Log: logging.Discard(),
	}
	body, ok := hCraft(hctx)
	if !ok {
		t.Fatalf("主线 %s: 9/1 未处理", lineItemID)
	}
	result := decodeTreeValue(t, srv, body)
	if got := u64Of(result.Fields[0].Num); got != 1 {
		t.Fatalf("主线 %s 做引线失败: 803.f0=%d", lineItemID, got)
	}

	rows, err := db.ListItemsByContainers(ctx, uid, []string{"backpack"})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ItemID == leaderSweepOutput {
			return row
		}
	}
	t.Fatalf("主线 %s: 成品引线没有入背包", lineItemID)
	return nil
}

// assertCraftedLeaderUsable 校验成品引线：线材必须落在与材料相同的协议槽位，
// 强度按玩家那一根线计算，并且物品还能重新物化下发。
func assertCraftedLeaderUsable(
	t *testing.T, srv *Server, db *store.DB, crafted *store.Item, wantLineKg float64,
) {
	t.Helper()
	ctx := context.Background()

	fields, err := FieldsFromJSON(crafted.FieldsJSON)
	if err != nil {
		t.Fatalf("成品 fields_json 不可解析: %v", err)
	}
	lineKey := craftedMainLineKey
	lineObj := fields[fieldIdxByCS(crafted.WireClass, lineKey)].Val
	if lineObj == nil || lineObj.Kind == proto.KindNull {
		lineKey = gearAttrLeaderIn
		lineObj = fields[fieldIdxByCS(crafted.WireClass, lineKey)].Val
	}
	if lineObj == nil || lineObj.Kind != proto.KindObj {
		t.Fatalf("成品缺少内嵌线材: %+v", lineObj)
	}
	lineCategory := gearCatLine
	if lineKey == gearAttrLeaderIn {
		lineCategory = gearCatLeaderWire
	}
	wireClass := categoryWireClass[lineCategory]
	if lineObj.Class != wireClass.Class || lineObj.TypeID != uint32(wireClass.TypeID) {
		t.Fatalf("内线协议类型=%s/%d，期望 %s/%d",
			lineObj.Class, lineObj.TypeID, wireClass.Class, wireClass.TypeID)
	}
	numericID := int64(u64Of(lineObj.Fields[fieldIdxByCS(lineObj.Class, csNumericID)].Num))
	wireDef, err := db.GetItemDef(ctx, numericID)
	if err != nil || wireDef == nil {
		t.Fatalf("内线定义不存在: %d", numericID)
	}
	if wireDef.Category != lineCategory {
		t.Fatalf("内线模型 %s 品类=%d，期望材料品类 %d",
			wireDef.ItemID, wireDef.Category, lineCategory)
	}
	if got := u64Of(lineObj.Fields[fieldIdxByCS(lineObj.Class, gearAttrLineLen)].Num); got != leaderSweepLengthCM {
		t.Fatalf("内线长度=%d，期望 %dcm", got, leaderSweepLengthCM)
	}
	if got := wireFloatForTest(t, lineObj, csStrength); got < wantLineKg-0.01 || got > wantLineKg+0.01 {
		t.Fatalf("内线强度=%f，期望按真实线材 %.2fkg", got, wantLineKg)
	}
	root := wireFloatForTest(t, &proto.Value{
		Kind: proto.KindObj, Class: crafted.WireClass, Fields: fields, TypeID: uint32(crafted.TypeID),
	}, csStrength)
	if root <= 0 {
		t.Fatalf("成品引线总强度=%f，必须为正", root)
	}

	tree, err := srv.ItemToTree(ctx, crafted)
	if err != nil {
		t.Fatalf("成品引线无法下发: %v", err)
	}
	reloaded := tree.Fields[fieldIdxByCS(tree.Class, lineKey)].Val
	if reloaded == nil || reloaded.Kind != proto.KindObj ||
		u64Of(reloaded.Fields[fieldIdxByCS(reloaded.Class, csNumericID)].Num) != uint64(numericID) {
		t.Fatalf("重新下发后内线模型发生变化: %+v", reloaded)
	}
}

// TestRepresentativeLinesCanCraftCraftedLeader 抽样走真实 9/1 制作：
// 覆盖不同线种、材质和拉力区间，验证成品模型、强度、入包和重新下发。
func TestRepresentativeLinesCanCraftCraftedLeader(t *testing.T) {
	t.Parallel()
	db := openStaticTestDB(t)
	ctx := context.Background()
	srv := newSkillsCraftServer(t, db)
	const uid = int64(2000043)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}

	lineDefs, err := db.ListItemDefsByCategory(ctx, gearCatLine)
	if err != nil {
		t.Fatal(err)
	}
	wireDefs, err := db.ListItemDefsByCategory(ctx, gearCatLeaderWire)
	if err != nil {
		t.Fatal(err)
	}
	defs := make(map[string]*store.ItemDef, len(lineDefs)+len(wireDefs))
	for _, batch := range [][]*store.ItemDef{lineDefs, wireDefs} {
		for _, def := range batch {
			defs[def.ItemID] = def
		}
	}

	for _, itemID := range sampleForShort(t, representativeLeaderCraftItems, 4) {
		def := defs[itemID]
		if def == nil {
			t.Fatalf("抽样线材 %s 不在目录中", itemID)
		}
		wireDef, err := srv.leaderWireDefForLine(ctx, def)
		if err != nil {
			t.Errorf("线材 %s 无法映射前导线: %v", itemID, err)
			continue
		}
		if wireDef.Category != gearCatLeaderWire {
			t.Errorf("线材 %s 映射到 %s（品类 %d），期望前导线 %d",
				itemID, wireDef.ItemID, wireDef.Category, gearCatLeaderWire)
			continue
		}
		crafted := craftLeaderOnce(t, srv, db, uid, itemID)
		assertCraftedLeaderUsable(t, srv, db, crafted, itemDefF32(def, gearAttrMaxLoad))
		if _, err := db.SQL.ExecContext(ctx, "DELETE FROM item WHERE uid=?", uid); err != nil {
			t.Fatal(err)
		}
	}
}

// TestLeaderWireMappingCoversEveryLine 是 -short 也跑的快速版本：
// 只验证目录里每一根线都能选到一根像样的前导线定义。
func TestLeaderWireMappingCoversEveryLine(t *testing.T) {
	t.Parallel()
	db := openStaticTestDB(t)
	ctx := context.Background()
	srv := newSkillsCraftServer(t, db)

	lineDefs, err := db.ListItemDefsByCategory(ctx, gearCatLine)
	if err != nil {
		t.Fatal(err)
	}
	wireDefs, err := db.ListItemDefsByCategory(ctx, gearCatLeaderWire)
	if err != nil {
		t.Fatal(err)
	}
	if len(lineDefs) == 0 || len(wireDefs) == 0 {
		t.Fatal("测试库缺少线材或前导线目录")
	}
	lineDefs = sampleForShort(t, lineDefs, 12)
	wireDefs = sampleForShort(t, wireDefs, 12)
	for _, def := range append(append([]*store.ItemDef{}, lineDefs...), wireDefs...) {
		wireDef, err := srv.leaderWireDefForLine(ctx, def)
		if err != nil {
			t.Errorf("线材 %s 无法映射前导线: %v", def.ItemID, err)
			continue
		}
		if wireDef.Category != gearCatLeaderWire {
			t.Errorf("线材 %s 映射到 %s（品类 %d），期望前导线 %d",
				def.ItemID, wireDef.ItemID, wireDef.Category, gearCatLeaderWire)
			continue
		}
		if want := itemDefF32(def, gearAttrMaxLoad); want > 0 && itemDefF32(wireDef, gearAttrMaxLoad) <= 0 {
			t.Errorf("线材 %s（%.2fkg）映射到零拉力前导线 %s",
				def.ItemID, want, wireDef.ItemID)
		}
	}
}

// TestCraftedLeaderFromUnmappedLineEquipsIntoRig 验证"做出来还能用"：
// 用目录里没有孪生前导线的 60kg 编织线做一根引线，装到海钓钓组的前导槽，
// 钓组必须把它当成真前导。
func TestCraftedLeaderFromUnmappedLineEquipsIntoRig(t *testing.T) {
	t.Parallel()
	db := openStaticTestDB(t)
	ctx := context.Background()
	srv := newSkillsCraftServer(t, db)
	const uid = int64(2000044)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}

	crafted := craftLeaderOnce(t, srv, db, uid, "braid_15765")
	rod, err := srv.CreateItem(ctx, uid, "pilkr_10212_92-80190", "backpack", 1)
	if err != nil {
		t.Fatalf("造海竿失败: %v", err)
	}
	if !srv.rigSetType(ctx, uid, rod.GUID, 50) { // rig_MarinePilker：前导只有 175 一个槽
		t.Fatal("rigSetType 失败")
	}
	res := srv.rigSetComponent(ctx, uid, rod.GUID, 0, crafted.GUID, 175,
		rigSetOpts{ParentType: familyRod, EnforceCategory: true})
	if res == nil || res.Rejected {
		t.Fatalf("自制引线装不上钓组: %+v", res)
	}
	installed, err := db.GetItemByGUID(ctx, crafted.GUID)
	if err != nil || installed == nil {
		t.Fatal(err)
	}
	if installed.Container != "rig" {
		t.Fatalf("自制引线装配后容器=%s，期望 rig", installed.Container)
	}
	if gear := srv.rigGearIDs(ctx, uid, rod.GUID); gear.Leader == "" {
		t.Fatal("钓组没有把自制引线识别为前导")
	}
}
