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

// assertCraftedLeaderUsable 校验成品引线：内嵌线材必须是真实前导线定义，
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
	wireClass := categoryWireClass[gearCatLeaderWire]
	lineObj := fields[fieldIdxByCS(crafted.WireClass, gearAttrLeaderIn)].Val
	if lineObj == nil || lineObj.Kind != proto.KindObj {
		t.Fatalf("成品缺少内嵌线材: %+v", lineObj)
	}
	if lineObj.Class != wireClass.Class || lineObj.TypeID != uint32(wireClass.TypeID) {
		t.Fatalf("内线协议类型=%s/%d，期望 %s/%d",
			lineObj.Class, lineObj.TypeID, wireClass.Class, wireClass.TypeID)
	}
	numericID := int64(u64Of(lineObj.Fields[fieldIdxByCS(lineObj.Class, csNumericID)].Num))
	wireDef, err := db.GetItemDef(ctx, numericID)
	if err != nil || wireDef == nil {
		t.Fatalf("内线定义不存在: %d", numericID)
	}
	if wireDef.Category != gearCatLeaderWire {
		t.Fatalf("内线模型 %s 品类=%d，期望前导线 %d",
			wireDef.ItemID, wireDef.Category, gearCatLeaderWire)
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
	reloaded := tree.Fields[fieldIdxByCS(tree.Class, gearAttrLeaderIn)].Val
	if reloaded == nil || reloaded.Kind != proto.KindObj ||
		u64Of(reloaded.Fields[fieldIdxByCS(reloaded.Class, csNumericID)].Num) != uint64(numericID) {
		t.Fatalf("重新下发后内线模型发生变化: %+v", reloaded)
	}
}

// TestEveryLineCanCraftCraftedLeader 全量验证目录里的每一根线都能做引线：
// 不报错、模型是前导线、强度取真实材料、能入背包也能重新下发。
// 目录里 1044 根主线中 890 根没有参数完全一致的前导线定义，
// 旧实现在这些线上一律失败。
func TestEveryLineCanCraftCraftedLeader(t *testing.T) {
	if testing.Short() {
		t.Skip("全量 1237 根线材实测约 100s，-short 下只跑映射覆盖")
	}
	db := openRecipeTestDB(t)
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
	if len(lineDefs) == 0 || len(wireDefs) == 0 {
		t.Fatal("测试库缺少线材或前导线目录")
	}

	checked := 0
	for _, batch := range [][]*store.ItemDef{lineDefs, wireDefs} {
		for _, def := range batch {
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
				t.Errorf("线材 %s（%.2fkg）映射到零拉力前导线 %s", def.ItemID, want, wireDef.ItemID)
				continue
			}
			crafted := craftLeaderOnce(t, srv, db, uid, def.ItemID)
			assertCraftedLeaderUsable(t, srv, db, crafted, itemDefF32(def, gearAttrMaxLoad))
			if _, err := db.SQL.ExecContext(ctx, "DELETE FROM item WHERE uid=?", uid); err != nil {
				t.Fatal(err)
			}
			checked++
		}
	}
	if checked != len(lineDefs)+len(wireDefs) {
		t.Fatalf("覆盖了 %d 根线材，目录共 %d 根", checked, len(lineDefs)+len(wireDefs))
	}
}

// TestLeaderWireMappingCoversEveryLine 是 -short 也跑的快速版本：
// 只验证目录里每一根线都能选到一根像样的前导线定义。
func TestLeaderWireMappingCoversEveryLine(t *testing.T) {
	db := openRecipeTestDB(t)
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
	for _, def := range append(append([]*store.ItemDef{}, lineDefs...), wireDefs...) {
		if _, err := srv.leaderWireDefForLine(ctx, def); err != nil {
			t.Errorf("线材 %s 无法映射前导线: %v", def.ItemID, err)
		}
	}
}

// TestCraftedLeaderFromUnmappedLineEquipsIntoRig 验证"做出来还能用"：
// 用目录里没有孪生前导线的 60kg 编织线做一根引线，装到海钓钓组的前导槽，
// 钓组必须把它当成真前导。
func TestCraftedLeaderFromUnmappedLineEquipsIntoRig(t *testing.T) {
	db := openRecipeTestDB(t)
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
