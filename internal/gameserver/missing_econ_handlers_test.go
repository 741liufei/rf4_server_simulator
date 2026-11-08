package gameserver

import (
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

func TestMissingEconomyContainers(t *testing.T) {
	t.Parallel()
	srv, _ := newDetachTestServer(t)
	sess := newProbeSess(srv)

	t.Run("21/5 args145 empty", func(t *testing.T) {
		body, ok := hWorkshopRedeem(&Ctx{Sess: sess, Main: 21, Sub: 5, Log: logging.Discard()})
		if !ok {
			t.Fatal("21/5 未处理")
		}
		v, _, err := proto.DecodeValue(body, 0, srv.Table())
		if err != nil {
			t.Fatal(err)
		}
		if v.Kind != proto.KindArgs || v.ArgCodeStr() != "145" || len(v.Items) != 0 {
			t.Fatalf("21/5=%s/%q/%d，期望 args145 空数组", v.Kind, v.ArgCodeStr(), len(v.Items))
		}
	})

	t.Run("22/5 args149 boat rows", func(t *testing.T) {
		body, ok := hStoreBoats(&Ctx{Sess: sess, Main: 22, Sub: 5, Log: logging.Discard()})
		if !ok {
			t.Fatal("22/5 未处理")
		}
		v, _, err := proto.DecodeValue(body, 0, srv.Table())
		if err != nil {
			t.Fatal(err)
		}
		if v.Kind != proto.KindArgs || v.ArgCodeStr() != "149" || len(v.Items) != len(loadShopBoats()) {
			t.Fatalf("22/5=%s/%q/%d，期望 args149 共 %d 项",
				v.Kind, v.ArgCodeStr(), len(v.Items), len(loadShopBoats()))
		}
		for i, item := range v.Items {
			if item.Class != "efdgfpfbjai" || item.TypeID != 560 {
				t.Fatalf("22/5[%d]=%s/%d，期望 efdgfpfbjai/560", i, item.Class, item.TypeID)
			}
		}
	})
}
