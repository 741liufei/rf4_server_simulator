package gameserver

import (
	"time"

	"rf4server/internal/proto"
)

// pushKeepalive 2/2 世界时钟保活推送：args(524)[obj 308]。
//
// 载荷包含 308 玩家姿态浮点块，
// f[7] = u64 FILETIME 世界时钟（必须真实 UTC，与 1/2 同源）；
// 客户端把它 ×24 展开成游戏时间（现实 1h = 游戏 24h），世界日号也据它推算。
// 状态推进走环境感知读取口：舒适度向天气/篝火/昼夜均衡点漂移，
// 晒阳与装备被动加成在这里生效（动作路径读到的都是上一拍持久化值）。
func pushKeepalive(sess *Session) {
	ft := uint64(proto.WindowsFiletimeNow())
	now := time.Now()
	env := sess.conditionEnvForSession(now, sess.Srv.conditionParams())
	obj := sess.Srv.buildPose308Env(sess.pushCtx(), sess.Userid, env)
	obj.Fields[7].Num = ptrU64(ft)
	sess.Push(2, 2, proto.Args524(obj))
}
