package gameserver

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// 14/12 顶层 args 为 [竿 GUID, 鱼 GUID, 状态对象]。
func leftTree(rod, fish string, f0 uint64) *proto.Value {
	items := []*proto.Value{}
	if rod != "" {
		items = append(items, proto.Guid(rod))
	}
	if fish != "" {
		items = append(items, proto.Guid(fish))
	}
	items = append(items, proto.Obj("lhicjpmglbo", 0,
		proto.Field{Op: "U32", Num: ptrU64(f0)}))
	return proto.Args524(items...)
}

// settledFish 走真实结算产生回执，再把 FishState 摆成「14/4 已结算、
// 等待 14/5/14/6」的窗口态（与 fishingEndSettle 结束后的状态一致）。
func settledFish(t *testing.T, s *Server, uid int64, guid string) *RolledFish {
	t.Helper()
	fish := &RolledFish{FishGUID: guid, Species: "perch", Grade: 1, WeightG: 640}
	if _, err := s.settleCatchOnce(t.Context(), uid, fish, 1); err != nil {
		t.Fatalf("结算失败: %v", err)
	}
	st := s.stateFor(uid)
	st.mu.Lock()
	st.Pending, st.Hooked = fish, true
	st.CastActive, st.Resolved = false, false
	st.mu.Unlock()
	return fish
}

func counterValue(t *testing.T, db *store.DB, uid int64, key string) int64 {
	t.Helper()
	var v int64
	err := db.SQL.QueryRowContext(t.Context(),
		`SELECT COALESCE((SELECT value FROM player_counters WHERE uid=? AND key=?),0)`,
		uid, key).Scan(&v)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// 用例 1：已结算待处理窗口内，14/12 的 f0=2/7/3 不得清鱼、不得计 escapes；
// 随后 14/5 仍能正常入护。
func TestSettledCatchSurvivesLeftReports(t *testing.T) {
	t.Parallel()
	s, db := newRigTestServer(t)
	sess := newProbeSess(s)
	uid := sess.Userid
	if _, err := db.EnsureProfile(t.Context(), uid); err != nil {
		t.Fatal(err)
	}
	fish := settledFish(t, s, uid, strings.Repeat("5", 32))
	rod := strings.Repeat("a", 32)

	escapesBefore := counterValue(t, db, uid, "escapes")
	for _, f0 := range []uint64{2, 7, 3} {
		if _, ok := hFishingLeft(miscCtx(sess, 14, 12, leftTree(rod, fish.FishGUID, f0))); !ok {
			t.Fatalf("14/12 f0=%d 未处理", f0)
		}
		st := s.stateFor(uid)
		st.mu.Lock()
		kept := st.Pending == fish && st.Hooked
		st.mu.Unlock()
		if !kept {
			t.Fatalf("f0=%d 清掉了已结算渔获", f0)
		}
	}
	if got := counterValue(t, db, uid, "escapes"); got != escapesBefore {
		t.Fatalf("守门窗口内 escapes 计数变化: %d -> %d", escapesBefore, got)
	}

	capture := &ctxReplyCapture{}
	if _, handled := s.fishingKeep(&Ctx{Sess: sess, Tree: proto.Args524(proto.Guid(rod), proto.Guid(fish.FishGUID)),
		ExpectResponse: true, replyCapture: capture, Log: logging.Discard()}); !handled || capture.failure {
		t.Fatalf("入护失败: handled=%v failure=%v", handled, capture.failure)
	}
	var count int
	if err := db.SQL.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM keepnet_items WHERE echo_guid=?`, fish.FishGUID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("护网条目数=%d err=%v", count, err)
	}
}

// 用例 2：已结算待处理窗口内，17/9 不动装备、不清鱼，回零断件 11028
// 且形状过契约校验；随后 14/5 仍能正常入护。
func TestSettledCatchSurvivesRodBreakReport(t *testing.T) {
	t.Parallel()
	s, db := newRigTestServer(t)
	sess := newProbeSess(s)
	uid := sess.Userid
	ctx := t.Context()
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	rod, err := s.CreateItem(ctx, uid, "spin_10001_S60L", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	fish := settledFish(t, s, uid, strings.Repeat("5", 32))

	capture := &ctxReplyCapture{}
	body, handled := hDynamicRodBreak(&Ctx{Sess: sess, Tree: proto.Args524(proto.Guid(rod.GUID), proto.Guid(rod.GUID)),
		ExpectResponse: true, replyCapture: capture, DecodeOK: true, Log: logging.Discard()})
	if !handled || capture.failure || body == nil {
		t.Fatalf("17/9 未回零断件结果: handled=%v failure=%v body=%d", handled, capture.failure, len(body))
	}
	if reason := s.dispatch.checkRegisteredResponse(17, 9, body, s.Table()); reason != "" {
		t.Fatalf("零断件 11028 未过契约校验: %s", reason)
	}
	if row, _ := db.GetItemByGUID(ctx, rod.GUID); row == nil || row.Durability != 1 {
		t.Fatalf("守门窗口内 17/9 不得动装备耐久: %+v", row)
	}
	st := s.stateFor(uid)
	st.mu.Lock()
	kept := st.Pending == fish && st.Hooked
	st.mu.Unlock()
	if !kept {
		t.Fatal("17/9 清掉了已结算渔获")
	}

	if _, handled := s.fishingKeep(&Ctx{Sess: sess, Tree: proto.Args524(proto.Guid(rod.GUID), proto.Guid(fish.FishGUID)),
		ExpectResponse: true, replyCapture: &ctxReplyCapture{}, Log: logging.Discard()}); !handled {
		t.Fatal("14/5 未处理")
	}
	var count int
	if err := db.SQL.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM keepnet_items WHERE echo_guid=?`, fish.FishGUID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("护网条目数=%d err=%v", count, err)
	}
}

// 用例 3+4：搏鱼中（CastActive=true）语义不变——GUID 匹配的 f0=2 仍按
// 真脱钩清鱼并计 escapes；GUID 不一致的陈旧帧不得清当前鱼。
func TestMidFightLeftSemanticsUnchanged(t *testing.T) {
	t.Parallel()
	s, db := newRigTestServer(t)
	sess := newProbeSess(s)
	uid := sess.Userid
	if _, err := db.EnsureProfile(t.Context(), uid); err != nil {
		t.Fatal(err)
	}
	rod := strings.Repeat("a", 32)

	// GUID 匹配：现有语义。
	fishA := &RolledFish{FishGUID: strings.Repeat("5", 32), Species: "perch", WeightG: 640}
	st := s.stateFor(uid)
	st.mu.Lock()
	st.CastActive, st.Pending, st.Hooked = true, fishA, true
	st.mu.Unlock()
	escapesBefore := counterValue(t, db, uid, "escapes")
	if _, ok := hFishingLeft(miscCtx(sess, 14, 12, leftTree(rod, fishA.FishGUID, 2))); !ok {
		t.Fatal("14/12 未处理")
	}
	st.mu.Lock()
	cleared := st.Pending == nil && !st.Hooked
	st.mu.Unlock()
	if !cleared {
		t.Fatal("搏鱼中 GUID 匹配的 f0=2 应按现有语义清鱼")
	}
	if got := counterValue(t, db, uid, "escapes"); got != escapesBefore+1 {
		t.Fatalf("搏鱼中跑鱼应计 escapes: %d -> %d", escapesBefore, got)
	}

	// GUID 错配：陈旧帧不得清当前鱼。
	fishB := &RolledFish{FishGUID: strings.Repeat("6", 32), Species: "perch", WeightG: 640}
	st.mu.Lock()
	st.CastActive, st.Pending, st.Hooked = true, fishB, true
	st.mu.Unlock()
	if _, ok := hFishingLeft(miscCtx(sess, 14, 12, leftTree(rod, fishA.FishGUID, 2))); !ok {
		t.Fatal("14/12 未处理")
	}
	st.mu.Lock()
	kept := st.Pending == fishB && st.Hooked
	st.mu.Unlock()
	if !kept {
		t.Fatal("GUID 错配的陈旧 14/12 清掉了正在搏斗的鱼")
	}
}

// 用例 3（17/9 侧）：搏鱼中 17/9 不被守门拦截，仍进入断件处理路径
// （此处钓组为空，breakLineOrRod 以失败回包证明走到了裁决）。
func TestMidFightRodBreakNotGuarded(t *testing.T) {
	t.Parallel()
	s, db := newRigTestServer(t)
	sess := newProbeSess(s)
	uid := sess.Userid
	ctx := context.Background()
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	rod, err := s.CreateItem(ctx, uid, "spin_10001_S60L", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	fish := &RolledFish{FishGUID: strings.Repeat("5", 32), Species: "perch", WeightG: 640}
	st := s.stateFor(uid)
	st.mu.Lock()
	st.CastActive, st.Pending, st.Hooked = true, fish, true
	st.GearGuid = rod.GUID
	st.mu.Unlock()

	capture := &ctxReplyCapture{}
	if _, handled := hDynamicRodBreak(&Ctx{Sess: sess, Tree: proto.Args524(proto.Guid(rod.GUID), proto.Guid(rod.GUID)),
		ExpectResponse: true, replyCapture: capture, DecodeOK: true, Log: logging.Discard()}); !handled {
		t.Fatal("17/9 未处理")
	}
	// 空钓组的最弱环就是竿本身：守门未拦时走真实断竿——竿耐久归零、
	// 鱼按现有语义清掉。守门路径绝不会产生这两个副作用。
	if row, _ := db.GetItemByGUID(ctx, rod.GUID); row == nil || row.Durability != 0 {
		t.Fatalf("搏鱼中 17/9 应走真实断件（竿耐久应归零）: %+v", row)
	}
	st.mu.Lock()
	cleared := st.Pending == nil
	st.mu.Unlock()
	if !cleared {
		t.Fatal("搏鱼中 17/9 应按现有语义清鱼")
	}
}

// 用例 5：护网满导致窗口拉长时鱼同样保留；腾出容量后 14/5 成功。
func TestSettledCatchSurvivesWhenKeepnetFull(t *testing.T) {
	t.Parallel()
	s, db := newRigTestServer(t)
	sess := newProbeSess(s)
	uid := sess.Userid
	ctx := t.Context()
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	// 导入快照把护网容量压到 1（containerKeepnet=5）。
	if err := db.KVSet(ctx, fmt.Sprintf("native-import:%d:inventory", uid),
		`{"countsByKind":{"5":{"A":1,"B":1}}}`); err != nil {
		t.Fatal(err)
	}
	rod := strings.Repeat("a", 32)

	// 先占满 1 格。
	filler := settledFish(t, s, uid, strings.Repeat("7", 32))
	if _, handled := s.fishingKeep(&Ctx{Sess: sess, Tree: proto.Args524(proto.Guid(rod), proto.Guid(filler.FishGUID)),
		ExpectResponse: true, replyCapture: &ctxReplyCapture{}, Log: logging.Discard()}); !handled {
		t.Fatal("占位入护未处理")
	}

	fish := settledFish(t, s, uid, strings.Repeat("5", 32))
	if _, ok := hFishingLeft(miscCtx(sess, 14, 12, leftTree(rod, fish.FishGUID, 2))); !ok {
		t.Fatal("14/12 未处理")
	}
	st := s.stateFor(uid)
	st.mu.Lock()
	kept := st.Pending == fish && st.Hooked
	st.mu.Unlock()
	if !kept {
		t.Fatal("护网满窗口内 14/12 清掉了渔获")
	}

	// 腾出容量后入护成功。
	if _, err := db.SQL.ExecContext(ctx, `DELETE FROM keepnet_items WHERE echo_guid=?`, filler.FishGUID); err != nil {
		t.Fatal(err)
	}
	if _, handled := s.fishingKeep(&Ctx{Sess: sess, Tree: proto.Args524(proto.Guid(rod), proto.Guid(fish.FishGUID)),
		ExpectResponse: true, replyCapture: &ctxReplyCapture{}, Log: logging.Discard()}); !handled {
		t.Fatal("14/5 未处理")
	}
	var count int
	if err := db.SQL.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM keepnet_items WHERE echo_guid=?`, fish.FishGUID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("护网条目数=%d err=%v", count, err)
	}
}
