package gameserver

import (
	"bytes"
	"testing"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

// 赛季时段必须覆盖当前时刻：资格节点按 5013 时段判定是否开放，
// 固定窗口过期后会让节点全部锁定。
func TestSeasonBoundsCoversNow(t *testing.T) {
	start, end := seasonBounds()
	now := uint64(proto.WindowsFiletimeNow())
	if start > now || now >= end {
		t.Fatalf("赛季时段未覆盖当前时刻: start=%d now=%d end=%d", start, now, end)
	}
	s := proto.FiletimeToTime(int64(start))
	e := proto.FiletimeToTime(int64(end))
	if s.Day() != 22 || s.Hour() != 0 || e.Day() != 22 || e.Hour() != 9 {
		t.Fatalf("赛季锚点应为每月 22 日 00:00 → 次月 22 日 09:00 UTC，实得 %s ~ %s", s, e)
	}
	if d := e.Sub(s); d < 30*24*time.Hour || d > 32*24*time.Hour {
		t.Fatalf("赛季跨度异常: %s", d)
	}
}

// 玩家档案 307 内嵌的 5012/5013 与 28/2 必须下发同一时段。
func TestSeasonBoundsSharedByProfileAndFog(t *testing.T) {
	start, end := seasonBounds()

	body, err := proto.EncodeValue(build5013(0, seasonEndFiletime(0)), wiregen.Table())
	if err != nil {
		t.Fatalf("5013 编码失败: %v", err)
	}
	dec, _, err := proto.DecodeValue(body, 0, wiregen.Table())
	if err != nil {
		t.Fatalf("5013 解码失败: %v", err)
	}
	if len(dec.Fields) < 3 {
		t.Fatalf("5013 字段不足: %d", len(dec.Fields))
	}
	if got := dec.Fields[1].Num; got == nil || *got != start {
		t.Fatalf("5013.f1 起点不符: got=%v want=%d", got, start)
	}
	if got := dec.Fields[2].Num; got == nil || *got != end {
		t.Fatalf("5013.f2 终点不符: got=%v want=%d", got, end)
	}

	fogBody, ok := hFog(&Ctx{})
	if !ok {
		t.Fatal("28/2 未处理")
	}
	if !bytes.Contains(fogBody, proto.PackU64(start)) || !bytes.Contains(fogBody, proto.PackU64(end)) {
		t.Fatal("28/2 未下发当前赛季时段")
	}
	staleStart := uint64(134238816000000000)
	staleEnd := uint64(134265924000000000)
	if bytes.Contains(fogBody, proto.PackU64(staleStart)) || bytes.Contains(fogBody, proto.PackU64(staleEnd)) {
		t.Fatal("28/2 仍在下发过期赛季时段")
	}
}
