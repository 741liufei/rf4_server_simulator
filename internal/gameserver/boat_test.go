package gameserver

import (
	"strings"
	"testing"

	"rf4server/internal/proto"
)

// TestCheckTicket 验证 3/7 player/check_ticket 的响应形状：
//
// 客户端会把响应强转成 ckdficiaaji（wire 类 bplepbfgpmk / type_id 162）。
// 之前该命令落到兜底链、回的是空 args，客户端抛 InvalidCastException，
// BoatWatchDog 把玩家从船上传回岸边——即"买了船票也上不了船"。
func TestCheckTicket(t *testing.T) {
	srv, sess, ctx := newMiscProbe(t)

	it, err := srv.CreateItem(ctx, 1000001, "ticket_boat_komar_24", "backpack", 1)
	if err != nil || it == nil {
		t.Fatalf("创建船票失败: %v", err)
	}
	if it.TypeID != boatTicketTypeID {
		t.Fatalf("船票 type_id 应为 %d，实得 %d（wire %s）", boatTicketTypeID, it.TypeID, it.WireClass)
	}

	// 命中：返回船票对象本身。
	req := proto.Args524(proto.U32(1), proto.GuidArr([]string{it.GUID}))
	body, ok := hCheckTicket(miscCtx(sess, 3, 7, req))
	if !ok || len(body) == 0 {
		t.Fatal("3/7 未处理")
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatalf("3/7 响应不可解码: %v", err)
	}
	if dec.Kind != proto.KindObj || dec.TypeID != boatTicketTypeID {
		t.Fatalf("3/7 应回 type_id=%d 的 obj，实得 kind=%s type=%d class=%s",
			boatTicketTypeID, dec.Kind, dec.TypeID, dec.Class)
	}

	// 未命中：必须回 null，让客户端走"无票"分支而不是异常分支。
	miss := proto.Args524(proto.U32(1), proto.GuidArr([]string{strings.Repeat("f", 32)}))
	body2, ok2 := hCheckTicket(miscCtx(sess, 3, 7, miss))
	if !ok2 {
		t.Fatal("3/7 未命中时应返回 handled")
	}
	if len(body2) != 1 || body2[0] != 0x00 {
		t.Fatalf("未命中应回 null(0x00)，实得 %x", body2)
	}

	// 客户端按 E 时会发 [站点, null guid]（marker 0x17）询问该码头可用的票。
	// 必须按站点回一张真票，否则选票框是空的。
	nullGUID := proto.Args524(proto.U32(1), proto.Guid(strings.Repeat("0", 32), 0x17))
	body3, ok3 := hCheckTicket(miscCtx(sess, 3, 7, nullGUID))
	if !ok3 || len(body3) == 0 {
		t.Fatal("3/7 null guid 未处理")
	}
	dec3, _, err3 := proto.DecodeValue(body3, 0, srv.Table())
	if err3 != nil {
		t.Fatalf("3/7 null guid 响应不可解码: %v", err3)
	}
	if dec3.Kind != proto.KindObj || dec3.TypeID != boatTicketTypeID {
		t.Fatalf("null guid 应按站点回 type_id=%d 的票，实得 kind=%s type=%d",
			boatTicketTypeID, dec3.Kind, dec3.TypeID)
	}
}

func TestOnBoatStateNeedsBoardingEvidence(t *testing.T) {
	st := &FishState{}
	if on, known := st.onBoatLocked(1000); on || known {
		t.Fatal("没见过上船流程时应是未知，不能封鱼")
	}
	st.markBoardLocked(2000)
	if on, known := st.onBoatLocked(1500); !on || !known {
		t.Fatal("船票有效期内应判定为在船上")
	}
	if on, known := st.onBoatLocked(2500); on || !known {
		t.Fatal("船票到期后应判定为已下船")
	}
}
