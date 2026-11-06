package gameserver

import (
	"testing"
	"time"
)

// 落地播报去重：窗口内命中同鱼，跨窗与异鱼不命中，空 GUID 不参与。
func TestFishAnnouncedWindow(t *testing.T) {
	sess := &Session{}
	if sess.fishAnnounced("g1") {
		t.Fatal("未标记前不应命中")
	}
	sess.markFishAnnounced("g1")
	if !sess.fishAnnounced("g1") {
		t.Fatal("刚标记的鱼应在窗口内命中")
	}
	if sess.fishAnnounced("g2") {
		t.Fatal("其他 GUID 不应命中")
	}

	// 窗口过期后不再命中。
	sess.announcedMu.Lock()
	sess.announced["g1"] = time.Now().Add(-fishAnnouncedWindow - time.Second)
	sess.announcedMu.Unlock()
	if sess.fishAnnounced("g1") {
		t.Fatal("超窗后不应命中")
	}

	// 空 GUID 既不记录也不命中。
	sess.markFishAnnounced("")
	if sess.fishAnnounced("") {
		t.Fatal("空 GUID 不应命中")
	}
	if _, ok := sess.announced[""]; ok {
		t.Fatal("空 GUID 不应写入表")
	}
}

// 过期条目需要在查询/标记时被清理，避免长时间在线 map 无界增长。
func TestAnnouncedPrunesExpired(t *testing.T) {
	sess := &Session{}
	sess.markFishAnnounced("old")
	sess.announcedMu.Lock()
	sess.announced["old"] = time.Now().Add(-2 * fishAnnouncedWindow)
	sess.announcedMu.Unlock()

	sess.markFishAnnounced("new")

	sess.announcedMu.Lock()
	defer sess.announcedMu.Unlock()
	if _, ok := sess.announced["old"]; ok {
		t.Fatal("过期条目应被清理")
	}
	if _, ok := sess.announced["new"]; !ok {
		t.Fatal("新条目应保留")
	}
}

// 颜色分档：25=蓝冠及以上、26=奖杯、3=达标/破纪录；未达标不发模板。
func TestFishAnnouncementKind(t *testing.T) {
	cases := []struct {
		name      string
		grade     int64
		newRecord bool
		want      int
	}{
		{"蓝冠", 4, false, 25},
		{"奖杯", 3, false, 26},
		{"达标", 2, false, 3},
		{"达标破纪录", 2, true, 3},
		{"未达标", 1, false, 0},
		{"奖杯优先于破纪录", 3, true, 26},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fishAnnouncementKind(tc.grade, tc.newRecord); got != tc.want {
				t.Fatalf("grade=%d newRecord=%v: got %d, want %d", tc.grade, tc.newRecord, got, tc.want)
			}
		})
	}
}

// 入护播报已永久停用：未达标与达标鱼都不再补白色“入护：…”行。
func TestFishKeptDoesNotBroadcast(t *testing.T) {
	srv, sess, gc := newEscapeProbeServer(t)
	srv.notifyFishKept(sess, &RolledFish{FishGUID: "undersized", Species: "g_tench", WeightG: 400, Grade: 1})
	srv.notifyFishKept(sess, &RolledFish{FishGUID: "kept", Species: "g_tench", WeightG: 900, Grade: 2})

	done := make(chan string, 1)
	go func() {
		_, _, payload, err := gc.ReadRecord()
		if err != nil {
			done <- ""
			return
		}
		done <- string(payload)
	}()
	select {
	case payload := <-done:
		if payload != "" {
			t.Fatalf("入护播报应永久停用，仍收到推送: %q", payload)
		}
	case <-time.After(300 * time.Millisecond):
		// 没有推送 = 期望结果
	}
}
