package gameserver

import (
	"testing"
	"time"

	"rf4server/internal/store"
)

// 落水保护必须跟随 bite_delay_scale：把倍率调快后，第一轮到点不应再被
// "落水未满 6 秒"顺延回去（否则实际出鱼时间恒为 6 秒）。
func TestBiteFloorDelayFollowsScale(t *testing.T) {
	cases := []struct {
		name string
		cfg  biteDelaySettings
		lure bool
		want time.Duration
	}{
		{"默认倍率保持 6 秒", biteDelaySettings{scale: 1}, false, 6 * time.Second},
		{"倍率 0.25 降到 1.5 秒", biteDelaySettings{scale: 0.25}, false, 1500 * time.Millisecond},
		{"倍率 0.5 降到 3 秒", biteDelaySettings{scale: 0.5}, false, 3 * time.Second},
		{"倍率极小时受 800ms 下限保护", biteDelaySettings{scale: 0.01}, false, 800 * time.Millisecond},
		{"路亚固定 1.2 秒且不吃倍率", biteDelaySettings{scale: 0.25}, true, 1200 * time.Millisecond},
		{"固定延迟优先于倍率", biteDelaySettings{fixed: 2, scale: 0.25}, false, 2 * time.Second},
		{"上限低于 6 秒时保持上限", biteDelaySettings{scale: 1, maxDelay: 4}, false, 4 * time.Second},
		{"倍率为 0 时按未配置处理", biteDelaySettings{scale: 0}, false, 6 * time.Second},
	}
	for _, c := range cases {
		if got := biteFloorDelay(c.cfg, c.lure); got != c.want {
			t.Fatalf("%s: cfg=%+v lure=%v got=%v want=%v", c.name, c.cfg, c.lure, got, c.want)
		}
	}
}

// timer 路径必须和水域级配置共用同一份有效参数，不能只读取全局 tunable。
func TestBiteFloorUsesEffectiveLocConfig(t *testing.T) {
	cases := []struct {
		name    string
		setting store.FishBiteSetting
		want    time.Duration
	}{
		{
			name: "水域倍率",
			setting: store.FishBiteSetting{
				BiteProbability: 1, BiteDelayFixed: 0, BiteDelayScale: 0.25,
				BiteDelayMax: 20, BiteDelayLureMax: 6,
			},
			want: 1500 * time.Millisecond,
		},
		{
			name: "水域固定延迟",
			setting: store.FishBiteSetting{
				BiteProbability: 1, BiteDelayFixed: 2, BiteDelayScale: 1,
				BiteDelayMax: 20, BiteDelayLureMax: 6,
			},
			want: 2 * time.Second,
		},
		{
			name: "水域上限低于 6 秒",
			setting: store.FishBiteSetting{
				BiteProbability: 1, BiteDelayFixed: 0, BiteDelayScale: 1,
				BiteDelayMax: 4, BiteDelayLureMax: 6,
			},
			want: 4 * time.Second,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, db := newDetachTestServer(t)
			t.Cleanup(func() { stopFishingRecoveryTimers(srv) })
			const level = "level_pr243_review"
			const loc = "loc_pr243_review"
			if _, err := db.SQL.ExecContext(t.Context(), `INSERT OR REPLACE INTO loc_alias(alias,loc,note) VALUES(?,?,?)`, level, loc, "pr243 review"); err != nil {
				t.Fatal(err)
			}
			if err := db.SaveAdminFishConfig(t.Context(), store.FishConfigSave{Area: loc, Setting: c.setting}, "pr243-review"); err != nil {
				t.Fatal(err)
			}
			srv.tunMu.Lock()
			srv.tunCache = map[string]float64{}
			srv.tunAt = time.Now()
			srv.tunMu.Unlock()

			sess := registerBiteTestSession(t, srv, 243, nil)
			st := srv.stateFor(sess.Userid)
			now := time.Now()
			st.mu.Lock()
			st.Loc = level
			st.RigType = 1
			st.CastActive = true
			st.InWater = true
			st.CastTs = now
			st.FallSeen = true
			st.FallTs = now
			st.Resolved = false
			st.PushSent = false
			st.Pending = nil
			st.TickSeen = false
			st.RigSettled = false
			ticket := fishingTimerTicket{st.castGeneration, st.biteTicket}
			st.mu.Unlock()

			srv.onRodBiteTimerTicket(sess.Userid, st, ticket)
			st.mu.Lock()
			due := st.BiteAt
			st.mu.Unlock()
			got := time.Until(due)
			if got < c.want-200*time.Millisecond || got > c.want+300*time.Millisecond {
				t.Fatalf("下一次咬钩排期=%v，期望约 %v", got, c.want)
			}
		})
	}
}
