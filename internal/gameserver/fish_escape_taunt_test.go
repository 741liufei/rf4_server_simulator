package gameserver

import (
	"bytes"
	"testing"
	"time"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/tunnel"
)

// newEscapeProbeServer 构造不依赖数据库模板的最小服务端，避免 testdb 模板问题。
func newEscapeProbeServer(t *testing.T) (*Server, *Session, *tunnel.Conn) {
	t.Helper()
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "probe"}}, logging.Discard(), nil)
	sess, gc := newTunnelProbe(t, srv)
	return srv, sess, gc
}

// TestFishEscapeTauntBroadcastShape 跑鱼提示默认走 401 kind3 蓝色模板：
// 24/27 推送、f0=3、f4 为 typed KV（键1 文案、键2 重量），文案含鱼种、档位与语气词。
func TestFishEscapeTauntBroadcastShape(t *testing.T) {
	srv, sess, gc := newEscapeProbeServer(t)
	srv.notifyFishEscape(sess, &RolledFish{Species: "g_tench", WeightG: 1547, Grade: 3}, "脱钩离开")

	env := awaitPushEnvelope(t, gc)
	if env.Main != 24 || env.Sub != 27 {
		t.Fatalf("跑鱼提示应走 24/27，实得 %d/%d", env.Main, env.Sub)
	}
	dec, _, err := proto.DecodeValue(env.Body, 0, srv.Table())
	if err != nil {
		t.Fatalf("推送 body 不可解码: %v", err)
	}
	if dec.Kind != proto.KindArgs || len(dec.Items) != 3 {
		t.Fatalf("24/27 body 应 args[3]，实得 %v/%d", dec.Kind, len(dec.Items))
	}
	msg := dec.Items[0]
	if f0 := msg.Field(0).Num; f0 == nil || *f0 != 3 {
		t.Fatalf("跑鱼模板类型应为 3，实得 %v", f0)
	}
	if msg.Field(4).Val == nil {
		t.Fatal("kind3 必须带 KV 参数")
	}
	// f4 是原始 typed KV 字节，直接按报文断言：容器类型名 135、键1 文案、
	// 键2 重量（u32le），重量必须排在文案之后（客户端模板句尾追加重量）。
	body := env.Body
	for _, want := range [][]byte{[]byte("135"), []byte("g_tench"), []byte("稀有"), []byte("你的")} {
		if !bytes.Contains(body, want) {
			t.Fatalf("推送报文缺少 %q: %x", want, body)
		}
	}
	textAt := bytes.Index(body, []byte("你的"))
	weight := []byte{0x0b, 0x06, 0x00, 0x00} // 1547g
	weightAt := bytes.Index(body, weight)
	if weightAt < 0 || weightAt < textAt {
		t.Fatalf("KV 键2 重量应在文案之后: text=%d weight=%d body=%x", textAt, weightAt, body)
	}
	if len(fishEscapeTaunts) == 0 {
		t.Fatal("语气词池为空")
	}
	found := false
	for _, taunt := range fishEscapeTaunts {
		if taunt != "" && bytes.Contains(body, []byte(taunt)) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("文案未包含语气词池中的任意一条: %x", body)
	}
}

// TestFishEscapeOfficialFallback 关闭嘲讽开关后回退官方 kind7 模板。
func TestFishEscapeOfficialFallback(t *testing.T) {
	srv, sess, gc := newEscapeProbeServer(t)
	srv.tunMu.Lock()
	srv.tunCache = map[string]float64{"fish_escape_taunt": 0}
	srv.tunAt = time.Now()
	srv.tunMu.Unlock()

	srv.notifyFishEscape(sess, &RolledFish{Species: "g_tench", WeightG: 547}, "脱钩离开")
	env := awaitPushEnvelope(t, gc)
	dec, _, err := proto.DecodeValue(env.Body, 0, srv.Table())
	if err != nil {
		t.Fatalf("推送 body 不可解码: %v", err)
	}
	if f0 := dec.Items[0].Field(0).Num; f0 == nil || *f0 != 7 {
		t.Fatalf("关闭嘲讽后模板类型应为 7，实得 %v", f0)
	}
	if f4 := dec.Items[0].Field(4).Val; f4 != nil && f4.Kind != proto.KindNull {
		t.Fatalf("官方模板不应再带 KV 参数: %+v", f4)
	}
}

// TestFishEscapeWithoutPendingFish 无可播报鱼体时回退到原因对应的官方模板，
// 不能拼出“你的 ，跑掉了”或附带 0 克重量。
func TestFishEscapeWithoutPendingFish(t *testing.T) {
	srv, sess, gc := newEscapeProbeServer(t)
	srv.notifyFishEscape(sess, nil, "断主线")
	env := awaitPushEnvelope(t, gc)
	if env.Main != 24 || env.Sub != 27 {
		t.Fatalf("跑鱼提示应走 24/27，实得 %d/%d", env.Main, env.Sub)
	}
	dec, _, err := proto.DecodeValue(env.Body, 0, srv.Table())
	if err != nil {
		t.Fatalf("推送 body 不可解码: %v", err)
	}
	if f0 := dec.Items[0].Field(0).Num; f0 == nil || *f0 != 6 {
		t.Fatalf("断主线无鱼体时应回退官方模板 6，实得 %v", f0)
	}
	if f4 := dec.Items[0].Field(4).Val; f4 != nil && f4.Kind != proto.KindNull {
		t.Fatalf("官方模板不应附带 KV 参数: %+v", f4)
	}
}
