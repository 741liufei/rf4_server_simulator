package gameserver

import (
	"context"
	"math"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

func TestCraftAllLeaderRecipesWithBothLineCategories(t *testing.T) {
	t.Parallel()
	db := openStaticTestDB(t)
	srv := newSkillsCraftServer(t, db)
	ctx := context.Background()
	rows, err := db.SQL.QueryContext(ctx, "SELECT recipe_id FROM recipe WHERE kind='leader' ORDER BY recipe_id")
	if err != nil {
		t.Fatal(err)
	}
	var recipeIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		recipeIDs = append(recipeIDs, id)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(recipeIDs) != 52 {
		t.Fatalf("应覆盖 52 个引线配方，实际 %d", len(recipeIDs))
	}
	recipeIDs = sampleForShort(t, recipeIDs, 4)
	uid := int64(2100000)
	for _, recipeID := range recipeIDs {
		for _, lineID := range []string{"mono_15065", "braid_15001", "br_leader_40077"} {
			t.Run(recipeID+"/"+lineID, func(t *testing.T) {
				recipe := srv.resolveRecipe(ctx, recipeID)
				if isCraftedShockLeader(recipe.OutputID) && lineID == "br_leader_40077" {
					return
				}
				uid++
				if _, err := db.EnsureProfile(ctx, uid); err != nil {
					t.Fatal(err)
				}
				line, err := srv.CreateItem(ctx, uid, lineID, "backpack", 1)
				if err != nil {
					t.Fatal(err)
				}
				srv.alignItemRowQty(ctx, line)
				lineFields, err := FieldsFromJSON(line.FieldsJSON)
				if err != nil {
					t.Fatal(err)
				}
				before, ok := itemQtyValue(line.WireClass, lineFields)
				if !ok || before == 0 {
					before = 1
				}
				beforeQty := int64(before)
				lineDef, err := db.GetItemDefByItemID(ctx, lineID)
				if err != nil {
					t.Fatal(err)
				}
				slots := []craftSlot{{ItemID: "craft_mat_line", GUID: line.GUID}}
				lineNeed := int64(0)
				for _, demand := range parseDemands(recipe.MaterialsJSON) {
					if demand.DemandID == "craft_mat_line" {
						lineNeed = demand.need()
					}
					if demand.DemandID == "craft_mat_swivels" {
						connector, err := srv.CreateItem(ctx, uid, "swlock_40009", "backpack", 1)
						if err != nil {
							t.Fatal(err)
						}
						srv.alignItemRowQty(ctx, connector)
						slots = append(slots, craftSlot{ItemID: demand.DemandID, GUID: connector.GUID})
					}
				}
				sess := &Session{ID: 1, Userid: uid, Srv: srv, log: logging.Discard()}
				hctx := &Ctx{Sess: sess, Main: 9, Sub: 1, CallID: 1, DecodeOK: true, Tree: proto.Args524(craftObj8(recipeID, slots, 1)), Log: logging.Discard()}
				body, ok := hCraft(hctx)
				if !ok || u64Of(decodeTreeValue(t, srv, body).Fields[0].Num) != 1 {
					t.Fatal("制作失败")
				}
				items, err := db.ListItemsByContainers(ctx, uid, []string{"backpack"})
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, item := range items {
					if item.ItemID != recipe.OutputID {
						continue
					}
					found = true
					tree, err := srv.ItemToTree(ctx, item)
					if err != nil {
						t.Fatal(err)
					}
					key, emptyKey := craftedMainLineKey, gearAttrLeaderIn
					if lineDef.Category == gearCatLeaderWire {
						key, emptyKey = emptyKey, key
					}
					inner := tree.Fields[fieldIdxByCS(tree.Class, key)].Val
					if inner == nil || inner.Kind != proto.KindObj || inner.Class != categoryWireClass[lineDef.Category].Class {
						t.Fatalf("线材槽位或协议类型错误: %+v", inner)
					}
					if u64Of(inner.Fields[fieldIdxByCS(inner.Class, csNumericID)].Num) != uint64(lineDef.NumericID) {
						t.Fatal("所选线材模型未保留")
					}
					rootDef, _ := db.GetItemDefByItemID(ctx, recipe.OutputID)
					wantLength := uint64(math.Round(itemDefF32(rootDef, gearAttrLength)))
					if u64Of(inner.Fields[fieldIdxByCS(inner.Class, gearAttrLineLen)].Num) != wantLength {
						t.Fatal("成品线材长度不符")
					}
					wantStrength := itemDefF32(lineDef, gearAttrMaxLoad)
					if len(slots) > 1 {
						connectorDef, _ := db.GetItemDefByItemID(ctx, "swlock_40009")
						wantStrength = math.Min(wantStrength, itemDefF32(connectorDef, gearAttrMaxLoad))
					}
					if math.Abs(wireFloatForTest(t, tree, csStrength)-wantStrength) > 0.001 {
						t.Fatal("成品强度不符")
					}
					if other := tree.Fields[fieldIdxByCS(tree.Class, emptyKey)].Val; other != nil && other.Kind != proto.KindNull {
						t.Fatal("另一线材槽位应为空")
					}
					changed, invalid, err := srv.repairCraftedLeaderWire(ctx, item, tree.Fields)
					if changed || invalid || err != nil {
						t.Fatalf("成品重载修复应保留物品: %v/%v/%v", changed, invalid, err)
					}
				}
				if !found {
					t.Fatal("背包缺少成品")
				}
				after, err := db.GetItemByGUID(ctx, line.GUID)
				if beforeQty == lineNeed && after == nil {
					return
				}
				if err != nil || after == nil || after.Qty != beforeQty-lineNeed {
					t.Fatalf("材料消耗错误: %+v, %v", after, err)
				}
			})
		}
	}
}
