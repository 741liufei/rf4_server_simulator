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
	if drag > 0 && math.Abs(expected-drag) > 1e-5 {
		t.Fatal("root does not use installed washer")
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
			assertReelSnapshot(t, srv, reel.GUID, "rgm_ef_spark10021", drag)
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
