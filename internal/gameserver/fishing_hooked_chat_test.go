package gameserver

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/tunnel"
)

func collectHookedChat(peer *tunnel.Conn) <-chan *proto.Envelope {
	ch := make(chan *proto.Envelope, 32)
	go func() {
		defer close(ch)
		for {
			_, _, payload, err := peer.ReadRecord()
			if err != nil {
				return
			}
			if env, ok := proto.ParseEnvelope(payload); ok && env.Main == 24 && env.Sub == 27 {
				ch <- env
			}
		}
	}()
	return ch
}

// A final push on the same connection bounds the assertion without a sleep.
func finishHookedChat(t *testing.T, sess *Session, ch <-chan *proto.Envelope) []string {
	t.Helper()
	const end = "hooked-chat-test-end"
	if sess.Push(24, 27, buildChatEchoPush("", end, uint64(sess.Userid), 1)) == 0 {
		t.Fatal("could not send final chat marker")
	}
	var texts []string
	timeout := time.After(3 * time.Second)
	for {
		select {
		case env, ok := <-ch:
			if !ok {
				t.Fatal("chat connection closed")
			}
			v, _, err := proto.DecodeValue(env.Body, 0, sess.Srv.Table())
			if err != nil || v.ArgCount() != 3 || v.ArgItem(0).Class != "lfbdbaffeai" || v.ArgItem(1).StrOr() != "" {
				t.Fatalf("invalid 24/27 chat: %v %+v", err, v)
			}
			field := v.ArgItem(0).Field(1)
			text := field.StrOr()
			if text == end {
				return texts
			}
			texts = append(texts, text)
		case <-timeout:
			t.Fatal("chat marker timed out")
		}
	}
}

func TestHookedChatUsesEachRodsShortcutAndFishOnce(t *testing.T) {
	s, db := newRigTestServer(t)
	t.Cleanup(func() { stopFishingRecoveryTimers(s) })
	setWeatherTestTunable(t, s, "fish_hooked_chat", "1")
	sess, peer := newTunnelProbe(t, s)
	ch := collectHookedChat(peer)
	other, otherPeer := newTunnelProbe(t, s)
	other.ID, other.Userid = 78, 1000002
	otherCh := collectHookedChat(otherPeer)
	s.sessions[other.ID] = other
	if _, err := db.SQL.Exec(`INSERT INTO loc_wiki_fish(loc,fish_key,fish_zh)
		VALUES('chat-test','chat-a','鲈鱼'),('chat-test','chat-b','狗鱼'),('chat-test','chat-c','鲤鱼')`); err != nil {
		t.Fatal(err)
	}
	var rods []string
	var fish []*RolledFish
	for i, slot := range []int64{3, 1, 2} {
		rod, err := s.CreateItem(t.Context(), sess.Userid, "tele_10180_4", "backpack", 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.BindShortcut(t.Context(), sess.Userid, slot, rod.GUID); err != nil {
			t.Fatal(err)
		}
		f := &RolledFish{FishGUID: newFishGUID(), GearGUID: rod.GUID, Species: []string{"chat-a", "chat-b", "chat-c"}[i],
			WeightG: []int64{1234, 567, 23456}[i], Grade: 2}
		st := s.selectFishingRod(sess.Userid, rod.GUID)
		st.mu.Lock()
		st.Pending, st.CastActive, st.InWater = f, true, true
		st.mu.Unlock()
		rods, fish = append(rods, rod.GUID), append(fish, f)
	}
	if err := db.BindShortcut(t.Context(), sess.Userid, 5, rods[0]); err != nil {
		t.Fatal(err)
	}
	for i, rod := range rods {
		ctx := miscCtx(sess, 14, 11, rigRequestTree(t, proto.Guid(rod), proto.Guid(fish[i].FishGUID)))
		hFishingHooked(ctx)
		hFishingHooked(ctx)
	}
	want := []string{
		"[上钩] 快捷栏3号竿：鲈鱼 1.234kg",
		"[上钩] 快捷栏1号竿：狗鱼 0.567kg",
		"[上钩] 快捷栏2号竿：鲤鱼 23.456kg",
	}
	if got := finishHookedChat(t, sess, ch); !slices.Equal(got, want) {
		t.Fatalf("chat = %q, want %q", got, want)
	}
	if got := finishHookedChat(t, other, otherCh); len(got) != 0 {
		t.Fatalf("another player received private fishing details: %q", got)
	}
	if s.fishingState(sess.Userid).GearGuid != rods[2] {
		t.Fatal("chat changed the held rod")
	}
}

func TestHookedChatMarinePullAndOrdinaryPull(t *testing.T) {
	s, db := newRigTestServer(t)
	t.Cleanup(func() { stopFishingRecoveryTimers(s) })
	setWeatherTestTunable(t, s, "fish_hooked_chat", "1")
	sess, peer := newTunnelProbe(t, s)
	ch := collectHookedChat(peer)
	rod, err := s.CreateItem(t.Context(), sess.Userid, "tele_10180_4", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO rig(guid,uid,rig_type,name) VALUES(?,?,50,'海竿')`, rod.GUID, sess.Userid); err != nil {
		t.Fatal(err)
	}
	st := s.selectFishingRod(sess.Userid, rod.GUID)
	st.mu.Lock()
	st.RigType, st.CastActive = 50, true
	st.Pending = &RolledFish{FishGUID: newFishGUID(), Species: "unknown-fish", WeightG: 90123}
	st.mu.Unlock()
	ctx := miscCtx(sess, 14, 9, rigRequestTree(t, proto.Guid(rod.GUID)))
	hFishingPull(ctx)
	hFishingPull(ctx)
	hFishingHooked(ctx)
	want := "[上钩] 海竿（" + rod.GUID[:8] + "）：unknown-fish 90.123kg"
	if got := finishHookedChat(t, sess, ch); !slices.Equal(got, []string{want}) {
		t.Fatalf("marine hook chat = %q, want %q", got, want)
	}
	st.mu.Lock()
	st.RigType, st.Hooked = 2, false
	st.Pending = &RolledFish{FishGUID: newFishGUID(), Species: "unknown-fish", WeightG: 1000}
	st.mu.Unlock()
	hFishingPull(ctx)
	if got := finishHookedChat(t, sess, ch); len(got) != 0 {
		t.Fatalf("ordinary pull is not a hook confirmation: %q", got)
	}
	hFishingHooked(ctx)
	want = "[上钩] 海竿（" + rod.GUID[:8] + "）：unknown-fish 1.000kg"
	if got := finishHookedChat(t, sess, ch); !slices.Equal(got, []string{want}) {
		t.Fatalf("next fish chat = %q, want %q", got, want)
	}
}

func TestHookedChatDisabledAndInvalidEvents(t *testing.T) {
	s, _ := newRigTestServer(t)
	t.Cleanup(func() { stopFishingRecoveryTimers(s) })
	sess, peer := newTunnelProbe(t, s)
	ch := collectHookedChat(peer)
	st := s.fishingStateForRod(sess.Userid, "11111111111111111111111111111111")
	fish := &RolledFish{FishGUID: newFishGUID(), Species: "perch", WeightG: 1234}
	st.mu.Lock()
	st.Pending, st.CastActive = fish, true
	st.mu.Unlock()
	ctx := miscCtx(sess, 14, 11, rigRequestTree(t, proto.Guid(st.GearGuid)))
	hFishingHooked(ctx) // Default is off.
	setWeatherTestTunable(t, s, "fish_hooked_chat", "1")
	for _, scenario := range []struct {
		pending  *RolledFish
		active   bool
		resolved bool
	}{
		{nil, false, false},
		{fish, false, false},
		{fish, true, true},
	} {
		st.mu.Lock()
		st.Pending, st.CastActive, st.Resolved, st.Hooked = scenario.pending, scenario.active, scenario.resolved, false
		st.mu.Unlock()
		hFishingHooked(ctx)
	}
	if got := finishHookedChat(t, sess, ch); len(got) != 0 {
		t.Fatalf("disabled or invalid hook was announced: %q", got)
	}
}

func TestHookedChatRejectedFishRepushOnce(t *testing.T) {
	for _, rigType := range []int64{2, 50} {
		for _, status := range []uint64{3, 5} {
			t.Run(fmt.Sprintf("rig%d/status%d", rigType, status), func(t *testing.T) {
				s, _ := newRigTestServer(t)
				t.Cleanup(func() { stopFishingRecoveryTimers(s) })
				setWeatherTestTunable(t, s, "fish_hooked_chat", "1")
				sess, peer := newTunnelProbe(t, s)
				s.sessions[sess.ID] = sess
				ch := collectHookedChat(peer)
				rod := "11111111111111111111111111111111"
				fish := &RolledFish{FishGUID: newFishGUID(), Species: "perch", WeightG: 1234}
				st := s.fishingStateForRod(sess.Userid, rod)
				st.mu.Lock()
				st.RigType, st.Pending, st.CastActive, st.InWater = rigType, fish, true, true
				st.persist.sessionID = sess.ID
				st.mu.Unlock()
				hook := func() {
					if marineRigType(rigType) {
						hFishingPull(miscCtx(sess, 14, 9, rigRequestTree(t, proto.Guid(rod))))
					} else {
						hFishingHooked(miscCtx(sess, 14, 11, rigRequestTree(t, proto.Guid(rod))))
					}
				}
				hook()
				left := proto.Args524(proto.Guid(rod), proto.Obj("lhicjpmglbo", 0,
					proto.Field{Op: "U32", Num: ptrU64(status)}))
				hFishingLeft(miscCtx(sess, 14, 12, left))
				st.mu.Lock()
				if st.Pending != fish || st.Hooked || st.pendingTimer == nil {
					st.mu.Unlock()
					t.Fatal("rejection did not retain the fish for repush")
				}
				st.pendingTimer.Stop()
				ticket := fishingTimerTicket{st.castGeneration, st.pendingTicket}
				st.mu.Unlock()
				s.repushRodPendingTicket(sess.Userid, st, ticket, fish)
				st.mu.Lock()
				repushed := st.PushSent && st.Pending == fish
				st.mu.Unlock()
				if !repushed {
					t.Fatal("the original fish was not repushed")
				}
				hook()
				hFishingHooked(miscCtx(sess, 14, 11, rigRequestTree(t, proto.Guid(rod))))
				want := "[上钩] 鱼竿（11111111）：perch 1.234kg"
				if got := finishHookedChat(t, sess, ch); !slices.Equal(got, []string{want}) {
					t.Fatalf("repushed fish chat = %q, want one %q", got, want)
				}
			})
		}
	}
}

func TestHookedChatEarlyConfirmation(t *testing.T) {
	for _, rigType := range []int64{2, 50} {
		t.Run(fmt.Sprintf("rig%d", rigType), func(t *testing.T) {
			s, _ := newRigTestServer(t)
			t.Cleanup(func() { stopFishingRecoveryTimers(s) })
			setWeatherTestTunable(t, s, "fish_hooked_chat", "1")
			sess, peer := newTunnelProbe(t, s)
			s.sessions[sess.ID] = sess
			ch := collectHookedChat(peer)
			rod := "11111111111111111111111111111111"
			fish := &RolledFish{FishGUID: newFishGUID(), Species: "perch", WeightG: 1234}
			st := s.fishingStateForRod(sess.Userid, rod)
			st.mu.Lock()
			st.RigType, st.CastActive, st.InWater = rigType, true, true
			st.CastTs, st.Armed = time.Now().Add(-20*time.Second), fish
			st.persist.sessionID = sess.ID
			st.mu.Unlock()
			ctx := miscCtx(sess, 14, 11, rigRequestTree(t, proto.Guid(rod)))
			hFishingHooked(ctx)
			st.mu.Lock()
			if !st.Hooked || st.Pending != nil || st.biteTimer == nil {
				st.mu.Unlock()
				t.Fatal("early confirmation did not schedule the missing fish")
			}
			st.cancelBiteTimerLocked()
			ticket := fishingTimerTicket{st.castGeneration, st.biteTicket}
			st.mu.Unlock()
			if got := finishHookedChat(t, sess, ch); len(got) != 0 {
				t.Fatalf("early confirmation without a fish was announced: %q", got)
			}
			s.onRodBiteTimerTicket(sess.Userid, st, ticket)
			st.mu.Lock()
			generated := st.Pending == fish && st.Hooked && st.PushSent
			st.mu.Unlock()
			if !generated {
				t.Fatal("bite did not generate and push the fish while preserving Hooked")
			}
			if marineRigType(rigType) {
				hFishingPull(miscCtx(sess, 14, 9, rigRequestTree(t, proto.Guid(rod))))
			} else {
				hFishingHooked(ctx)
			}
			want := "[上钩] 鱼竿（11111111）：perch 1.234kg"
			if got := finishHookedChat(t, sess, ch); !slices.Equal(got, []string{want}) {
				t.Fatalf("early hook fish chat = %q, want one %q", got, want)
			}
			hFishingHooked(ctx)
			hFishingHooked(ctx)
			if got := finishHookedChat(t, sess, ch); len(got) != 0 {
				t.Fatalf("repeated hook confirmation was announced: %q", got)
			}
		})
	}
}

func TestHookedChatRestoredPendingFish(t *testing.T) {
	s, db := newRigTestServer(t)
	rod := "11111111111111111111111111111111"
	st := s.fishingStateForRod(1000001, rod)
	st.mu.Lock()
	st.CastActive, st.Hooked = true, true
	st.Pending = &RolledFish{FishGUID: newFishGUID(), Species: "perch", WeightG: 1234}
	st.mu.Unlock()

	restored := newFishingRecoveryServer(t, db)
	setWeatherTestTunable(t, restored, "fish_hooked_chat", "1")
	sess, peer := newTunnelProbe(t, restored)
	sess.Srv.sessions[sess.ID] = sess
	ch := collectHookedChat(peer)
	st = restored.fishingStateForRod(sess.Userid, rod)
	st.mu.Lock()
	if !st.Hooked || st.Pending == nil {
		st.mu.Unlock()
		t.Fatal("hooked fish was not restored from storage")
	}
	fish, generation := st.Pending, st.castGeneration
	st.persist.sessionID = sess.ID
	st.mu.Unlock()
	restored.pushPendingBite(sess, st, fish, rod, generation)
	ctx := miscCtx(sess, 14, 11, rigRequestTree(t, proto.Guid(rod), proto.Guid(fish.FishGUID)))
	hFishingHooked(ctx)
	hFishingHooked(ctx)
	want := "[上钩] 鱼竿（11111111）：perch 1.234kg"
	if got := finishHookedChat(t, sess, ch); !slices.Equal(got, []string{want}) {
		t.Fatalf("restored fish chat = %q, want one %q", got, want)
	}
}
