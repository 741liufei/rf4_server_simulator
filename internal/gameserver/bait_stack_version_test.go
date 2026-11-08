package gameserver

import (
	"testing"

	"rf4server/internal/store"
)

func baitStackVersion(t *testing.T, row *store.Item) uint64 {
	t.Helper()
	if row == nil {
		t.Fatal("missing bait stack")
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	idx := fieldIdxByCS(row.WireClass, csCreated)
	if idx < 0 || idx >= len(fields) {
		t.Fatal("missing bait stack version")
	}
	return u64Of(fields[idx].Num)
}

func TestBaitReloadPersistsSourceStackVersion(t *testing.T) {
	s, db := newRigTestServer(t)
	rod := baitTestRod(t, s)
	// 补饵以钓组仍有钩为前提；无钩钓组的补饵请求按无效处理。
	mountHookForBaitTest(t, s, rod, 0)
	source, child := baitTestMount(t, s, rod, "worm_01", 5, 350)
	before, err := db.GetItemByGUID(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	previousVersion := baitStackVersion(t, before)
	if _, err := s.consumeBaitEvent(t.Context(), 1000001, rod, "stack-version-bite", []string{child}, false); err != nil {
		t.Fatal(err)
	}
	change, err := s.reloadBaitForCast(t.Context(), 1000001, rod)
	if err != nil {
		t.Fatal(err)
	}
	if !containsGUID(change.Updated, source) {
		t.Fatal("reload omitted source stack update")
	}
	after, err := db.GetItemByGUID(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	version := baitStackVersion(t, after)
	if baitQuantity(after) != 3 || after.Qty != 3 {
		t.Fatalf("reload source quantity = %d/%d, want 3/3", baitQuantity(after), after.Qty)
	}
	if version <= previousVersion {
		t.Fatalf("reload source version = %d, want > %d", version, previousVersion)
	}
	tree, err := s.ItemToTree(t.Context(), after)
	if err != nil {
		t.Fatal(err)
	}
	versionField := tree.Field(fieldIdxByCS(after.WireClass, csCreated))
	quantityField := tree.Field(fieldIdxByCS(after.WireClass, csQty))
	if versionField.U64() != version || quantityField.U32() != 3 {
		t.Fatal("source snapshot omitted persisted quantity or version")
	}
}

func TestBaitDetachPersistsSourceStackVersion(t *testing.T) {
	s, db := newRigTestServer(t)
	rod := baitTestRod(t, s)
	source, child := baitTestMount(t, s, rod, "worm_01", 5, 350)
	before, err := db.GetItemByGUID(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	previousVersion := baitStackVersion(t, before)
	result := s.rigRemoveItemComponent(t.Context(), 1000001, rod, child)
	if result == nil || result.SourceTree == nil || result.SourceGUID != source {
		t.Fatal("detach omitted source stack refresh")
	}
	after, err := db.GetItemByGUID(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	version := baitStackVersion(t, after)
	if baitQuantity(after) != 5 || after.Qty != 5 {
		t.Fatalf("detach source quantity = %d/%d, want 5/5", baitQuantity(after), after.Qty)
	}
	if version <= previousVersion {
		t.Fatalf("detach source version = %d, want > %d", version, previousVersion)
	}
	versionField := result.SourceTree.Field(fieldIdxByCS(after.WireClass, csCreated))
	quantityField := result.SourceTree.Field(fieldIdxByCS(after.WireClass, csQty))
	if versionField.U64() != version || quantityField.U32() != 5 {
		t.Fatal("detach snapshot omitted persisted quantity or version")
	}
}
