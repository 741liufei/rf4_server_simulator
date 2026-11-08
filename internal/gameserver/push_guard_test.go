package gameserver

import (
	"strings"
	"testing"

	"rf4server/internal/proto"
)

func TestPushGuardGuidArrAndUnknownPattern(t *testing.T) {
	good := proto.Args(proto.S("524"), proto.GuidArr([]string{"0123456789abcdef0123456789abcdef"}))
	if reason := checkPushArgsStrict(4, 21, good); reason != "" {
		t.Fatalf("4/21 正确 guidarr 被误拒: %s", reason)
	}
	bad := proto.Args(proto.S("524"), proto.Guid("0123456789abcdef0123456789abcdef"))
	if reason := checkPushArgsStrict(4, 21, bad); reason == "" || !strings.Contains(reason, "guidarr") {
		t.Fatalf("4/21 错误 guid 形状未被拒绝: %q", reason)
	}

	c := loadContract()
	const key = "99/99"
	old, had := c.PushArgs[key]
	c.PushArgs[key] = []string{"bogus_mode"}
	defer func() {
		if had {
			c.PushArgs[key] = old
		} else {
			delete(c.PushArgs, key)
		}
	}()
	body := proto.Args524(proto.U32(1))
	if reason := checkPushArgsStrict(99, 99, body); reason == "" || !strings.Contains(reason, "未知推送合同模式") {
		t.Fatalf("未知合同模式未被拒绝: %q", reason)
	}
}

func TestPushContractPatternsAreKnown(t *testing.T) {
	for key, decl := range loadContract().PushArgs {
		for _, want := range decl {
			want = strings.TrimSuffix(want, "...")
			if !knownPushPattern(want) {
				t.Errorf("%s 使用未登记推送模式 %q", key, want)
			}
		}
	}
}

// 4/22 物品推送必须放行全部物品 wire tid：铃铛 700、竿架 11500 属合法物品
// （items.go categoryWireClasses），曾因合同收窄 100..199 被拒发导致钓鱼游
// 装备包在背包缺件（2026-11-05 日志实锤）。
func TestPush422AllowsAllItemWireTypeIDs(t *testing.T) {
	for _, tid := range []uint32{140, 147, 700, 1470, 3200, 11500, 13301} {
		tree := proto.Obj("ohciikgjghe", tid)
		body := proto.Args(proto.S("61"), tree)
		if reason := checkPushArgsStrict(4, 22, body); reason != "" {
			t.Fatalf("4/22 物品 tid=%d 被拒发: %s", tid, reason)
		}
	}
	// 下限防呆保留：tid<100 的系统对象仍应拒绝。
	low := proto.Args(proto.S("61"), proto.Obj("ohciikgjghe", 99))
	if reason := checkPushArgsStrict(4, 22, low); reason == "" {
		t.Fatal("4/22 tid<100 系统对象未被拒绝")
	}
}
