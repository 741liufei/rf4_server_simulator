package gameserver

import (
	"fmt"
	"math"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

func snapshotFields(t *testing.T, raw string) []proto.Field {
	t.Helper()
	fields, err := FieldsFromJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	return fields
}
func snapshotJSON(t *testing.T, fields []proto.Field) string {
	t.Helper()
	raw, err := FieldsToJSON(fields)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func snapshotPart(t *testing.T, srv *Server, fields []proto.Field, category int64) (*proto.Value, *store.ItemDef) {
	t.Helper()
	child, def, _, ok := srv.selectedReelPart(t.Context(), fields, category)
	if !ok || child == nil || def == nil {
		t.Fatalf("missing part category %d", category)
	}
	return child, def
}

func snapshotFloat(fields []proto.Field, class, name string) float64 {
	i := fieldIdxByCS(class, name)
	if i < 0 || i >= len(fields) {
		return math.NaN()
	}
	if fields[i].Num != nil {
		return float64(proto.F32FromBits(uint32(*fields[i].Num)))
	}
	return fields[i].Float
}

func assertReelSnapshot(t *testing.T, srv *Server, guid, gearID string, drag float64) *store.Item {
	t.Helper()
	ctx := t.Context()
	row := mustItem(t, srv.DB, guid)
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := srv.ItemToTree(ctx, row)
	if err != nil {
		t.Fatal(err)
	}
	expected := snapshotFloat(tree.Fields, tree.Class, csStrength)
	if math.IsNaN(expected) || math.IsInf(expected, 0) {
		t.Fatal("invalid root")
	}
	if math.Abs(expected-drag) > 1e-5 {
		t.Fatalf("root does not use installed washer: got %g want %g", expected, drag)
	}
	if got := snapshotFloat(fields, row.WireClass, csStrength); math.IsNaN(got) || math.IsInf(got, 0) || math.Abs(got-expected) > 1e-5 {
		t.Fatalf("stored root=%g, normalized=%g", got, expected)
	}
	for _, fs := range [][]proto.Field{fields, tree.Fields} {
		child, def, _, ok := srv.selectedReelPart(ctx, fs, 52)
		if !ok || def == nil || def.ItemID != gearID || child == nil {
			t.Fatalf("installed gear changed: %+v", def)
		}
		expectedDef, err := srv.DB.GetItemDefByItemID(ctx, gearID)
		if err != nil || expectedDef == nil {
			t.Fatalf("gear definition: %v", err)
		}
		if itemDefF32(def, gearAttrRatio) != itemDefF32(expectedDef, gearAttrRatio) {
			t.Fatal("gear ratio changed")
		}
		washer, _, _, ok := srv.selectedReelPart(ctx, fs, 9)
		if !ok {
			t.Fatal("washer missing")
		}
		value, present, _ := frictionWasherDrag(washer)
		if !present || math.Abs(value-drag) > 1e-5 {
			t.Fatalf("washer state changed: %g want %g", value, drag)
		}
	}
	raw, err := proto.EncodeValue(tree, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	decoded, end, err := proto.DecodeValue(raw, 0, srv.Table())
	if err != nil || end != len(raw) {
		t.Fatalf("tree codec: %v", err)
	}
	if snapshotFloat(decoded.Fields, decoded.Class, csStrength) != expected {
		t.Fatal("wire root differs")
	}
	return row
}

func snapshotWorkshopRequest(guid, id string) *proto.Value {
	return proto.Args524(proto.StrVal("workshop_01"), wiregen.MustMakeObj("nfmkdcbnkhp", map[int]*proto.Value{
		0: proto.Guid(guid), 1: proto.Args(proto.S("503"), wiregen.MustMakeObj("gcllbdkgkna", map[int]*proto.Value{0: proto.U32(52), 1: proto.StrVal(id)})), 2: proto.U32(0), 3: proto.U64(100)}))
}

func TestReelPartSnapshotWorkshop(t *testing.T) {
	for _, drag := range []float64{2.25, 0} {
		t.Run(fmt.Sprintf("drag_%g", drag), func(t *testing.T) {
			srv, db := newRigTestServer(t)
			defer db.Close()
			ctx := t.Context()
			const uid = int64(1000001)
			if err := srv.EnsurePlayer(ctx, uid); err != nil {
				t.Fatal(err)
			}
			profile, err := db.EnsureProfile(ctx, uid)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.UpdateBalance(ctx, uid, 1000000, profile.Gold, profile.Bank); err != nil {
				t.Fatal(err)
			}
			reel, err := srv.CreateItem(ctx, uid, "spin_5354", "backpack", 1)
			if err != nil {
				t.Fatal(err)
			}
			fields := snapshotFields(t, reel.FieldsJSON)
			washer, _, _, ok := srv.selectedReelPart(ctx, fields, 9)
			if !ok {
				t.Fatal("washer missing")
			}
			setWireFloat(washer.Fields, washer.Class, fieldIdxByCS(washer.Class, csDragForce), drag)
			raw := snapshotJSON(t, fields)
			reel.FieldsJSON = raw
			srv.canonicalizeReelStrength(ctx, reel, fields)
			raw = snapshotJSON(t, fields)
			if err := db.UpdateItemFields(ctx, reel.GUID, raw, reel.Qty, reel.Durability, reel.Workmanship, reel.RigGUID); err != nil {
				t.Fatal(err)
			}
			_, stock := snapshotPart(t, srv, fields, 52)
			assertReelSnapshot(t, srv, reel.GUID, stock.ItemID, drag)
			session, peer := newTunnelProbe(t, srv)
			var previousRatio float64
			for _, id := range []string{"rgm_zmn_jet_jsg-r", "rgm_ef_spark10021"} {
				req := snapshotWorkshopRequest(reel.GUID, id)
				encoded, err := proto.EncodeValue(req, srv.Table())
				if err != nil {
					t.Fatal(err)
				}
				decoded, _, err := proto.DecodeValue(encoded, 0, srv.Table())
				if err != nil {
					t.Fatal(err)
				}
				c := &Ctx{Sess: session, Main: 21, Sub: 3, Tree: decoded, DecodeOK: true, ExpectResponse: true, Log: logging.Discard()}
				balanceBefore, err := db.EnsureProfile(ctx, uid)
				if err != nil {
					t.Fatal(err)
				}
				body, ok := hWorkshopOrder(c)
				if !ok || len(body) == 0 {
					t.Fatal("workshop failed")
				}
				balanceAfter, err := db.EnsureProfile(ctx, uid)
				if err != nil {
					t.Fatal(err)
				}
				if balanceBefore.Silver-balanceAfter.Silver != 100 {
					t.Fatal("workshop charge changed")
				}
				fresh := assertReelSnapshot(t, srv, reel.GUID, id, drag)
				reply, _, err := proto.DecodeValue(body, 0, srv.Table())
				if err != nil {
					t.Fatal(err)
				}
				stored := snapshotFields(t, fresh.FieldsJSON)
				if snapshotFloat(reply.Fields, reply.Class, csStrength) != snapshotFloat(stored, fresh.WireClass, csStrength) {
					t.Fatal("response differs from database")
				}
				if len(c.afterResponse) != 1 {
					t.Fatal("missing refresh")
				}
				for _, fn := range c.afterResponse {
					fn()
				}
				push := awaitPushEnvelope(t, peer)
				if push.Main != 4 || push.Sub != 22 {
					t.Fatalf("wrong push %d/%d", push.Main, push.Sub)
				}
				v, _, err := proto.DecodeValue(push.Body, 0, srv.Table())
				if err != nil {
					t.Fatal(err)
				}
				pushed, err := proto.EncodeValue(v.ArgItem(0), srv.Table())
				if err != nil {
					t.Fatal(err)
				}
				if string(pushed) != string(body) {
					t.Fatal("AfterResponse differs from response")
				}
				def, defErr := db.GetItemDefByItemID(ctx, id)
				if defErr != nil || def == nil {
					t.Fatalf("gear definition: %v", defErr)
				}
				ratio := itemDefF32(def, gearAttrRatio)
				if previousRatio != 0 && ratio == previousRatio {
					t.Fatal("fixtures need different ratios")
				}
				previousRatio = ratio
			}
			// Same-model gear repair restores only the installed gear's durability.
			fresh := mustItem(t, db, reel.GUID)
			fs := snapshotFields(t, fresh.FieldsJSON)
			gear, _ := snapshotPart(t, srv, fs, 52)
			setWireFloat(gear.Fields, gear.Class, fieldIdxByCS(gear.Class, csDurability), .4)
			raw = snapshotJSON(t, fs)
			if err := db.UpdateItemFields(ctx, fresh.GUID, raw, fresh.Qty, fresh.Durability, fresh.Workmanship, fresh.RigGUID); err != nil {
				t.Fatal(err)
			}
			c := &Ctx{Sess: newProbeSess(srv), Tree: snapshotWorkshopRequest(reel.GUID, "rgm_ef_spark10021"), Log: logging.Discard()}
			gearRepairBody, gearRepaired := hWorkshopOrder(c)
			if !gearRepaired {
				t.Fatal("gear repair failed")
			}
			gearRepairTree, _, gearRepairErr := proto.DecodeValue(gearRepairBody, 0, srv.Table())
			if gearRepairErr != nil {
				t.Fatal(gearRepairErr)
			}
			repairedGear, repairedDef := snapshotPart(t, srv, gearRepairTree.Fields, 52)
			if snapshotFloat(repairedGear.Fields, repairedGear.Class, csDurability) != 1 {
				t.Fatal("gear repair response condition")
			}
			if repairedDef.ItemID != "rgm_ef_spark10021" {
				t.Fatal("gear repair response model")
			}
			fresh = assertReelSnapshot(t, srv, reel.GUID, "rgm_ef_spark10021", drag)
			fs = snapshotFields(t, fresh.FieldsJSON)
			gear, _ = snapshotPart(t, srv, fs, 52)
			if snapshotFloat(gear.Fields, gear.Class, csDurability) != 1 {
				t.Fatal("gear not repaired")
			}
			if err := db.SetItemDurability(ctx, reel.GUID, .6); err != nil {
				t.Fatal(err)
			}
			repair := proto.Args524(proto.StrVal("workshop_01"), wiregen.MustMakeObj("nfmkdcbnkhp", map[int]*proto.Value{0: proto.Guid(reel.GUID), 1: proto.Args524(proto.U32(52)), 2: proto.U32(0), 3: proto.U64(100)}))
			c = &Ctx{Sess: newProbeSess(srv), Tree: repair, Log: logging.Discard()}
			repairBody, repaired := hWorkshopRepair(false)(c)
			if !repaired {
				t.Fatal("reel repair failed")
			}
			repairTree, _, repairErr := proto.DecodeValue(repairBody, 0, srv.Table())
			if repairErr != nil {
				t.Fatal(repairErr)
			}
			if snapshotFloat(repairTree.Fields, repairTree.Class, csDurability) != 1 {
				t.Fatal("reel repair response durability")
			}
			// 进店修理是整机大保养：轮内烧掉、磨旧的件一并恢复，否则本体修回 1.0
			// 也收不了线。所以修完之后根受力回到当前安装摩擦片的额定值。
			repairedRow := mustItem(t, db, reel.GUID)
			ratedDrag, ratedOK := srv.reelWasherRatedFromRow(ctx, repairedRow)
			if !ratedOK || ratedDrag <= 0 {
				t.Fatal("installed washer rated drag after repair")
			}
			if repairedRow.Durability != 1 {
				t.Fatalf("reel durability after repair=%v", repairedRow.Durability)
			}
			assertReelSnapshot(t, srv, reel.GUID, "rgm_ef_spark10021", ratedDrag)
		})
	}
}

func TestReelPartSnapshotStartup(t *testing.T) {
	for _, drag := range []float64{2.25, 0} {
		t.Run(fmt.Sprintf("drag_%g", drag), func(t *testing.T) {
			srv, db := newRigTestServer(t)
			defer db.Close()
			ctx := t.Context()
			reel, err := srv.CreateItem(ctx, 1000001, "spin_5354", "backpack", 1)
			if err != nil {
				t.Fatal(err)
			}
			fields := snapshotFields(t, reel.FieldsJSON)
			gear, def := snapshotPart(t, srv, fields, 52)
			washer, _ := snapshotPart(t, srv, fields, 9)
			setWireFloat(gear.Fields, gear.Class, fieldIdxByCS(gear.Class, csDurability), .4)
			setWireFloat(washer.Fields, washer.Class, fieldIdxByCS(washer.Class, csDragForce), drag)
			setWireFloat(fields, reel.WireClass, fieldIdxByCS(reel.WireClass, csStrength), itemDefF32(def, gearAttrRatio))
			raw := snapshotJSON(t, fields)
			if err := db.UpdateItemFields(ctx, reel.GUID, raw, reel.Qty, reel.Durability, reel.Workmanship, reel.RigGUID); err != nil {
				t.Fatal(err)
			}
			// The legacy zero-washer migration is independently covered; this fixture is a worn current save.
			if err := db.KVSet(ctx, reelFrictionZeroMigrationKey, "done"); err != nil {
				t.Fatal(err)
			}
			if _, err := srv.RepairReelComponents(ctx); err != nil {
				t.Fatal(err)
			}
			assertReelSnapshot(t, srv, reel.GUID, def.ItemID, drag)
			originalGear, err := proto.EncodeValue(gear, srv.Table())
			if err != nil {
				t.Fatal(err)
			}
			var previous string
			for i := 0; i < 2; i++ {
				if i > 0 {
					path := db.Path
					if err := db.Close(); err != nil {
						t.Fatal(err)
					}
					db, err = store.Open(path)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = db.Close() })
					srv = NewServer(srv.cfg, logging.Discard(), nil)
					srv.SetDB(db)
				}
				if err := srv.RepairItemPhysics(ctx); err != nil {
					t.Fatal(err)
				}
				if _, err := srv.RepairReelComponents(ctx); err != nil {
					t.Fatal(err)
				}
				fresh := assertReelSnapshot(t, srv, reel.GUID, def.ItemID, drag)
				if i > 0 && fresh.FieldsJSON != previous {
					t.Fatal("startup not idempotent")
				}
				saved, err := FieldsFromJSON(fresh.FieldsJSON)
				if err != nil {
					t.Fatal(err)
				}
				savedGear, _, _, ok := srv.selectedReelPart(ctx, saved, 52)
				if !ok {
					t.Fatal("gear missing after startup")
				}
				afterGear, err := proto.EncodeValue(savedGear, srv.Table())
				if err != nil {
					t.Fatal(err)
				}
				if string(afterGear) != string(originalGear) {
					t.Fatal("startup changed installed gear identity or condition")
				}
				previous = fresh.FieldsJSON
			}
		})
	}
}

func TestReelPartSnapshotBatchAndUnrelatedParts(t *testing.T) {
	for _, drag := range []float64{2.25, 0} {
		t.Run(fmt.Sprintf("drag_%g", drag), func(t *testing.T) {
			srv, db := newRigTestServer(t)
			defer db.Close()
			ctx := t.Context()
			const uid = int64(1000001)
			if err := srv.EnsurePlayer(ctx, uid); err != nil {
				t.Fatal(err)
			}
			profile, err := db.EnsureProfile(ctx, uid)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.UpdateBalance(ctx, uid, 1000000, profile.Gold, profile.Bank); err != nil {
				t.Fatal(err)
			}
			reel, err := srv.CreateItem(ctx, uid, "spin_5354", "backpack", 1)
			if err != nil {
				t.Fatal(err)
			}
			fields := snapshotFields(t, reel.FieldsJSON)
			washer, _ := snapshotPart(t, srv, fields, 9)
			setWireFloat(washer.Fields, washer.Class, fieldIdxByCS(washer.Class, csDragForce), drag)
			protected := map[int64][]byte{}
			for _, part := range []struct {
				category                int64
				workmanship, durability float64
			}{{7, .7, 0}, {10, .8, .5}} {
				child, _ := snapshotPart(t, srv, fields, part.category)
				setWireFloat(child.Fields, child.Class, fieldIdxByCS(child.Class, csWorkmanship), part.workmanship)
				setWireFloat(child.Fields, child.Class, fieldIdxByCS(child.Class, csDurability), part.durability)
				raw, err := proto.EncodeValue(child, srv.Table())
				if err != nil {
					t.Fatal(err)
				}
				protected[part.category] = raw
			}
			save := func() {
				t.Helper()
				raw := snapshotJSON(t, fields)
				if err := db.UpdateItemFields(ctx, reel.GUID, raw, reel.Qty, reel.Durability, reel.Workmanship, reel.RigGUID); err != nil {
					t.Fatal(err)
				}
			}
			checkProtected := func() {
				t.Helper()
				stored := snapshotFields(t, mustItem(t, db, reel.GUID).FieldsJSON)
				for category, before := range protected {
					child, _ := snapshotPart(t, srv, stored, category)
					after, err := proto.EncodeValue(child, srv.Table())
					if err != nil {
						t.Fatal(err)
					}
					if string(after) != string(before) {
						t.Fatalf("unrelated category %d changed", category)
					}
				}
			}
			// A legacy root is corrected without changing unrelated installed parts.
			setWireFloat(fields, reel.WireClass, fieldIdxByCS(reel.WireClass, csStrength), 123)
			save()
			for i := 0; i < 2; i++ {
				if _, err := srv.RepairReelComponents(ctx); err != nil {
					t.Fatal(err)
				}
				checkProtected()
			}
			_, stock := snapshotPart(t, srv, fields, 52)
			assertReelSnapshot(t, srv, reel.GUID, stock.ItemID, drag)
			session, peer := newTunnelProbe(t, srv)
			// A gear-only order must preserve both worn and zero-valued washers and other parts.
			request := snapshotWorkshopRequest(reel.GUID, "rgm_zmn_jet_jsg-r")
			c := &Ctx{Sess: session, Tree: request, Log: logging.Discard()}
			if _, ok := hWorkshopOrder(c); !ok {
				t.Fatal("gear-only order failed")
			}
			checkProtected()
			assertReelSnapshot(t, srv, reel.GUID, "rgm_zmn_jet_jsg-r", drag)
			// The protocol accepts multiple part orders in the same request.
			orders := proto.Args(proto.S("503"),
				wiregen.MustMakeObj("gcllbdkgkna", map[int]*proto.Value{0: proto.U32(9), 1: proto.StrVal("ef-fb-sp.orig-5805")}),
				wiregen.MustMakeObj("gcllbdkgkna", map[int]*proto.Value{0: proto.U32(52), 1: proto.StrVal("rgm_ef_spark10021")}),
			)
			request = proto.Args524(proto.StrVal("workshop_01"), wiregen.MustMakeObj("nfmkdcbnkhp", map[int]*proto.Value{0: proto.Guid(reel.GUID), 1: orders, 2: proto.U32(0), 3: proto.U64(100)}))
			requestBytes, err := proto.EncodeValue(request, srv.Table())
			if err != nil {
				t.Fatal(err)
			}
			decoded, end, err := proto.DecodeValue(requestBytes, 0, srv.Table())
			if err != nil || end != len(requestBytes) {
				t.Fatalf("request decode: %v", err)
			}
			c = &Ctx{Sess: session, Main: 21, Sub: 3, Tree: decoded, DecodeOK: true, ExpectResponse: true, Log: logging.Discard()}
			body, ok := hWorkshopOrder(c)
			if !ok || len(body) == 0 {
				t.Fatal("batch replacement failed")
			}
			checkProtected()
			// A newly purchased washer has its definition force; this is a replacement, not revival of the old washer.
			row := assertReelSnapshot(t, srv, reel.GUID, "rgm_ef_spark10021", 3.5)
			stored := snapshotFields(t, row.FieldsJSON)
			_, newWasher := snapshotPart(t, srv, stored, 9)
			if newWasher.ItemID != "ef-fb-sp.orig-5805" {
				t.Fatal("washer was not replaced")
			}
			reply, end, err := proto.DecodeValue(body, 0, srv.Table())
			if err != nil || end != len(body) {
				t.Fatalf("response decode: %v", err)
			}
			if snapshotFloat(reply.Fields, reply.Class, csStrength) != 3.5 {
				t.Fatal("response uses old washer")
			}
			_, replyGear := snapshotPart(t, srv, reply.Fields, 52)
			if replyGear.ItemID != "rgm_ef_spark10021" {
				t.Fatal("response uses old gear")
			}
			if len(c.afterResponse) != 1 {
				t.Fatal("batch refresh missing")
			}
			c.afterResponse[0]()
			push := awaitPushEnvelope(t, peer)
			if push.Main != 4 || push.Sub != 22 {
				t.Fatal("wrong refresh command")
			}
			value, _, err := proto.DecodeValue(push.Body, 0, srv.Table())
			if err != nil {
				t.Fatal(err)
			}
			pushed, err := proto.EncodeValue(value.ArgItem(0), srv.Table())
			if err != nil {
				t.Fatal(err)
			}
			if string(pushed) != string(body) {
				t.Fatal("batch refresh differs from response")
			}
			// Even after building and sending a projected tree, the persistent child state remains untouched.
			checkProtected()
		})
	}
}

func TestReelZeroDragRootPaths(t *testing.T) {
	for _, path := range []string{"tree", "repair", "workshop"} {
		t.Run(path, func(t *testing.T) {
			srv, db := newRigTestServer(t)
			defer db.Close()
			ctx := t.Context()
			const uid = int64(1000001)
			if err := srv.EnsurePlayer(ctx, uid); err != nil {
				t.Fatal(err)
			}
			profile, err := db.EnsureProfile(ctx, uid)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.UpdateBalance(ctx, uid, 1000000, profile.Gold, profile.Bank); err != nil {
				t.Fatal(err)
			}
			reel, err := srv.CreateItem(ctx, uid, "spin_5354", "backpack", 1)
			if err != nil {
				t.Fatal(err)
			}
			fields := snapshotFields(t, reel.FieldsJSON)
			washer, _ := snapshotPart(t, srv, fields, 9)
			setWireFloat(washer.Fields, washer.Class, fieldIdxByCS(washer.Class, csDragForce), 0)
			raw := snapshotJSON(t, fields)
			if err := db.UpdateItemFields(ctx, reel.GUID, raw, reel.Qty, reel.Durability, reel.Workmanship, reel.RigGUID); err != nil {
				t.Fatal(err)
			}
			switch path {
			case "repair":
				if _, err := srv.RepairReelComponents(ctx); err != nil {
					t.Fatal(err)
				}
			case "workshop":
				c := &Ctx{Sess: newProbeSess(srv), Tree: snapshotWorkshopRequest(reel.GUID, "rgm_zmn_jet_jsg-r"), Log: logging.Discard()}
				if _, ok := hWorkshopOrder(c); !ok {
					t.Fatal("workshop failed")
				}
			}
			row := mustItem(t, db, reel.GUID)
			if path != "tree" {
				stored := snapshotFields(t, row.FieldsJSON)
				if got := snapshotFloat(stored, row.WireClass, csStrength); got != 0 {
					t.Fatalf("stored zero-drag root=%g, want 0", got)
				}
			}
			tree, err := srv.ItemToTree(ctx, row)
			if err != nil {
				t.Fatal(err)
			}
			if got := snapshotFloat(tree.Fields, tree.Class, csStrength); got != 0 {
				t.Fatalf("wire zero-drag root=%g, want 0", got)
			}
			child, _ := snapshotPart(t, srv, tree.Fields, 9)
			value, present, _ := frictionWasherDrag(child)
			if !present || value != 0 {
				t.Fatal("washer zero lost")
			}
		})
	}
}

func TestReelDragFieldPresenceAndFallback(t *testing.T) {
	for _, tc := range []struct {
		name    string
		value   float64
		present bool
		want    float64
	}{
		{"positive", 2.25, true, 2.25}, {"zero", 0, true, 0}, {"missing", 0, false, 3.5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, db := newRigTestServer(t)
			defer db.Close()
			ctx := t.Context()
			row, err := srv.CreateItem(ctx, 1000001, "spin_5354", "backpack", 1)
			if err != nil {
				t.Fatal(err)
			}
			fields := snapshotFields(t, row.FieldsJSON)
			_, _, slot, found := srv.selectedReelPart(ctx, fields, 9)
			if !found {
				t.Fatal("washer missing")
			}
			washer, err := srv.buildPartInstance(ctx, row, "ef-fb-sp.orig-5805", "test-washer")
			if err != nil || washer == nil {
				t.Fatalf("washer: %v", err)
			}
			idx := fieldIdxByCS(washer.Class, csDragForce)
			if idx < 0 || idx >= len(washer.Fields) {
				t.Fatal("force field missing")
			}
			if tc.present {
				setWireFloat(washer.Fields, washer.Class, idx, tc.value)
			} else {
				washer.Fields[idx].Num = nil
			}
			fields[slot].Val = washer
			setWireFloat(fields, row.WireClass, fieldIdxByCS(row.WireClass, csStrength), 123)
			row.FieldsJSON = snapshotJSON(t, fields)
			got, present := srv.reelInstalledDragFieldFromRow(ctx, row)
			if present != tc.present || (present && got != tc.value) {
				t.Fatalf("stored source=%g,%v", got, present)
			}
			// The existing reader keeps its original behavior for other callers.
			if value := srv.reelBaseDragFromRow(ctx, row); math.Abs(value-tc.want) > 1e-5 {
				t.Fatalf("base reader=%g want %g", value, tc.want)
			}
			childBefore := snapshotJSON(t, washer.Fields)
			srv.canonicalizeReelStrength(ctx, row, fields)
			if value := snapshotFloat(fields, row.WireClass, csStrength); math.Abs(value-tc.want) > 1e-5 {
				t.Fatalf("root=%g want %g", value, tc.want)
			}
			if snapshotJSON(t, washer.Fields) != childBefore {
				t.Fatal("root normalization changed washer")
			}
			raw := snapshotJSON(t, fields)
			if err := db.UpdateItemFields(ctx, row.GUID, raw, row.Qty, row.Durability, row.Workmanship, row.RigGUID); err != nil {
				t.Fatal(err)
			}
			fresh := mustItem(t, db, row.GUID)
			stored := snapshotFields(t, fresh.FieldsJSON)
			if value := snapshotFloat(stored, fresh.WireClass, csStrength); math.Abs(value-tc.want) > 1e-5 {
				t.Fatal("stored root mismatch")
			}
			tree, err := srv.ItemToTree(ctx, fresh)
			if err != nil {
				t.Fatal(err)
			}
			if value := snapshotFloat(tree.Fields, tree.Class, csStrength); math.Abs(value-tc.want) > 1e-5 {
				t.Fatal("wire root mismatch")
			}
		})
	}
}

func TestReelDegradePersistsRoot(t *testing.T) {
	for _, path := range []string{"degrade", "slip"} {
		for _, wearOut := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/worn_%t", path, wearOut), func(t *testing.T) {
				srv, db := newRigTestServer(t)
				defer db.Close()
				ctx := t.Context()
				const uid = int64(1000001)
				reel, err := srv.CreateItem(ctx, uid, "spin_5354", "backpack", 1)
				if err != nil {
					t.Fatal(err)
				}
				initial := snapshotFields(t, reel.FieldsJSON)
				if snapshotFloat(initial, reel.WireClass, csStrength) <= 0 {
					t.Fatal("new reel root must be positive")
				}
				washer, def := snapshotPart(t, srv, initial, 9)
				step := itemDefF32(def, gearAttrDragMax) * reel.Workmanship * reelSlipWear
				if step <= 0 {
					t.Fatal("invalid wear fixture")
				}
				current := step * 3
				if wearOut {
					current = step / 2
				}
				setWireFloat(washer.Fields, washer.Class, fieldIdxByCS(washer.Class, csDragForce), current)
				reel.FieldsJSON = snapshotJSON(t, initial)
				srv.canonicalizeReelStrength(ctx, reel, initial)
				reel.FieldsJSON = snapshotJSON(t, initial)
				if err := db.UpdateItemFields(ctx, reel.GUID, reel.FieldsJSON, reel.Qty, reel.Durability, reel.Workmanship, reel.RigGUID); err != nil {
					t.Fatal(err)
				}
				washerBefore := snapshotJSON(t, washer.Fields)
				// Call the actual wear persistence path; do not run a repair or workshop operation.
				if path == "degrade" {
					worn, found, err := srv.degradeFrictionWasher(ctx, reel)
					if err != nil || !found || worn != wearOut {
						t.Fatalf("wear result %t/%t: %v", worn, found, err)
					}
				} else {
					out, err := srv.damageReelSlip(ctx, uid, zeroGUID, reel.GUID)
					if err != nil || out == nil || out.FrictionWorn != wearOut {
						t.Fatalf("slip result: %+v %v", out, err)
					}
				}
				fresh := mustItem(t, db, reel.GUID)
				fields := snapshotFields(t, fresh.FieldsJSON)
				afterWasher, _ := snapshotPart(t, srv, fields, 9)
				unchanged := append([]proto.Field(nil), afterWasher.Fields...)
				setWireFloat(unchanged, afterWasher.Class, fieldIdxByCS(afterWasher.Class, csDragForce), current)
				// 磨损还会把摩擦片耐久写成制动力剩余比例的镜像（背包与工坊靠它看损耗%），
				// 比对时一并还原，确认其余身份字段没被动过。
				setWireFloat(unchanged, afterWasher.Class, fieldIdxByCS(afterWasher.Class, csDurability),
					snapshotFloat(washer.Fields, washer.Class, csDurability))
				if snapshotJSON(t, unchanged) != washerBefore {
					t.Fatal("wear changed washer identity, condition or other fields")
				}
				force, present, _ := frictionWasherDrag(afterWasher)
				if !present || math.IsNaN(force) || math.IsInf(force, 0) {
					t.Fatal("stored washer force missing or invalid")
				}
				if wearOut && force != 0 {
					t.Fatalf("washer did not wear out: %g", force)
				}
				if !wearOut && (force <= 0 || force >= current) {
					t.Fatalf("intermediate wear missing: %g", force)
				}
				if root := snapshotFloat(fields, fresh.WireClass, csStrength); root != force {
					t.Errorf("immediate stored root=%g washer=%g", root, force)
				}
				tree, err := srv.ItemToTree(ctx, fresh)
				if err != nil {
					t.Fatal(err)
				}
				if root := snapshotFloat(tree.Fields, tree.Class, csStrength); root != force {
					t.Errorf("wire root=%g washer=%g", root, force)
				}
				encoded, err := proto.EncodeValue(tree, srv.Table())
				if err != nil {
					t.Fatal(err)
				}
				decoded, end, err := proto.DecodeValue(encoded, 0, srv.Table())
				if err != nil || end != len(encoded) {
					t.Fatalf("worn reel codec: %v", err)
				}
				if snapshotFloat(decoded.Fields, decoded.Class, csStrength) != force {
					t.Fatal("encoded worn root mismatch")
				}
				// 本体耐久是轮内零件的综合：磨薄摩擦片它就得降（只降不升），
				// 烧穿时熔断到 0，客户端才会红框、才修不好就收不了线。
				if fresh.Durability > reel.Durability {
					t.Fatalf("wear raised reel durability: %v -> %v", reel.Durability, fresh.Durability)
				}
				if path == "slip" {
					if wearOut != (fresh.Durability == 0) {
						t.Fatalf("reel durability must follow washer wear: worn=%t durability=%v", wearOut, fresh.Durability)
					}
				} else if fresh.Durability != reel.Durability {
					// 只磨零件字段的入口不负责本体：聚合发生在结算与伤害入口。
					t.Fatalf("bare washer degradation must not touch reel durability: %v -> %v", reel.Durability, fresh.Durability)
				}
				perf, err := srv.syncReelPerformance(ctx, fresh)
				if err != nil {
					t.Fatal(err)
				}
				if math.Abs(perf.BaseDragKg-force) > 1e-5 {
					t.Fatal("performance mismatch")
				}
				storedAgain := snapshotFields(t, mustItem(t, db, reel.GUID).FieldsJSON)
				if root := snapshotFloat(storedAgain, fresh.WireClass, csStrength); root != force {
					t.Errorf("after performance sync root=%g washer=%g", root, force)
				}
			})
		}
	}
}
