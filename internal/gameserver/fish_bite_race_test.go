package gameserver

import (
	"testing"
	"time"
)

// 2026-11-06 23:57 日志实证（loc12_15 rig_MarinePilkerr2）：
// 咬钩 timer 在饵未入水时到点自毁（NeedBite 等待态，BiteAt 留在过去），
// 随后的 14/7 入水帧里 resumeFishingState 用过期 BiteAt 重排出立即触发的
// timer，与 handler 自己的 14/7 补排赛跑：timer 先判出 m_scorpius 并推
// 14/14（客户端 14/11 中鱼开始搏鱼），补排随后清场把它抹掉再排一枪，
// 5 秒后判出 alfonsino——客户端 f0=6 用新鱼替换当前鱼，收竿结算入库的
// 是 alfonsino，与搏鱼所见不符。
//
// 清场要害已由 #215 的 scheduleBiteState 守卫挡住；本文件钉住另一半——
// resumeFishingState 不得用已过期的 BiteAt 同会话补枪（重连/重启恢复
// 与未来时刻的同会话补排安全网语义保留，见
// TestFishingLeftLureEscapeReschedulesBiteDuringRetrieve）。

func TestResumeSameSessionDoesNotRearmExpiredBiteTimer(t *testing.T) {
	srv, _, st := timerTestServer(t)
	st.mu.Lock()
	st.CastActive = true
	st.Resolved = false
	st.InWater = false
	st.NeedBite = true
	st.PushSent = false
	st.Pending = nil
	st.pushing = nil
	st.cancelBiteTimerLocked() // 咬钩已到点自毁：NeedBite 等待态
	st.BiteAt = time.Now().Add(-time.Second)
	st.mu.Unlock()
	srv.resumeFishingState(st)
	st.mu.Lock()
	if st.biteTimer != nil {
		st.mu.Unlock()
		t.Fatal("同一会话的 NeedBite 等待态不得用过期 BiteAt 重排（会立即触发并与 14/7 补排赛跑）")
	}
	// 重连（会话转移）仍要恢复 timer。
	st.persist.sessionID = 0
	st.mu.Unlock()
	srv.resumeFishingState(st)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.biteTimer == nil {
		t.Fatal("重连后应恢复咬钩 timer")
	}
}

// 补排的合法场景（14/14 尚未发出）必须照常排期，防止 resume 收敛误伤补排。
func TestScheduleBiteForHookedStillSchedulesWhenNoFishOut(t *testing.T) {
	srv, sess, st := timerTestServer(t)
	st.mu.Lock()
	st.CastActive = true
	st.Resolved = false
	st.InWater = true
	st.NeedBite = false
	st.Pending = nil
	st.PushSent = false
	st.pushing = nil
	st.cancelBiteTimerLocked() // 模拟旧 timer 已因 NeedBite 到点自毁
	st.mu.Unlock()
	if !srv.scheduleBiteState(sess, st, true, false) {
		t.Fatal("无鱼在外时补排应正常排期")
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.biteTimer == nil || st.PushSent || st.Pending != nil {
		t.Fatal("补排后应有新 timer 且不得残留鱼状态")
	}
}
