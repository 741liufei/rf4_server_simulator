package gameserver

import (
	"strings"
	"testing"
	"time"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

func TestReleaseRejectsStaleFishGUID(t *testing.T) {
	for _, withRod := range []bool{false, true} {
		t.Run(map[bool]string{false: "fish_only", true: "rod_and_fish"}[withRod], func(t *testing.T) {
			s, db := newRigTestServer(t)
			sess := newProbeSess(s)
			if _, err := db.EnsureProfile(t.Context(), sess.Userid); err != nil {
				t.Fatal(err)
			}
			rod := "11111111111111111111111111111111"
			st := s.stateFor(sess.Userid)
			fish := &RolledFish{FishGUID: strings.Repeat("5", 32), Species: "perch", XP: 100}
			st.mu.Lock()
			st.Pending, st.Hooked = fish, true
			st.mu.Unlock()
			args := []*proto.Value{proto.Guid(strings.Repeat("6", 32))}
			if withRod {
				args = append([]*proto.Value{proto.Guid(rod)}, args...)
			}
			capture := &ctxReplyCapture{}
			body, handled := s.fishingRelease(&Ctx{Sess: sess, Tree: proto.Args524(args...), ExpectResponse: true,
				replyCapture: capture, Log: logging.Discard()})
			if !handled || body != nil || !capture.failure || st.Pending != fish {
				t.Fatal("stale release changed the current catch or reported success")
			}
			profile, err := db.GetProfile(t.Context(), sess.Userid)
			if err != nil || profile.XP != 0 {
				t.Fatalf("stale release awarded experience: %+v %v", profile, err)
			}
		})
	}
}

// 升级礼物走背包直发（不走邮件）：9→10 结算收到官方 10 级礼物（蜉蝣幼虫×15）；
// 同等级跨结算/放生/重放只发一次；金币不变；关闭 tunable 恢复旧行为（不发）。
func TestCatchAndReleaseLevelUpGrantsOfficialGifts(t *testing.T) {
	s, db := newRigTestServer(t)
	sess := newProbeSess(s)
	uid := sess.Userid
	if _, err := db.EnsureProfile(t.Context(), uid); err != nil {
		t.Fatal(err)
	}
	backpackQty := func(itemID string) int64 {
		t.Helper()
		var q int64
		if err := db.SQL.QueryRow(`SELECT COALESCE(SUM(qty),0) FROM item WHERE uid=? AND item_id=?`,
			uid, itemID).Scan(&q); err != nil {
			t.Fatal(err)
		}
		return q
	}
	mailCount := func() int {
		t.Helper()
		var count int
		if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM mail WHERE uid=?`, uid).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}

	// 结算 9→10（浮钓竿）：直发 10 级礼物，金币不变，无邮件；礼物行随结果返回。
	if _, err := db.SQL.Exec(`UPDATE profile SET level=9,xp=?,gold=1234 WHERE uid=?`, baseXp(10)-1, uid); err != nil {
		t.Fatal(err)
	}
	fish := &RolledFish{FishGUID: strings.Repeat("7", 32), Species: "perch", Grade: 2, WeightG: 642}
	result, err := s.settleCatchOnce(t.Context(), uid, fish, 1)
	if err != nil || result.newLevel < 10 {
		t.Fatalf("catch level-up failed: %+v %v", result, err)
	}
	if len(result.giftItems) != 1 || result.giftItems[0].ItemID != "naiad" || result.giftItems[0].Qty != 15 {
		t.Fatalf("结算应返回 10 级礼物行: %+v", result.giftItems)
	}
	profile, err := db.GetProfile(t.Context(), uid)
	if err != nil || profile.Gold != 1234 {
		t.Fatalf("level-up changed gold: %+v %v", profile, err)
	}
	if got := backpackQty("naiad"); got != 15 {
		t.Fatalf("10 级礼物应为蜉蝣幼虫×15: got %d", got)
	}
	if got := mailCount(); got != 0 {
		t.Fatalf("升级礼物不走邮件: mail=%d", got)
	}

	// 同一尾鱼重放结算：不重复发放，礼物行为空。
	if _, err := s.settleCatchOnce(t.Context(), uid, fish, 1); err != nil {
		t.Fatal(err)
	}
	if got := backpackQty("naiad"); got != 15 {
		t.Fatalf("重放结算不应重发礼物: got %d", got)
	}

	// 放生再次 9→10：同等级幂等，不重发。
	if _, err := db.SQL.Exec(`UPDATE profile SET level=9,xp=?,gold=1234 WHERE uid=?`, baseXp(10)-1, uid); err != nil {
		t.Fatal(err)
	}
	rel, relGifts, replayed, err := s.releaseCatchOnce(t.Context(), uid, fish.FishGUID, 10, 1)
	if err != nil || replayed || rel.Level < 10 {
		t.Fatalf("release level-up failed: %+v replayed=%v %v", rel, replayed, err)
	}
	if len(relGifts) != 0 {
		t.Fatalf("同等级放生不应返回礼物行: %+v", relGifts)
	}
	if got := backpackQty("naiad"); got != 15 {
		t.Fatalf("同等级放生升级不应重发礼物: got %d", got)
	}

	// 关闭 tunable：升到 11 级不发；重新开启后补升再直发 11 级浮钓四件套。
	if err := db.TunableSet(t.Context(), "level_gifts_enabled", "0", "test"); err != nil {
		t.Fatal(err)
	}
	s.tunAt = time.Time{} // 强制 tunableF 刷新缓存
	eleven := &RolledFish{FishGUID: strings.Repeat("8", 32), Species: "perch", Grade: 2, WeightG: 642}
	if _, err := db.SQL.Exec(`UPDATE profile SET level=10,xp=? WHERE uid=?`, baseXp(11)-1, uid); err != nil {
		t.Fatal(err)
	}
	if res, err := s.settleCatchOnce(t.Context(), uid, eleven, 1); err != nil || res.newLevel < 11 {
		t.Fatalf("level 11 failed: %+v %v", res, err)
	}
	if got := backpackQty("tele_10112_5"); got != 0 {
		t.Fatalf("关闭开关后不应发礼物: got %d", got)
	}
	if err := db.TunableSet(t.Context(), "level_gifts_enabled", "1", "test"); err != nil {
		t.Fatal(err)
	}
	s.tunAt = time.Time{}
	if _, err := db.SQL.Exec(`UPDATE profile SET level=10,xp=? WHERE uid=?`, baseXp(11)-1, uid); err != nil {
		t.Fatal(err)
	}
	again := &RolledFish{FishGUID: strings.Repeat("9", 32), Species: "perch", Grade: 2, WeightG: 642}
	if res, err := s.settleCatchOnce(t.Context(), uid, again, 1); err != nil || res.newLevel < 11 {
		t.Fatalf("level 11 retry failed: %+v %v", res, err)
	}
	for _, want := range []struct {
		item string
		qty  int64
	}{{"tele_10112_5", 1}, {"mono_15212", 1}, {"bob_25164", 1}, {"clas_20090", 1}} {
		if got := backpackQty(want.item); got != want.qty {
			t.Fatalf("11 级浮钓套 %s 应为 %d: got %d", want.item, want.qty, got)
		}
	}
}

// 11 级礼物按升到该级最后一尾鱼的钓法分套：纺车（路亚）→ Arrow S60L + Hydra Smart 2 25S。
func TestLevel11GiftVariesByRigType(t *testing.T) {
	s, db := newRigTestServer(t)
	sess := newProbeSess(s)
	uid := sess.Userid
	if _, err := db.EnsureProfile(t.Context(), uid); err != nil {
		t.Fatal(err)
	}
	backpackQty := func(itemID string) int64 {
		t.Helper()
		var q int64
		if err := db.SQL.QueryRow(`SELECT COALESCE(SUM(qty),0) FROM item WHERE uid=? AND item_id=?`,
			uid, itemID).Scan(&q); err != nil {
			t.Fatal(err)
		}
		return q
	}
	if _, err := db.SQL.Exec(`UPDATE profile SET level=10,xp=? WHERE uid=?`, baseXp(11)-1, uid); err != nil {
		t.Fatal(err)
	}
	// 放生一尾路亚（rigType=5）钓上来的鱼把玩家送进 11 级 → 路亚套，
	// 礼物行随放生结果返回（放生路径无奖励列表，靠 4/22 同步背包）。
	rel, gifts, replayed, err := s.releaseCatchOnce(t.Context(), uid, strings.Repeat("a", 32), 10, 5)
	if err != nil || replayed || rel.Level < 11 {
		t.Fatalf("spin release level 11 failed: %+v replayed=%v %v", rel, replayed, err)
	}
	if len(gifts) != 2 {
		t.Fatalf("放生应返回 11 级路亚套礼物行: %+v", gifts)
	}
	for _, want := range []struct {
		item string
		qty  int64
	}{{"spin_10003_S60L", 1}, {"spin_5826", 1}} {
		if got := backpackQty(want.item); got != want.qty {
			t.Fatalf("11 级路亚套 %s 应为 %d: got %d", want.item, want.qty, got)
		}
	}
	if got := backpackQty("tele_10112_5"); got != 0 {
		t.Fatalf("路亚升级不应发浮钓竿: got %d", got)
	}
}
