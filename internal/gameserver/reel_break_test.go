package gameserver

// 渔轮损坏回归：
// TestReelSlipWearsFrictionWasherOut 验证泄力打滑磨的是摩擦片(cat9)制动力
// csDragForce，本体耐久作为零件综合一起降；磨穿后泄力失效（最大泄力归零）。
// TestBrokenReelBlocksCastRig 验证 rigBrokenReelGUID 只对已装配且耐久归零的轮子生效。

import (
	"context"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

// mountReelOnRod 用 17/2 把轮子装到竿上，返回装配后的轮子行。
func mountReelOnRod(t *testing.T, srv *Server, rodGUID, reelGUID string) {
	t.Helper()
	sess := newProbeSess(srv)
	tree := rigRequestTree(t, proto.U32(0), proto.Guid(rodGUID), proto.U32(0),
		proto.Guid(reelGUID), proto.U32(0))
	body, ok := hRodSetComp(&Ctx{Sess: sess, Main: 17, Sub: 2, Tree: tree,
		DecodeOK: true, Log: logging.Discard()})
	if !ok || len(body) == 0 {
		t.Fatal("17/2 装渔轮未处理")
	}
}

func TestReelSlipWearsFrictionWasherOut(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatalf("造竿失败: %v", err)
	}
	reel, err := srv.CreateItem(ctx, uid, "bclp_5004", "backpack", 1)
	if err != nil {
		t.Fatalf("造轮失败: %v", err)
	}
	mountReelOnRod(t, srv, rod.GUID, reel.GUID)

	initial, perr := srv.syncReelPerformance(ctx, reel)
	if perr != nil || initial.BaseDragKg <= 0 {
		t.Fatalf("轮子应有摩擦片制动力: %+v err=%v", initial, perr)
	}

	// 泄力打滑磨的是摩擦片（基准 reelSlipWear=0.5%），本体耐久不变。
	var worn bool
	var last *gearBreakOutcome
	for i := 0; i < 200; i++ {
		outcome, derr := srv.damageReelSlip(ctx, uid, rod.GUID, reel.GUID)
		if derr != nil {
			t.Fatalf("第 %d 次打滑失败: %v", i+1, derr)
		}
		last = outcome
		if outcome.FrictionWorn {
			worn = true
			break
		}
	}
	if !worn {
		t.Fatal("连续打滑后摩擦片仍未磨穿")
	}
	if last == nil || last.ReelBroken {
		t.Fatalf("摩擦片磨穿只应标记 FrictionWorn，不应标记 ReelBroken: %+v", last)
	}

	row, err := db.GetItemByGUID(ctx, reel.GUID)
	if err != nil || row == nil {
		t.Fatalf("读取轮子失败: %v", err)
	}
	// 摩擦片是熔断件：烧穿之后本体耐久必须归零，客户端才会给红框、
	// 轮子才会收不了线、才必须去换片或进修理。
	if row.Durability != 0 {
		t.Fatalf("摩擦片烧穿后本体耐久应归零，实得 %v", row.Durability)
	}

	// 泄力失效：基础刹车力与锁轮承力都归零，重新同步后仍为 0。
	// 注意用重新从库读取的 row（Fresh FieldsJSON），不能用 CreateItem 的旧指针。
	perf, perr := srv.syncReelPerformance(ctx, row)
	if perr != nil {
		t.Fatalf("重建轮子物理参数失败: %v", perr)
	}
	if perf.BaseDragKg > 1e-9 || perf.LockedDragKg > 1e-9 {
		t.Fatalf("摩擦片磨穿后泄力应失效，实得 base=%v locked=%v", perf.BaseDragKg, perf.LockedDragKg)
	}
}

func TestBrokenReelBlocksCastRig(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatalf("造竿失败: %v", err)
	}
	reel, err := srv.CreateItem(ctx, uid, "bclp_5004", "backpack", 1)
	if err != nil {
		t.Fatalf("造轮失败: %v", err)
	}
	mountReelOnRod(t, srv, rod.GUID, reel.GUID)

	// 完好轮子不拦抛竿。
	if got := srv.rigBrokenReelGUID(ctx, uid, rod.GUID); got != "" {
		t.Fatalf("完好轮子不应拦抛竿，实得 %q", got)
	}
	// 未装轮子的竿也不拦（手竿没有轮）。
	handRod, err := srv.CreateItem(ctx, uid, "tele_10012_tl16", "backpack", 1)
	if err != nil {
		t.Fatalf("造手竿失败: %v", err)
	}
	if got := srv.rigBrokenReelGUID(ctx, uid, handRod.GUID); got != "" {
		t.Fatalf("无轮竿不应拦抛竿，实得 %q", got)
	}

	if err := db.SetItemDurability(ctx, reel.GUID, 0); err != nil {
		t.Fatalf("写耐久失败: %v", err)
	}
	if got := srv.rigBrokenReelGUID(ctx, uid, rod.GUID); got != reel.GUID {
		t.Fatalf("耐久归零的轮子应拦抛竿，实得 %q", got)
	}
	// 别人的竿不认。
	if got := srv.rigBrokenReelGUID(ctx, int64(1000002), rod.GUID); got != "" {
		t.Fatalf("非本人竿不应判为损坏，实得 %q", got)
	}
}
