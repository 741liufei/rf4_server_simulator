package gameserver

import (
	"encoding/hex"
	"testing"
)

// TestBoatUseIsBoardUsesCapturedBodies 用实机抓到的 3/10 请求体验证上下船判据。
// 上船样本紧跟在 3/7 验票通过之后 9~10 毫秒，下船样本是玩家靠岸按 E 的那一刻。
func TestBoatUseIsBoardUsesCapturedBodies(t *testing.T) {
	board := mustHexBoat(t, "030335323401000e")
	off := mustHexBoat(t, "030335323401000f")
	if on, ok := boatUseIsBoard(board); !ok || !on {
		t.Fatalf("0x0e 应判上船，得到 on=%v ok=%v", on, ok)
	}
	if on, ok := boatUseIsBoard(off); !ok || on {
		t.Fatalf("0x0f 应判下船，得到 on=%v ok=%v", on, ok)
	}
	for _, body := range [][]byte{nil, {}, mustHexBoat(t, "030335323401000a")} {
		if _, ok := boatUseIsBoard(body); ok {
			t.Fatalf("未取样的请求体不该给结论，body=%x", body)
		}
	}
}

// TestOnBoatLockedPrefersClientBoatEvents 校验在船状态优先采用客户端实时上报。
func TestOnBoatLockedPrefersClientBoatEvents(t *testing.T) {
	const now = 1000
	var st FishState
	if on, known := st.onBoatLocked(now); on || known {
		t.Fatalf("本次会话没见过船应中性，得到 on=%v known=%v", on, known)
	}
	st.markBoardLocked(now + 3600) // 3/7 验票通过，租期一小时
	if on, known := st.onBoatLocked(now); !on || !known {
		t.Fatalf("验票通过应在船上，得到 on=%v known=%v", on, known)
	}
	st.markBoatClientEventLocked(now+60, false) // 3/10 下船
	if on, known := st.onBoatLocked(now + 60); on || !known {
		t.Fatalf("客户端报下船后应立即判离船，得到 on=%v known=%v", on, known)
	}
	st.markBoatClientEventLocked(now+120, true) // 3/10 重新上船
	if on, known := st.onBoatLocked(now + 120); !on || !known {
		t.Fatalf("回船上应重新判在船上，得到 on=%v known=%v", on, known)
	}
	if on, _ := st.onBoatLocked(now + 3601); on {
		t.Fatal("租期已过应判不在船上")
	}
}

func mustHexBoat(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
