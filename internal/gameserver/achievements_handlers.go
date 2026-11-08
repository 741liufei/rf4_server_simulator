package gameserver

import (
	"rf4server/internal/proto"
)

func registerAchievementHandlers(d *Dispatch) {
	d.Register(3, 3, hAchievements305)
	d.Register(3, 16, hPlayerRewards2015)
	d.Register(3, 17, hFishTrophies323)
}

// hPlayerRewards2015 处理 3/16 奖赏列表。客户端 RPC 签名
// public iidloljandl[] cfbomeockod() 返回 2015 数组，容器名 "185"
// （argcode_table by_type_id[2015]=185 = by_cmd[3/16]）。
// 奖赏持久化与发奖链路尚未实装，本轮只把兜底 args("524") 换成合法空数组。
func hPlayerRewards2015(ctx *Ctx) ([]byte, bool) {
	return encodeArgsCode(ctx, "185")
}

func hAchievements305(ctx *Ctx) ([]byte, bool) {
	if ctx.Sess == nil || ctx.Sess.Srv == nil || ctx.Sess.Srv.DB == nil {
		return encodeArgsCode(ctx, "114")
	}
	v, err := ctx.Sess.Srv.buildAchievements305(ctx.Sess.Srv.dbCtx(), ctx.Sess.Userid)
	if err != nil {
		ctx.Log.Error("3/3 成就查询失败", "err", err)
		return encodeArgsCode(ctx, "114")
	}
	body, err := proto.EncodeValue(v, ctx.Sess.Table())
	if err != nil {
		ctx.Log.Error("3/3 成就编码失败", "err", err)
		return encodeArgsCode(ctx, "114")
	}
	// 纯查询：不判定、不写库、不推送。成就解锁与弹窗只发生在玩法事件后。
	return body, true
}

func hFishTrophies323(ctx *Ctx) ([]byte, bool) {
	if ctx.Sess == nil || ctx.Sess.Srv == nil || ctx.Sess.Srv.DB == nil {
		return encodeArgsCode(ctx, "132")
	}
	v, err := ctx.Sess.Srv.buildFishTrophies323(ctx.Sess.Srv.dbCtx(), ctx.Sess.Userid)
	if err != nil {
		ctx.Log.Error("3/17 鱼种奖杯查询失败", "err", err)
		return encodeArgsCode(ctx, "132")
	}
	body, err := proto.EncodeValue(v, ctx.Sess.Table())
	if err != nil {
		ctx.Log.Error("3/17 鱼种奖杯编码失败", "err", err)
		return encodeArgsCode(ctx, "132")
	}
	return body, true
}

func encodeArgsCode(ctx *Ctx, code string) ([]byte, bool) {
	body, err := proto.EncodeValue(proto.Args(proto.S(code)), ctx.Sess.Table())
	if err != nil {
		ctx.Log.Error("空参数列表编码失败", "code", code, "err", err)
		return emptyArgsBody(), true
	}
	return body, true
}
