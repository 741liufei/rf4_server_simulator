package gameserver

import (
	"context"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

func TestItemRemoveCannotDeleteOtherUsersItem(t *testing.T) {
	t.Parallel()
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	it, err := srv.CreateItem(ctx, 1000001, "worm_01", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	sess.Userid = 1000002
	tree := rigRequestTree(t, proto.Guid(it.GUID))
	if _, ok := hItemRemove(&Ctx{Sess: sess, Main: 4, Sub: 7, Tree: tree, DecodeOK: true, Log: logging.Discard()}); !ok {
		t.Fatal("4/7 未处理")
	}
	row, err := db.GetItemByGUID(ctx, it.GUID)
	if err != nil || row == nil {
		t.Fatal("其他玩家不能通过 4/7 删除不属于自己的物品")
	}
}
