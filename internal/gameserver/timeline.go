package gameserver

// timeline.go 实现登录和进图时间线中的辅助命令。
// 这些命令大多只需要返回结构合法的最小响应；响应类型错误会导致客户端解析失败。

import (
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

func registerTimelineHandlers(d *Dispatch) { // fire-and-forget：客户端不等响应，绝不回包
	d.Register(1, 7, hNoReply)        // game/locale 区域上报
	d.Register(1, 15, hNoReply)       // game/enter_place_notify 进入地图单向通知
	d.Register(3, 10, hBoatUse)       // boat_use 上船/下船单向上报，据此维护在船状态
	d.Register(3, 11, hCampfireStart) // campfire_start：保存到期时间并返回有效篝火状态
	d.Register(1, 18, hBoolTrue)      // game/telemetry 设备指纹 → bool true

	// 空集/无数据语义
	d.Register(6, 2, hNull)           // comp/load 无竞赛 → null
	d.Register(24, 3, hSocialCurrent) // social/current：返回当前公共聊天频道
	d.Register(24, 12, hU32ArrEmpty)  // social/bl_get 黑名单 → 空 u32 数组
	d.Register(9, 5, hCraftTemplates)
	d.Register(3, 18, hPlayerWarnings)
	d.RegisterResponseContract(3, 18, ResponseContractRule{
		Kind: proto.KindArgs, ArgCode: "189", ArgItemTypeIDs: []uint32{3005},
	})

	// 环境/视觉（28 族，纯构造最小合法结构）
	d.Register(28, 1, hEnvironment)
	d.Register(28, 2, hFog)

	// 金币商店货架（23/1 storereal/list）
	d.Register(23, 1, hStoreRealList)
}

// hNoReply 处理但不回包（fire-and-forget）。
func hNoReply(*Ctx) ([]byte, bool) { return nil, true }

// hBoolTrue 回 bool true（0x0e）。
func hBoolTrue(*Ctx) ([]byte, bool) { return []byte{0x0e}, true }

// hNull 回 null（0x00）。
func hNull(*Ctx) ([]byte, bool) { return []byte{0x00}, true }

// hU32ArrEmpty 回空 u32 数组（05 0000）。
func hU32ArrEmpty(*Ctx) ([]byte, bool) { return []byte{0x05, 0x00, 0x00}, true }

// hCraftTemplates 处理 9/5，返回空的 args(97) 配方模板。
// 必须通过编码器生成字节，避免手写长度前缀导致错位。
func hCraftTemplates(ctx *Ctx) ([]byte, bool) {
	body, err := proto.EncodeValue(proto.Args(proto.S("97")), ctx.Sess.Table())
	if err != nil {
		return emptyArgsBody(), true
	}
	return body, true
}

// hPlayerWarnings 返回警告数组；客户端按数组解包，null 会触发空引用。
func hPlayerWarnings(ctx *Ctx) ([]byte, bool) {
	body, err := proto.EncodeValue(proto.Args(proto.S("189")), ctx.Sess.Table())
	return body, err == nil
}

// nullVal 共享的 null 值。
func nullVal() *proto.Value { return &proto.Value{Kind: proto.KindNull} }

// hEnvironment 处理 28/1，返回 5011 环境配置对象和空 args(206) 参数表。
func hEnvironment(*Ctx) ([]byte, bool) {
	obj := proto.Obj("acjaheimjge", 5011,
		proto.Field{Op: "BYTE", Num: ptrU64(0)},
		proto.Field{Op: "VAL", Val: proto.Args(proto.S("206"))},
	)
	body, err := proto.EncodeValue(obj, wiregen.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// hFog 处理 28/2，返回 5003 雾效与环境时段对象。
// 赛季块包含固定时段和空的环境参数容器。
func hFog(*Ctx) ([]byte, bool) {
	season := proto.Obj("nkakjeigjla", 5013,
		proto.Field{Op: "BYTE", Num: ptrU64(6)},
		proto.Field{Op: "U64", Num: ptrU64(134238816000000000)},
		proto.Field{Op: "U64", Num: ptrU64(134265924000000000)},
		proto.Field{Op: "OPT", Num: ptrU64(0)},
		proto.Field{Op: "U64", Num: ptrU64(0)},
		proto.Field{Op: "VAL", Val: nullVal()},
	)
	fog := proto.Obj("fopkeoppndn", 5003,
		proto.Field{Op: "VAL", Val: proto.Obj("fkcmcjlagdh", 5012,
			proto.Field{Op: "VAL", Val: proto.Args(proto.S("210"), season)}),
		},
		proto.Field{Op: "VAL", Val: proto.Args(proto.S("202"))},
		proto.Field{Op: "VAL", Val: proto.Args(proto.S("201"))},
	)
	body, err := proto.EncodeValue(fog, wiregen.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// hStoreRealList 23/1 金币商店：本项目不提供充值或付费功能，恒回空货架。
func hStoreRealList(ctx *Ctx) ([]byte, bool) {
	body, err := proto.EncodeValue(proto.Args(proto.S("147")), ctx.Sess.Table())
	if err != nil {
		return emptyArgsBody(), true
	}
	return body, true
}

// 3/10 boat_use 的动作字节。协议契约里 3/10 没有 DTO（fallback_kinds 为 none），
// 解码器会把请求体尾字节当结束标记吞掉，所以按尾字节判上下船。实机样本：
// 0x0e 两次都紧跟在 3/7 验票通过之后 9~10 毫秒，即上船；0x0f 出现在玩家靠岸
// 按 E 下船的瞬间，另有 6 条历史样本时机吻合。
const (
	boatUseBoard     byte = 0x0e
	boatUseDisembark byte = 0x0f
)

// boatUseIsBoard 从 3/10 请求体取上下船结论。取值认不出时 ok=false，
// 调用方只记日志不改状态，避免把“没证据”写成“已下船”。
func boatUseIsBoard(body []byte) (board bool, ok bool) {
	if len(body) == 0 {
		return false, false
	}
	switch body[len(body)-1] {
	case boatUseBoard:
		return true, true
	case boatUseDisembark:
		return false, true
	}
	return false, false
}

// hBoatUse 处理 3/10 boat_use：单向上报，不回包。上船时 3/7 已经写过租期，
// 这里补的是“下船瞬间”的实时证据，否则只能等租期到期才算下船。
func hBoatUse(ctx *Ctx) ([]byte, bool) {
	board, ok := boatUseIsBoard(ctx.Body)
	if !ok {
		if ctx.Log != nil {
			ctx.Log.Info("3/10 boat_use", "len", len(ctx.Body), "hex", traceHexHead(ctx.Body, 160))
		}
		return nil, true
	}
	srv := ctx.Sess.Srv
	st := srv.stateFor(ctx.Sess.Userid)
	if st != nil {
		st.mu.Lock()
		st.markBoatClientEventLocked(time.Now().Unix(), board)
		st.mu.Unlock()
	}
	if ctx.Log != nil {
		if board {
			ctx.Log.Info("3/10 boat_use 已上船")
		} else {
			ctx.Log.Info("3/10 boat_use 已下船")
		}
	}
	return nil, true
}
