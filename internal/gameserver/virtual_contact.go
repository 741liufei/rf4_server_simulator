package gameserver

import (
	"context"
	"database/sql"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

func isVirtualContact(uid int64) bool {
	return uid == fishQueryContactUID || uid == gearAssistantUID
}

func virtualContactCard(uid int64, name string) *proto.Value {
	return wiregen.MustMakeObj("dgbmlekfpbg", map[int]*proto.Value{
		0: proto.U32(uint32(uid)),
		1: proto.StrVal(name),
		2: proto.Str(nil, 0x15),
		3: proto.U32(0),
		4: proto.U32(1),
		5: proto.U32(0),
		6: proto.Bool(true),
		7: proto.U32(4),
		8: proto.U32(0),
	})
}

// insertContactReply lets a state-changing assistant save its setting and reply
// in the same transaction. Notification must follow the request acknowledgement.
func insertContactReply(ctx context.Context, tx *sql.Tx, uid, sender int64, text string) (int64, error) {
	res, err := tx.ExecContext(ctx, `INSERT INTO mail(uid,sender,kind,text,created_ts,read) VALUES(?,?,0,?,?,0)`, uid, sender, text, float64(time.Now().Unix()))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}
