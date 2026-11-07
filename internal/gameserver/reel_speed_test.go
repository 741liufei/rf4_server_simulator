package gameserver

// 17/15 收线速度（路亚族）持久化回归。
// 2026-11-07 抓包：客户端调“收线速度”发 17/15（f5.f7），调“摩擦”发 17/14（f5.f6）。
// 服务端此前只处理 17/14，17/15 的值被直接丢弃，重登后速度回到默认。

import (
	"context"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

func TestRodSetSpeedPersists17_15(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	rod, err := srv.CreateItem(ctx, testUID, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatalf("创建竿失败: %v", err)
	}
	sess := newProbeSess(srv)

	tree := proto.Args524(proto.Guid(rod.GUID), proto.U32(41))
	body, handled := hRodSetSpeed(&Ctx{Sess: sess, Main: 17, Sub: 15, Tree: tree,
		DecodeOK: true, Log: logging.Discard()})
	if !handled || len(body) != 0 {
		t.Fatalf("17/15 处理失败: handled=%v body=%d", handled, len(body))
	}

	got, err := db.GetRodSetting(ctx, rod.GUID, reelSpeedSettingKey)
	if err != nil || got != "41" {
		t.Fatalf("收线速度未按竿持久化: got=%q err=%v", got, err)
	}
	if speed, ok := srv.rodReelSpeedSetting(ctx, rod.GUID); !ok || speed != 41 {
		t.Fatalf("收线速度读取失败: got=%d ok=%v", speed, ok)
	}

	row, err := db.GetItemByGUID(ctx, rod.GUID)
	if err != nil || row == nil {
		t.Fatalf("重读竿实例失败: %v", err)
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	idx := fieldIdxByCS(row.WireClass, csRigObject)
	if idx < 0 || idx >= len(fields) || fields[idx].Val == nil || fields[idx].Val.FieldCount() <= 7 {
		t.Fatal("竿实例 f5 缺失")
	}
	if got := u32Of(fields[idx].Val.Field(7).Num); got != 41 {
		t.Fatalf("f5.f7 收线速度=%d，期望 41", got)
	}

	st := srv.stateFor(testUID)
	st.mu.Lock()
	speed := st.ReelSetting
	st.mu.Unlock()
	if speed != 41 {
		t.Fatalf("会话收线速度=%d，期望 41", speed)
	}
}

func TestBuildRig153LikePreservesReelSettings(t *testing.T) {
	src := buildRig153Full("rig_Lure", 5, nil, 0, 0, 0, 0)
	src.Fields[6] = proto.Field{Op: "U32", Num: ptrU64(24)}
	src.Fields[7] = proto.Field{Op: "U32", Num: ptrU64(41)}

	got := buildRig153Like(src, "rig_Lure", 5, nil, 0, 0)
	if drag := u32Of(got.Field(6).Num); drag != 24 {
		t.Fatalf("重建钓组把摩擦档位冲成 %d，期望 24", drag)
	}
	if speed := u32Of(got.Field(7).Num); speed != 41 {
		t.Fatalf("重建钓组把收线速度冲成 %d，期望 41", speed)
	}
}

// 存量竿没有 f5 时，登录会走 defaultRig153；已保存的摩擦/速度也要回填。
func TestDefaultRig153RestoresSavedReelSettings(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	seedShopRodDef(t, db)
	insertBareRod(t, db, testUID, testShopRodGUID, "feeder_10126_FD390", 5559)
	if err := db.UpsertRodSetting(ctx, testShopRodGUID, reelDragSettingKey, "24"); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertRodSetting(ctx, testShopRodGUID, reelSpeedSettingKey, "41"); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetItemByGUID(ctx, testShopRodGUID)
	if err != nil || row == nil {
		t.Fatalf("读取竿实例失败: %v", err)
	}
	rig := srv.defaultRig153(ctx, row)
	if drag := u32Of(rig.Field(6).Num); drag != 24 {
		t.Fatalf("缺失 f5 时摩擦档位=%d，期望 24", drag)
	}
	if speed := u32Of(rig.Field(7).Num); speed != 41 {
		t.Fatalf("缺失 f5 时收线速度=%d，期望 41", speed)
	}
}
