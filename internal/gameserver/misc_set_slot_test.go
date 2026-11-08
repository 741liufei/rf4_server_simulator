package gameserver

import (
	"context"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

func TestSetSlotUnbindsPreviousOwner(t *testing.T) {
	t.Parallel()
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	a, _ := srv.CreateItem(ctx, 1000001, "worm_01", "backpack", 1)
	b, _ := srv.CreateItem(ctx, 1000001, "bob_25018", "backpack", 1)
	sess := newProbeSess(srv)
	call := func(g string) {
		tree := proto.Args524(proto.U32(0), proto.Guid(g), proto.U32(77))
		if _, ok := hItemSetSlot(&Ctx{Sess: sess, Main: 4, Sub: 10, Tree: tree, DecodeOK: true, Log: logging.Discard()}); !ok {
			t.Fatal("4/10 未处理")
		}
	}
	call(a.GUID)
	call(b.GUID)
	ra, _ := db.GetItemByGUID(ctx, a.GUID)
	rb, _ := db.GetItemByGUID(ctx, b.GUID)
	t.Logf("a=%+v b=%+v", ra, rb)
	var g string
	_ = db.SQL.QueryRowContext(ctx, `SELECT guid FROM container_slot WHERE uid=1000001 AND container=0 AND slot=77`).Scan(&g)
	t.Logf("slot77=%s", g)
	if ra.Slot != 0 {
		t.Fatalf("A slot=%d，期望0", ra.Slot)
	}
}
