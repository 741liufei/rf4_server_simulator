package gameserver

// 17/9 断件上报的边界：空 GUID / zero GUID 表示“客户端没有给出具体断件”，
// 应回退到最弱环裁决；只有明确上报竿 GUID 时才直接断竿。

import (
	"context"
	"testing"
)

func mountWeakLineRig(t *testing.T, srv *Server) (context.Context, int64, string, string, string) {
	t.Helper()
	ctx := context.Background()
	const uid = int64(1000001)
	rod, err := srv.CreateItem(ctx, uid, "tele_10180_4", "backpack", 1)
	if err != nil {
		t.Fatalf("创建竿失败: %v", err)
	}
	// mono_15095 线 5.0kg，远弱于竿和 c_hook_20748（48.72kg）。
	line, err := srv.CreateItem(ctx, uid, "mono_15095", "backpack", 1)
	if err != nil {
		t.Fatalf("创建主线失败: %v", err)
	}
	hook, err := srv.CreateItem(ctx, uid, "c_hook_20748", "backpack", 1)
	if err != nil {
		t.Fatalf("创建钩失败: %v", err)
	}
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, hook.GUID)
	return ctx, uid, rod.GUID, line.GUID, hook.GUID
}

func TestBreakReportMissingOrZeroGUIDFallsBackToWeakestLink(t *testing.T) {
	for _, tc := range []struct {
		name     string
		reported string
	}{
		{name: "empty", reported: ""},
		{name: "zero", reported: zeroGUID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, db := newRigTestServer(t)
			ctx, uid, rodGUID, lineGUID, _ := mountWeakLineRig(t, srv)

			out, err := srv.breakLineOrRod(ctx, uid, rodGUID, tc.reported)
			if err != nil {
				t.Fatalf("空/zero 上报不应报错: %v", err)
			}
			if !out.LineBroken || out.BreakPart != "line" || out.BrokenGUID != lineGUID {
				t.Fatalf("空/zero 上报应按最弱环断主线: %+v", out)
			}
			// 官服清杯口径：主线断=整条销毁，不再只扣一段长度。
			if row, _ := db.GetItemByGUID(ctx, lineGUID); row != nil {
				t.Fatalf("断主线后主线应整条销毁，实得 %+v", row)
			}
			row, err := db.GetItemByGUID(ctx, rodGUID)
			if err != nil || row == nil || row.Durability <= 0 {
				t.Fatalf("回退最弱环不应断竿: row=%+v err=%v", row, err)
			}
		})
	}
}

func TestBreakReportRodGUIDBreaksRod(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx, uid, rodGUID, _, _ := mountWeakLineRig(t, srv)

	out, err := srv.breakLineOrRod(ctx, uid, rodGUID, rodGUID)
	if err != nil {
		t.Fatalf("明确上报竿断不应报错: %v", err)
	}
	if out.BreakPart != "rod" || out.BrokenGUID != rodGUID {
		t.Fatalf("明确上报竿 GUID 应断竿: %+v", out)
	}
	row, err := db.GetItemByGUID(ctx, rodGUID)
	if err != nil || row == nil || row.Durability > 0 {
		t.Fatalf("断竿耐久应归 0: row=%+v err=%v", row, err)
	}
}
