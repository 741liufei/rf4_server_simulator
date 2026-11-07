package gameserver

import (
	"testing"
	"time"
)

// 落水保护必须跟随 bite_delay_scale：把倍率调快后，第一轮到点不应再被
// "落水未满 6 秒"顺延回去（否则实际出鱼时间恒为 6 秒）。
func TestBiteFloorDelayFollowsScale(t *testing.T) {
	cases := []struct {
		name  string
		scale float64
		lure  bool
		want  time.Duration
	}{
		{"默认倍率保持 6 秒", 1, false, 6 * time.Second},
		{"倍率 0.25 降到 1.5 秒", 0.25, false, 1500 * time.Millisecond},
		{"倍率 0.5 降到 3 秒", 0.5, false, 3 * time.Second},
		{"倍率极小时受 800ms 下限保护", 0.01, false, 800 * time.Millisecond},
		{"路亚固定 1.2 秒且不吃倍率", 0.25, true, 1200 * time.Millisecond},
		{"倍率为 0 时按未配置处理", 0, false, 6 * time.Second},
	}
	for _, c := range cases {
		if got := biteFloorDelay(c.scale, c.lure); got != c.want {
			t.Fatalf("%s: scale=%v lure=%v got=%v want=%v", c.name, c.scale, c.lure, got, c.want)
		}
	}
}
