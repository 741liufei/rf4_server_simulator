package gameserver

import (
	"sort"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

// The assistant is a virtual contact, not a player account or an administrator.
const fishQueryContactUID int64 = 2147483647
const fishQueryContactName = "查鱼助手"

func hSocialMembers(ctx *Ctx) ([]byte, bool) {
	s := ctx.Sess.Srv
	ids := []uint32{uint32(fishQueryContactUID)}
	seen := map[int64]bool{fishQueryContactUID: true}
	s.mu.Lock()
	for _, peer := range s.sessions {
		if peer != nil && !peer.Closed() && peer.Userid > 0 && !seen[peer.Userid] {
			ids = append(ids, uint32(peer.Userid))
			seen[peer.Userid] = true
		}
	}
	s.mu.Unlock()
	sort.Slice(ids[1:], func(i, j int) bool { return ids[i+1] < ids[j+1] })
	return encodeSocialTree(ctx, proto.U32Arr(ids))
}

func fishQueryContactCard() *proto.Value {
	return wiregen.MustMakeObj("dgbmlekfpbg", map[int]*proto.Value{
		0: proto.U32(uint32(fishQueryContactUID)),
		1: proto.StrVal(fishQueryContactName),
		2: proto.Str(nil, 0x15),
		3: proto.U32(0),
		4: proto.U32(1),
		5: proto.U32(0),
		6: proto.Bool(true),
		7: proto.U32(4),
		8: proto.U32(0),
	})
}

const fishQueryHelp = "欢迎使用查鱼助手。此联系人通过个人消息回复，不会在公共聊天中广播。"

func (s *Server) receiveFishQuery(ctx *Ctx, _ string) ([]byte, bool) {
	if !ctx.ExpectResponse {
		return nil, true
	}
	reply := fishQueryHelp
	c := ctx.Sess.pushCtx()
	tx, err := beginEconomyTx(c, s.DB)
	if err != nil {
		ctx.ReplyFailure("System.IO.IOException", "查询回复保存失败")
		return nil, true
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(c, `INSERT INTO mail(uid,sender,kind,text,created_ts,read) VALUES(?,?,0,?,?,0)`, ctx.Sess.Userid, fishQueryContactUID, reply, float64(time.Now().Unix()))
	var id int64
	if err == nil {
		id, err = res.LastInsertId()
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		ctx.ReplyFailure("System.IO.IOException", "查询回复保存失败")
		return nil, true
	}
	ctx.AfterResponse(func() { s.notifyMail(ctx.Sess.pushCtx(), ctx.Sess.Userid, id) })
	return []byte{0}, true
}
