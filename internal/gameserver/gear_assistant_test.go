package gameserver

import (
	"math"
	"strings"
	"testing"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

func sendGearAssistantForTest(t *testing.T, s *Server, sess *Session, text string) {
	t.Helper()
	ctx := miscCtx(sess, 19, 1, rigRequestTree(t, proto.U32(uint32(gearAssistantUID)), proto.StrVal(text)))
	ctx.ExpectResponse = true
	res := s.dispatch.executeRequest(ctx, true)
	if res.nativeFailure || len(res.body) != 1 || res.body[0] != 0 {
		t.Fatalf("assistant request failed: %q", res.body)
	}
}

func TestGearAssistantCommandsAndPersistence(t *testing.T) {
	env := newDLineTestEnv(t, 1000001)
	if _, err := env.db.EnsureProfile(t.Context(), env.sess.Userid); err != nil {
		t.Fatal(err)
	}
	card := decodeWrappedBytes(t, env.invoke(t, 24, 19, proto.Args524(proto.U32(uint32(gearAssistantUID)))))
	if card.TypeID != 2002 || card.Fields[1].StrOr() != gearAssistantName || card.Fields[3].U32() != 0 {
		t.Fatalf("invalid assistant card: %+v", card)
	}
	for _, tc := range []struct {
		text, reply string
		enabled     bool
	}{
		{"菜单", "满红不爆：关闭", false},
		{"开启", "已开启", true},
		{"开", "已经开启", true},
		{"状态", "满红不爆：开启", true},
		{"开关", "发送「开」", true},
		{"关闭", "已关闭", false},
		{"关", "已经关闭", false},
		{" 开 ", "已开启", true},
	} {
		sendGearAssistantForTest(t, env.srv, env.sess, tc.text)
		on, err := readOverloadProtection(t.Context(), env.db.SQL, env.sess.Userid)
		if err != nil || on != tc.enabled {
			t.Fatalf("%s: enabled=%v err=%v", tc.text, on, err)
		}
		mails, err := env.db.MailList(t.Context(), env.sess.Userid, false)
		if err != nil || len(mails) == 0 || mails[0].Sender != gearAssistantUID || !strings.Contains(mails[0].Text, tc.reply) {
			t.Fatalf("%s: reply=%+v err=%v", tc.text, mails, err)
		}
	}
	// A new session reads the saved state; no session-local switch or real NPC account.
	reconnected := &Session{ID: 2, Userid: env.sess.Userid, Srv: env.srv, log: env.sess.log}
	sendGearAssistantForTest(t, env.srv, reconnected, "状态")
	on, err := readOverloadProtection(t.Context(), env.db.SQL, env.sess.Userid+1)
	if err != nil || on {
		t.Fatalf("setting leaked to another player: %v %v", on, err)
	}
	var profiles int
	if err := env.db.SQL.QueryRow(`SELECT COUNT(*) FROM profile WHERE uid=?`, gearAssistantUID).Scan(&profiles); err != nil || profiles != 0 {
		t.Fatalf("virtual assistant became a player: %d %v", profiles, err)
	}
	seedEconomyItemDef(t, env.db, 9452, "gear_assistant_mail_item", 27)
	item := economyTestItem(t, env.srv, env.sess.Userid, "gear_assistant_mail_item", "backpack", 3)
	env.invoke(t, 19, 2, proto.Args524(proto.U32(uint32(gearAssistantUID)), proto.U32(1), proto.Guid(item.GUID)))
	if got := mustItem(t, env.db, item.GUID); got.UID != env.sess.Userid || got.Qty != 3 {
		t.Fatal("assistant received player items")
	}
}

func TestGearAssistantMailReplayAndIsolation(t *testing.T) {
	s, db := newProgressSrv(t)
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(t.Context(), uid); err != nil {
		t.Fatal(err)
	}
	first := newMailTestSession(t, s, 1, uid)
	second := newMailTestSession(t, s, 2, uid+1)
	first.sess.Token = "gear-assistant-test"
	body, err := proto.EncodeValue(proto.Args524(proto.U32(uint32(gearAssistantUID)), proto.StrVal("开")), s.Table())
	if err != nil {
		t.Fatal(err)
	}
	send := func() {
		s.handleEnvelopePayload(first.sess, proto.BuildRequestEnvelope(701, 19, 1, append(append([]byte(nil), body...), 0)))
		select {
		case response := <-first.envelopes:
			if response.Marker != -2 || response.CallID != 701 || len(response.Body) != 1 || response.Body[0] != 0 {
				t.Fatalf("acknowledgement must precede notification: %+v", response)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no acknowledgement")
		}
	}
	send()
	mails, err := db.MailList(t.Context(), uid, false)
	if err != nil || len(mails) != 1 || mails[0].Sender != gearAssistantUID {
		t.Fatalf("reply not saved: %+v %v", mails, err)
	}
	assertMailTestNotification(t, first, mails[0])
	assertNoMailTestPush(t, second)
	sendGearAssistantForTest(t, s, first.sess, "关")
	send() // Replaying an old "on" must not undo a later "off".
	assertNoMailTestPush(t, first)
	if on, err := readOverloadProtection(t.Context(), db.SQL, uid); err != nil || on {
		t.Fatalf("replayed request overwrote newer setting: %v %v", on, err)
	}
	mails, err = db.MailList(t.Context(), uid, false)
	if err != nil || len(mails) != 2 {
		t.Fatalf("replay duplicated mail: %d %v", len(mails), err)
	}
}

func TestGearAssistantRollbackAndMalformedSettings(t *testing.T) {
	env := newDLineTestEnv(t, 1000001)
	if _, err := env.db.SQL.Exec(`CREATE TRIGGER reject_gear_reply BEFORE INSERT ON mail BEGIN SELECT RAISE(ABORT,'test mail failure'); END`); err != nil {
		t.Fatal(err)
	}
	ctx := env.ctx(19, 1, proto.Args524(proto.U32(uint32(gearAssistantUID)), proto.StrVal("开")))
	result := env.srv.dispatch.executeRequest(ctx, true)
	if !result.nativeFailure || len(ctx.afterResponse) != 0 {
		t.Fatal("failed save reported success or scheduled a reply")
	}
	if on, err := readOverloadProtection(t.Context(), env.db.SQL, env.sess.Userid); err != nil || on {
		t.Fatalf("reply failure did not roll back the setting: %v %v", on, err)
	}
	if _, err := env.db.SQL.Exec(`DROP TRIGGER reject_gear_reply`); err != nil {
		t.Fatal(err)
	}
	ctx = env.ctx(19, 1, proto.Args524(proto.U32(uint32(gearAssistantUID)), proto.StrVal("开")))
	ctx.ExpectResponse = false
	hMailSendMessage(ctx)
	if on, _ := readOverloadProtection(t.Context(), env.db.SQL, env.sess.Userid); on {
		t.Fatal("fire-and-forget message changed the setting")
	}
	if err := env.db.KVSet(t.Context(), overloadProtectionKey(env.sess.Userid), "invalid"); err != nil {
		t.Fatal(err)
	}
	if _, err := readOverloadProtection(t.Context(), env.db.SQL, env.sess.Userid); err == nil {
		t.Fatal("corrupt setting was silently interpreted as disabled")
	}
}

func TestGearAssistantProtectsThreeRodsAndKeepsManualActions(t *testing.T) {
	s, db := newRigTestServer(t)
	sess := newProbeSess(s)
	uid := sess.Userid
	sendGearAssistantForTest(t, s, sess, "开")
	var rods, lines, hooks []string
	for i := 0; i < 3; i++ {
		created := make([]*store.Item, 0, 4)
		for _, itemID := range []string{"spin_10001_S60L", "spin_5820", "mono_15249", "orig_20008_18"} {
			row, err := s.CreateItem(t.Context(), uid, itemID, "backpack", 1)
			if err != nil {
				t.Fatal(err)
			}
			created = append(created, row)
		}
		rod := created[0].GUID
		for _, item := range created[1:] {
			mountGearForTest(t, s, sess, rod, item.GUID)
		}
		slots := rigSlotGUIDs(t, s, rod)
		rods, lines, hooks = append(rods, rod), append(lines, slots[catLine]), append(hooks, slots[catHook])
		st := s.fishingStateForRod(uid, rod)
		fish := &RolledFish{FishGUID: newFishGUID(), Species: "perch", Grade: 1, WeightG: 123, LengthM: .2}
		st.mu.Lock()
		st.GearGuid, st.Pending = rod, fish
		st.CastActive, st.InWater, st.Hooked = true, true, true
		st.mu.Unlock()
		var before []store.Item
		for _, guid := range []string{rod, slots[gearCatReel], slots[catLine], slots[catHook]} {
			before = append(before, *mustItem(t, db, guid))
		}
		for _, item := range before {
			ctx := miscCtx(sess, 17, 9, rigRequestTree(t, proto.Guid(rod), proto.Guid(item.GUID)))
			ctx.ExpectResponse = true
			result := s.dispatch.executeRequest(ctx, true)
			if !result.nativeFailure || !strings.Contains(string(result.body), "System.OperationCanceledException") || len(ctx.afterResponse) != 0 {
				t.Fatalf("rod %d: break was not cancelled without equipment push", i)
			}
		}
		for _, item := range before {
			if after := *mustItem(t, db, item.GUID); after != item {
				t.Fatalf("protected %s changed", item.ItemID)
			}
		}
		st.mu.Lock()
		unchanged := st.Pending == fish && st.Hooked && st.CastActive && !st.Resolved && st.pendingTimer == nil && st.reelWear.BreakHits == 0
		st.mu.Unlock()
		if !unchanged {
			t.Fatal("protected break changed fish identity, timers or wear ledger")
		}
	}
	logs, err := db.ListItemDamageLogs(t.Context(), uid, 20)
	if err != nil || len(logs) != 0 {
		t.Fatalf("protected break wrote damage logs: %+v %v", logs, err)
	}
	if err := db.KVSet(t.Context(), overloadProtectionKey(uid+1), "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.breakLineOrRod(t.Context(), uid+1, rods[0], hooks[0]); err == nil {
		t.Fatal("protection bypassed ownership checks")
	}
	if _, err := s.breakLineOrRod(t.Context(), uid, rods[0], hooks[1]); err == nil {
		t.Fatal("protection accepted a component mounted on another rod")
	}
	sendGearAssistantForTest(t, s, sess, "关")
	out, err := s.breakLineOrRod(t.Context(), uid, rods[0], hooks[0])
	if err != nil || out.OverloadProtected || !out.ComponentBroken {
		t.Fatalf("disabled protection did not restore break: %+v %v", out, err)
	}
	sendGearAssistantForTest(t, s, sess, "开")
	beforeLength := s.itemLength(t.Context(), mustItem(t, db, lines[1]))
	out, err = s.cutLineTx(t.Context(), uid, rods[1], 1)
	if err != nil || out.OverloadProtected || s.itemLength(t.Context(), mustItem(t, db, lines[1])) != beforeLength-1 {
		t.Fatalf("protection prevented intentional line cut: %+v %v", out, err)
	}
	st := s.selectFishingRod(uid, rods[2])
	st.mu.Lock()
	st.InWater = false // Do not start a new bite timer in this departure check.
	st.mu.Unlock()
	left := miscCtx(sess, 14, 12, proto.Args524(proto.Obj("lhicjpmglbo", 24, proto.Field{Op: "U32", Num: ptrU64(1)})))
	hFishingLeft(left)
	st.mu.Lock()
	escaped := st.Pending == nil && !st.Hooked && st.pendingTimer == nil
	st.mu.Unlock()
	if !escaped {
		t.Fatal("equipment protection prevented natural fish departure or replayed it")
	}
}

func TestGearAssistantOverloadAccountingAndNormalWear(t *testing.T) {
	s, db := newRigTestServer(t)
	sess := newProbeSess(s)
	uid := sess.Userid
	rod, err := s.CreateItem(t.Context(), uid, "spin_10001_S60L", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	reel, err := s.CreateItem(t.Context(), uid, "spin_5820", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	mountReelOnRod(t, s, rod.GUID, reel.GUID)
	fish := &RolledFish{FishGUID: newFishGUID(), Species: "perch", WeightG: 4000}
	st := s.selectFishingRod(uid, rod.GUID)
	st.mu.Lock()
	st.GearGuid, st.Pending, st.CastActive, st.Hooked = rod.GUID, fish, true, true
	st.reelWear = newFightWearForTest(7.7, 5, 7.7, 11.55, 1, 1, true)
	st.reelWear.rodGUID, st.reelWear.reelGUID, st.reelWear.fishGUID = rod.GUID, reel.GUID, fish.FishGUID
	st.reelWear.BaseCharged, st.reelWear.Used = true, true
	st.reelWear.BurnWear, st.reelWear.BurnSecs, st.reelWear.SegSecs = .05, 1, 1
	st.mu.Unlock()
	sendGearAssistantForTest(t, s, sess, "开")
	load := miscCtx(sess, 14, 8, nil)
	for i := 0; i < 30; i++ {
		st.mu.Lock()
		st.reelWear.lastLoadAt = time.Now().Add(-5 * time.Second)
		st.mu.Unlock()
		s.recordFightReelLoad(load)
	}
	st.mu.Lock()
	w := st.reelWear
	st.mu.Unlock()
	if w.BurnWear != .05 || w.BurnSecs != 1 || w.SegSecs != 0 || !w.BaseCharged {
		t.Fatalf("protected overload added wear or removed previous wear: %+v", w)
	}
	// A long protected interval without load frames must not be caught up on close.
	st.mu.Lock()
	st.reelWear.lastLoadAt = time.Now().Add(-time.Hour)
	st.mu.Unlock()
	sendGearAssistantForTest(t, s, sess, "关")
	st.mu.Lock()
	if time.Since(st.reelWear.lastLoadAt) > time.Second || st.reelWear.SegSecs != 0 {
		st.mu.Unlock()
		t.Fatal("switch did not reset protected duration")
	}
	st.reelWear.lastLoadAt = time.Now().Add(-2 * time.Second)
	st.reelWear.protectionChangedAt = st.reelWear.lastLoadAt // Advance both boundaries for the simulated two seconds.
	st.mu.Unlock()
	s.recordFightReelLoad(load)
	st.mu.Lock()
	afterOff := st.reelWear
	st.mu.Unlock()
	if afterOff.BurnWear <= w.BurnWear || afterOff.BurnSecs <= w.BurnSecs {
		t.Fatal("closing protection did not resume locked overload")
	}
	sendGearAssistantForTest(t, s, sess, "开")
	st.mu.Lock()
	st.reelWear.locked = false
	st.reelWear.lastLoadAt = time.Now().Add(-2 * time.Second)
	st.reelWear.protectionChangedAt = st.reelWear.lastLoadAt
	st.mu.Unlock()
	s.recordFightReelLoad(load)
	st.mu.Lock()
	afterSlip := st.reelWear
	st.mu.Unlock()
	if afterSlip.SlipWear <= 0 || afterSlip.SlipTicks != 1 || afterSlip.BurnWear != afterOff.BurnWear {
		t.Fatal("protection disabled ordinary slip wear")
	}
	before := *mustItem(t, db, reel.GUID)
	s.settleFightReelWearState(t.Context(), st, uid, "14/4")
	after := *mustItem(t, db, reel.GUID)
	if after.Durability >= before.Durability {
		t.Fatal("protection erased incurred damage and ordinary use wear")
	}
	s.settleFightReelWearState(t.Context(), st, uid, "14/4")
	if again := *mustItem(t, db, reel.GUID); again != after {
		t.Fatal("duplicate settlement charged wear again")
	}
}

func TestGearAssistantFreshCacheRespectsSwitchTime(t *testing.T) {
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	w := fightReelWear{protectionChangedAt: now.Add(-150 * time.Millisecond)}
	if got := w.loadTickSecondsLocked(now, 1.9, 5); math.Abs(got-.15) > 1e-9 {
		t.Fatalf("fresh strength cache billed time before switch: %v", got)
	}
	w.lastLoadAt = now.Add(-time.Hour)
	if got := w.loadTickSecondsLocked(now, 1.9, 5); math.Abs(got-.15) > 1e-9 {
		t.Fatalf("old sample billed protected time: %v", got)
	}
	w.protectionChangedAt = now.Add(time.Second)
	if got := w.loadTickSecondsLocked(now, 1.9, 5); got != 0 {
		t.Fatalf("clock rollback created wear: %v", got)
	}
}
