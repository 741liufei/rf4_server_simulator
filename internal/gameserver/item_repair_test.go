package gameserver

import (
	"context"
	"strings"
	"testing"

	"rf4server/internal/proto"
)

// TestRepairItemPhysics 验证物理字段修复会写回数据库，
// 包括顶层强度、刹车力、容器枚举和钓组模板。
func TestRepairItemPhysics(t *testing.T) {
	t.Parallel()
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "feeder_10126_FD390", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_15097", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	reel, err := srv.CreateItem(ctx, uid, "spin_5184", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	runRodSet(t, srv, sess, rod.GUID, line.GUID, 0)
	runRodSet(t, srv, sess, rod.GUID, reel.GUID, 0)

	// 构造异常快照：线强度=0、容器=0；轮顶层强度=0、刹车片 f0=0。
	corrupt := func(itGUID string, mutate func([]proto.Field)) {
		t.Helper()
		row, err := db.GetItemByGUID(ctx, itGUID)
		if err != nil || row == nil {
			t.Fatalf("读取待污染物品 %s: %v", itGUID, err)
		}
		fields, err := FieldsFromJSON(row.FieldsJSON)
		if err != nil {
			t.Fatal(err)
		}
		mutate(fields)
		fj, err := FieldsToJSON(fields)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.UpdateItemFields(ctx, row.GUID, fj, row.Qty, row.Durability, row.Workmanship, row.RigGUID); err != nil {
			t.Fatal(err)
		}
	}
	corrupt(line.GUID, func(fields []proto.Field) {
		fields[fieldIdxByCS(line.WireClass, csStrength)].Num = ptrU64(0)
		fields[fieldIdxByCS(line.WireClass, csSlotEnum)].Num = ptrU64(0)
	})
	corrupt(reel.GUID, func(fields []proto.Field) {
		fields[fieldIdxByCS(reel.WireClass, csStrength)].Num = ptrU64(0)
		for _, key := range reelPartKeys {
			if idx := fieldIdxByCS(reel.WireClass, key); idx >= 0 {
				fields[idx].Op = "VAL"
				fields[idx].Val = proto.Null()
			}
		}
	})

	// 覆盖 FD390 的错误钓组：type12 装有食笼31。
	rodRow, _ := db.GetItemByGUID(ctx, rod.GUID)
	fields, _ := FieldsFromJSON(rodRow.FieldsJSON)
	rigIdx := fieldIdxByCS(rod.WireClass, csRigObject)
	fields[rigIdx].Op = "VAL"
	fields[rigIdx].Val = buildRig153("rig_BottomSimple", 12, map[int64]rigComp{
		47: {GUID: "00000000000000000000000000000047", Extra: 100},
		12: {GUID: "00000000000000000000000000000012", Extra: 250},
		31: {GUID: "00000000000000000000000000000031", Extra: 200},
	}, 100)
	fj, _ := FieldsToJSON(fields)
	if err := db.UpdateItemFields(ctx, rod.GUID, fj, rodRow.Qty, rodRow.Durability, rodRow.Workmanship, rodRow.RigGUID); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertRig(ctx, rod.GUID, uid, 12, "", "{}"); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateRigType(ctx, rod.GUID, 12); err != nil {
		t.Fatal(err)
	}

	if err := srv.RepairItemPhysics(ctx); err != nil {
		t.Fatalf("RepairItemPhysics: %v", err)
	}

	lineRow, _ := db.GetItemByGUID(ctx, line.GUID)
	lineFields, _ := FieldsFromJSON(lineRow.FieldsJSON)
	lineStrength := float64(proto.F32FromBits(uint32(u64Of(lineFields[fieldIdxByCS(line.WireClass, csStrength)].Num))))
	if lineStrength <= 0 {
		t.Fatalf("修复后线强度仍为 %f", lineStrength)
	}
	if got := u64Of(lineFields[fieldIdxByCS(line.WireClass, csSlotEnum)].Num); got != 0 {
		t.Fatalf("修复后根线容器枚举=%d，期望 0（嵌套子件才是 rig(4)）", got)
	}

	reelRow, _ := db.GetItemByGUID(ctx, reel.GUID)
	reelFields, _ := FieldsFromJSON(reelRow.FieldsJSON)
	reelStrength := float64(proto.F32FromBits(uint32(u64Of(reelFields[fieldIdxByCS(reel.WireClass, csStrength)].Num))))
	if reelStrength <= 0 {
		t.Fatalf("修复后渔轮顶层强度仍为 %f", reelStrength)
	}
	filledParts := 0
	for _, key := range reelPartKeys {
		field := reelFields[fieldIdxByCS(reel.WireClass, key)]
		if field.Val != nil && field.Val.Kind != proto.KindNull {
			filledParts++
		}
	}
	if filledParts == 0 {
		t.Fatal("修复后渔轮 f2..f14 子件仍全空")
	}
	drag := reelFields[fieldIdxByCS(reel.WireClass, "mlfjkaoicdn")].Val
	if drag == nil || drag.Kind == proto.KindNull {
		t.Fatal("修复后渔轮曳力子件 mlfjkaoicdn 仍为空")
	}
	dragForce := float64(proto.F32FromBits(uint32(u64Of(drag.Fields[fieldIdxByCS(drag.Class, csDragForce)].Num))))
	if dragForce <= 0 {
		t.Fatalf("修复后刹车子实例曳力仍为 %f", dragForce)
	}

	rodRow, _ = db.GetItemByGUID(ctx, rod.GUID)
	rigType, found, err := db.GetRigType(ctx, rod.GUID)
	if err != nil || !found || rigType != 12 {
		t.Fatalf("修复必须尊重显式/现有合法类型，实得 %d found=%v err=%v", rigType, found, err)
	}
	f5 := rigStoredF5(t, rodRow)
	if name := f5.Fields[0].StrOr(); name != "rig_BottomSimple" {
		t.Fatalf("修复后 f5.f0=%q，期望保持 rig_BottomSimple", name)
	}
	if v, found, err := db.KVGet(ctx, itemPhysicsRepairKey); err != nil || !found || !strings.Contains(v, "version="+itemPhysicsRepairVersion) {
		t.Fatalf("修复完成标记=%q found=%v err=%v", v, found, err)
	}

	// 标记已存在时仍必须重新扫描，以修复后续导入或回档产生的异常行。
	corrupt(line.GUID, func(fields []proto.Field) {
		fields[fieldIdxByCS(line.WireClass, csStrength)].Num = ptrU64(0)
	})
	if err := srv.RepairItemPhysics(ctx); err != nil {
		t.Fatalf("第二次 RepairItemPhysics: %v", err)
	}
	lineRow, _ = db.GetItemByGUID(ctx, line.GUID)
	lineFields, _ = FieldsFromJSON(lineRow.FieldsJSON)
	lineStrength = float64(proto.F32FromBits(uint32(u64Of(lineFields[fieldIdxByCS(line.WireClass, csStrength)].Num))))
	if lineStrength <= 0 {
		t.Fatalf("已有完成标记后新增异常行未被修复: strength=%f", lineStrength)
	}
}
