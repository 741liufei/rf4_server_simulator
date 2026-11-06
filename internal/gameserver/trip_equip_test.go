package gameserver

// 钓鱼游装备包测试：购票发放（租用标记/飞机图标）、重复购票去重、到期回收。

import (
	"context"
	"testing"
	"time"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

func TestTripEquipmentGrantAndRecall(t *testing.T) {
	srv, _ := newEconomyTxTestServer(t)
	ctx0 := context.Background()
	uid := int64(3910)
	place := "level_018_moskitnoe"
	if _, err := srv.DB.EnsureProfile(ctx0, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.DB.SQL.Exec(`UPDATE profile SET silver=50000 WHERE uid=?`, uid); err != nil {
		t.Fatal(err)
	}

	// 购票 → 进图（syncTripEquipment 模拟 1/13 落点/1/14 切图）→ 装备包发放。
	ctx := tripTestCtx(srv, uid, tripArgs(place, false, 29800), false)
	if body, handled := hPlaceTripTx(ctx); !handled || body == nil {
		t.Fatalf("购票失败: handled=%v body=%v", handled, body != nil)
	}
	srv.syncTripEquipment(ctx0, uid, place)
	grants, err := srv.DB.UnrecalledTripGrants(ctx0, uid, place)
	if err != nil {
		t.Fatal(err)
	}
	bundle := tripEquipmentBundles[place]
	wantPieces := int64(0)
	for _, e := range bundle {
		wantPieces += e.Units
	}
	if int64(len(grants)) != wantPieces {
		t.Fatalf("装备包应按件发放 %d 件: %d", wantPieces, len(grants))
	}
	// 每件：在背包、无租用对象（四轮实测租用对象一律"锁定"）、f12 带官方
	// 钓鱼游标志 16|256（2026-11-06 实测：16 清状态解锁等级锁、256 角标/tooltip）。
	for _, g := range grants {
		row, err := srv.DB.GetItemByGUID(ctx0, g.GUID)
		if err != nil || row == nil {
			t.Fatalf("发放物品缺失: %s", g.GUID)
		}
		if row.Container != "backpack" {
			t.Fatalf("发放容器错误: %s %s", row.ItemID, row.Container)
		}
		fields, ferr := FieldsFromJSON(row.FieldsJSON)
		if ferr != nil {
			t.Fatal(ferr)
		}
		if ri := fieldIdxByCS(row.WireClass, "bkmnbppciao"); ri >= 0 && ri < len(fields) &&
			fields[ri].Val != nil && fields[ri].Val.Kind != proto.KindNull {
			t.Fatalf("%s 不应带租用对象(位8=锁定)", row.ItemID)
		}
		fi := fieldIdxByCS(row.WireClass, "dbgaheblmei")
		if fi < 0 || fi >= len(fields) || fields[fi].Num == nil ||
			*fields[fi].Num&16 == 0 || *fields[fi].Num&256 == 0 {
			t.Fatalf("%s f12 缺钓鱼游标志 16|256", row.ItemID)
		}
	}

	// 重复购票（时长累加）→ 再进图 → 不重复发放。
	ctx2 := tripTestCtx(srv, uid, tripArgs(place, false, 29800), false)
	if _, handled := hPlaceTripTx(ctx2); !handled {
		t.Fatal("第二次购票未处理")
	}
	srv.syncTripEquipment(ctx0, uid, place)
	grants2, _ := srv.DB.UnrecalledTripGrants(ctx0, uid, place)
	if int64(len(grants2)) != wantPieces {
		t.Fatalf("重复购票不应重复发放: %d", len(grants2))
	}

	// 到期 → 装备包回收（物品删除、发放记录闭环）。两次购票 remaining=36000s，
	// 回拨 40000s 确保越过到期线；会话起点须早于回拨后的 last_consume_ts，
	// 否则会落入"离线暂停"分支不扣时。
	sess := &Session{ID: 1, Userid: uid, Srv: srv, log: logging.Discard()}
	sess.closed.Store(true)
	sess.startedAt = time.Now().Add(-40001 * time.Second)
	if _, err := srv.DB.SQL.Exec(
		`UPDATE trip_state SET last_consume_ts=last_consume_ts-40000 WHERE uid=? AND place=?`,
		uid, place); err != nil {
		t.Fatal(err)
	}
	if !sess.consumeTripTimes(time.Now()) {
		t.Fatal("到期未触发回收")
	}
	grants3, _ := srv.DB.UnrecalledTripGrants(ctx0, uid, place)
	if len(grants3) != 0 {
		t.Fatalf("回收后仍有未回收记录: %d", len(grants3))
	}
	for _, g := range grants {
		row, _ := srv.DB.GetItemByGUID(ctx0, g.GUID)
		if row != nil {
			t.Fatalf("到期后物品未删除: %s", row.ItemID)
		}
	}

	// 切到其他地图 → 该地图装备包回收（离图即收，重新进图再发）。
	// 场景独立用例：上面三次购票已用满限购，这里换 uid 走完整进/出循环。
	srv2, _ := newEconomyTxTestServer(t)
	ctxB := context.Background()
	uidB := int64(3912)
	placeB := "level_018_moskitnoe"
	if _, err := srv2.DB.EnsureProfile(ctxB, uidB); err != nil {
		t.Fatal(err)
	}
	if _, err := srv2.DB.SQL.Exec(`UPDATE profile SET silver=50000 WHERE uid=?`, uidB); err != nil {
		t.Fatal(err)
	}
	ctx4 := tripTestCtx(srv2, uidB, tripArgs(placeB, false, 29800), false)
	if _, handled := hPlaceTripTx(ctx4); !handled {
		t.Fatal("购票未处理")
	}
	srv2.syncTripEquipment(ctxB, uidB, placeB)
	grantsB, _ := srv2.DB.UnrecalledTripGrants(ctxB, uidB, placeB)
	if int64(len(grantsB)) != wantPieces {
		t.Fatalf("进图应发放 %d 件: %d", wantPieces, len(grantsB))
	}
	srv2.syncTripEquipment(ctxB, uidB, "level_001_lake")
	grantsB2, _ := srv2.DB.UnrecalledTripGrants(ctxB, uidB, placeB)
	if len(grantsB2) != 0 {
		t.Fatalf("切图后应回收: %d", len(grantsB2))
	}
	for _, g := range grantsB {
		row, _ := srv2.DB.GetItemByGUID(ctxB, g.GUID)
		if row != nil {
			t.Fatalf("切图后物品未删除: %s", row.ItemID)
		}
	}
	// 重新进图：再次发放（离图即收、进图再发的闭环）。
	srv2.syncTripEquipment(ctxB, uidB, placeB)
	grantsB3, _ := srv2.DB.UnrecalledTripGrants(ctxB, uidB, placeB)
	if int64(len(grantsB3)) != wantPieces {
		t.Fatalf("重新进图应再次发放 %d 件: %d", wantPieces, len(grantsB3))
	}
}

func TestTripEquipmentNorthSeaBundle(t *testing.T) {
	// 挪威海清单完整性：按件发放后全部落到物品定义。
	srv, _ := newEconomyTxTestServer(t)
	ctx0 := context.Background()
	uid := int64(3911)
	place := "level_012_north_sea"
	if _, err := srv.DB.EnsureProfile(ctx0, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.DB.SQL.Exec(`UPDATE profile SET silver=99999999 WHERE uid=?`, uid); err != nil {
		t.Fatal(err)
	}
	ctx := tripTestCtx(srv, uid, tripArgs(place, false, 139500), false)
	if _, handled := hPlaceTripTx(ctx); !handled {
		t.Fatal("购票未处理")
	}
	srv.syncTripEquipment(ctx0, uid, place)
	grants, err := srv.DB.UnrecalledTripGrants(ctx0, uid, place)
	if err != nil {
		t.Fatal(err)
	}
	bundle := tripEquipmentBundles[place]
	wantPieces := int64(0)
	for _, e := range bundle {
		wantPieces += e.Units
	}
	if int64(len(grants)) != wantPieces {
		t.Fatalf("挪威海装备包应发放 %d 件: %d", wantPieces, len(grants))
	}
	seen := map[string]bool{}
	for _, g := range grants {
		seen[g.ItemID] = true
	}
	for _, e := range bundle {
		if !seen[e.ItemID] {
			t.Fatalf("挪威海清单缺件: %s", e.ItemID)
		}
	}
}
