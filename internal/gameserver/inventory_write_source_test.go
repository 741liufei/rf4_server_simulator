package gameserver

import (
	"context"
	"testing"

	"rf4server/internal/proto"
)

func TestCreateRodPersistsFactoryPartsAndF5(t *testing.T) {
	t.Parallel()
	srv, _ := newDetachTestServer(t)
	ctx := context.Background()
	rod, err := srv.CreateItem(ctx, 1000001, "feeder_10126_FD390", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := FieldsFromJSON(rod.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if fields[i].Val == nil || fields[i].Val.Kind != proto.KindObj {
			t.Fatalf("持久化竿 f%d 厂件缺失", i)
		}
	}
	f5 := fields[5].Val
	if f5 == nil || f5.Class != "gbbhdkefnkn" {
		t.Fatalf("持久化竿 f5 钓组缺失: %#v", f5)
	}
	if got := int64(u64Of(f5.Field(1).Num)); got != 12 {
		t.Fatalf("持久化 f5.f1=%d，期望默认 BottomSimple(12)", got)
	}
}

func TestFactoryPartsStateFlagsMatchPartDefs(t *testing.T) {
	t.Parallel()
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	rod, err := srv.CreateItem(ctx, 1000001, "feeder_10126_FD390", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	def, err := db.GetItemDef(ctx, rod.NumericID)
	if err != nil || def == nil {
		t.Fatal(err)
	}
	fields, _ := FieldsFromJSON(rod.FieldsJSON)
	keys := []string{"ajkflmejnih", "ofpkgghjmmf", "ddbadcmdkcl"}
	for i, key := range keys {
		partID, ok := rodPartSystemID(def, key)
		if !ok {
			t.Fatalf("缺厂件槽 %s", key)
		}
		partDef, err := db.GetItemDefByItemID(ctx, partID)
		if err != nil || partDef == nil {
			t.Fatalf("厂件定义缺失 %s: %v", partID, err)
		}
		want, _ := defAttrU64(partDef, "hgaijieklek")
		if i == 0 {
			want |= 2048
		}
		part := fields[i].Val
		si := fieldIdxByCS(part.Class, csStateEnum)
		if got := u64Of(part.Field(si).Num); got != want {
			t.Fatalf("f%d %s 状态=0x%X，期望部件定义值 0x%X", i, partID, got, want)
		}
	}
}

func TestCountableQuantityMatchesField(t *testing.T) {
	t.Parallel()
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	it, err := srv.CreateItem(ctx, 1000001, "worm_01", "backpack", 2)
	if err != nil {
		t.Fatal(err)
	}
	fields, _ := FieldsFromJSON(it.FieldsJSON)
	qi := fieldIdxByCS(it.WireClass, csQty)
	if got, want := it.Qty, int64(u64Of(fields[qi].Num)); got != want || got != 60 {
		t.Fatalf("新堆 row.qty=%d field.qty=%d，期望60/60", got, want)
	}
	if _, stacked, err := srv.AddToStack(ctx, 1000001, "worm_01", "backpack", 1); err != nil || !stacked {
		t.Fatalf("AddToStack: stacked=%v err=%v", stacked, err)
	}
	row, _ := db.GetItemByGUID(ctx, it.GUID)
	fields, _ = FieldsFromJSON(row.FieldsJSON)
	if got, want := row.Qty, int64(u64Of(fields[qi].Num)); got != want || got != 90 {
		t.Fatalf("并堆后 row.qty=%d field.qty=%d，期望90/90", got, want)
	}
}

func TestNestedReelChildrenUseRigContainerEnum(t *testing.T) {
	t.Parallel()
	srv, _ := newDetachTestServer(t)
	ctx := context.Background()
	reel, err := srv.CreateItem(ctx, 1000001, "spin_5184", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	fields, _ := FieldsFromJSON(reel.FieldsJSON)
	for i := 2; i <= 14; i++ {
		sub := fields[i].Val
		if sub == nil || sub.Kind != proto.KindObj {
			continue
		}
		si := fieldIdxByCS(sub.Class, csSlotEnum)
		if si < 0 {
			t.Fatalf("f%d %s 缺容器枚举", i, sub.Class)
		}
		if got := u64Of(sub.Field(si).Num); got != 4 {
			t.Fatalf("f%d %s 容器=%d，期望 rig(4)", i, sub.Class, got)
		}
	}
}
