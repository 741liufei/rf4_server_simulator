package gameserver

// 17/12 钓深、17/13 轮管卡与 17/14/17/15 一样按竿持久化：
// 客户端上报的 rodGUID 必须先校验归属，不能修改其他玩家的竿。

import (
	"context"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

func TestRodSetDepthRejectsForeignRod(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	rod, err := srv.CreateItem(ctx, testUID, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatalf("创建竿失败: %v", err)
	}
	srv.ensureRig(ctx, testUID, rod.GUID)
	before := storedRigField(t, db, rod.GUID, 4)

	attacker := &Session{ID: 2, Userid: 2000002, Srv: srv, log: logging.Discard()}
	tree := proto.Args524(proto.Guid(rod.GUID), proto.U32(250))
	if _, handled := hRodSetDepth(&Ctx{Sess: attacker, Main: 17, Sub: 12, Tree: tree,
		DecodeOK: true, Log: logging.Discard()}); !handled {
		t.Fatal("17/12 未处理")
	}

	if after := storedRigField(t, db, rod.GUID, 4); after != before {
		t.Fatalf("外玩家竿 f5.f4 钓深被改成 %d，期望保持 %d", after, before)
	}
	st := srv.stateFor(attacker.Userid)
	st.mu.Lock()
	depth := st.DepthCM
	st.mu.Unlock()
	if depth != 0 {
		t.Fatalf("被拒绝的 17/12 改了会话钓深: %d", depth)
	}
}

func TestRodSetClipRejectsForeignRod(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	rod, err := srv.CreateItem(ctx, testUID, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatalf("创建竿失败: %v", err)
	}
	srv.ensureRig(ctx, testUID, rod.GUID)

	attacker := &Session{ID: 2, Userid: 2000002, Srv: srv, log: logging.Discard()}
	tree := proto.Args524(proto.Guid(rod.GUID), proto.U32(500))
	if _, handled := hRodSetClip(&Ctx{Sess: attacker, Main: 17, Sub: 13, Tree: tree,
		DecodeOK: true, Log: logging.Discard()}); !handled {
		t.Fatal("17/13 未处理")
	}

	if got, err := db.GetRodSetting(ctx, rod.GUID, reelClipSettingKey); err == nil || got != "" {
		t.Fatalf("外玩家竿被写入轮管卡: got=%q err=%v", got, err)
	}
	if after := storedRigField(t, db, rod.GUID, 5); after != 0 {
		t.Fatalf("外玩家竿 f5.f5 轮管卡被改成 %d，期望保持默认 0", after)
	}
}

func TestRodSetQuivertipRejectsForeignRod(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	rod, err := srv.CreateItem(ctx, testUID, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatalf("创建竿失败: %v", err)
	}
	srv.ensureRig(ctx, testUID, rod.GUID)
	beforeTip, _ := rodTipOf(t, db, rod.GUID)

	attacker := &Session{ID: 2, Userid: 2000002, Srv: srv, log: logging.Discard()}
	tree := proto.Args524(proto.U32(0), proto.Guid(rod.GUID), proto.StrVal("kam_quiv_1.0"))
	if _, handled := hSetQuivertip(&Ctx{Sess: attacker, Main: 17, Sub: 5, Tree: tree,
		DecodeOK: true, Log: logging.Discard()}); !handled {
		t.Fatal("17/5 未处理")
	}

	if got, err := db.GetRodSetting(ctx, rod.GUID, "quivertip"); err == nil || got != "" {
		t.Fatalf("外玩家竿被写入竿稍设置: got=%q err=%v", got, err)
	}
	if afterTip, _ := rodTipOf(t, db, rod.GUID); afterTip != beforeTip {
		t.Fatalf("外玩家竿稍被改成 %q，期望保持 %q", afterTip, beforeTip)
	}
}

func TestRodSetRigTypeRejectsForeignRod(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	rod, err := srv.CreateItem(ctx, testUID, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatalf("创建竿失败: %v", err)
	}
	srv.ensureRig(ctx, testUID, rod.GUID)
	beforeType := storedRigField(t, db, rod.GUID, 1)

	attacker := &Session{ID: 2, Userid: 2000002, Srv: srv, log: logging.Discard()}
	tree := proto.Args524(proto.U32(0), proto.Guid(rod.GUID), proto.U32(12))
	if _, handled := hRigSetType(&Ctx{Sess: attacker, Main: 17, Sub: 1, Tree: tree,
		DecodeOK: true, Log: logging.Discard()}); !handled {
		t.Fatal("17/1 未处理")
	}

	if got, err := db.GetRodSetting(ctx, rod.GUID, "rig_type_explicit"); err == nil || got != "" {
		t.Fatalf("外玩家竿被写入钓组类型标记: got=%q err=%v", got, err)
	}
	if afterType := storedRigField(t, db, rod.GUID, 1); afterType != beforeType {
		t.Fatalf("外玩家竿钓组类型被改成 %d，期望保持 %d", afterType, beforeType)
	}
}

func TestRodBaitMountRejectsForeignRod(t *testing.T) {
	t.Parallel()
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	rod, err := srv.CreateItem(ctx, testUID, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatalf("创建竿失败: %v", err)
	}
	srv.ensureRig(ctx, testUID, rod.GUID)
	beforeMount := storedRigField(t, db, rod.GUID, 8)

	attacker := &Session{ID: 2, Userid: 2000002, Srv: srv, log: logging.Discard()}
	tree := proto.Args524(proto.U32(0), proto.Guid(rod.GUID), proto.U32(64))
	if _, handled := hRigBaitMount(&Ctx{Sess: attacker, Main: 17, Sub: 6, Tree: tree,
		DecodeOK: true, Log: logging.Discard()}); !handled {
		t.Fatal("17/6 未处理")
	}

	if got, err := db.GetRodSetting(ctx, rod.GUID, baitMountSettingKey); err == nil || got != "" {
		t.Fatalf("外玩家竿被写入挂饵方式: got=%q err=%v", got, err)
	}
	if afterMount := storedRigField(t, db, rod.GUID, 8); afterMount != beforeMount {
		t.Fatalf("外玩家竿挂饵方式被改成 %d，期望保持 %d", afterMount, beforeMount)
	}
}
