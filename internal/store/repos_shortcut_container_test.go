package store

import (
	"context"
	"testing"
)

// 竿子在不同容器之间移动时，快捷栏绑定必须保留：
// 客户端按返回列表的顺序编号槽位，绑定一丢就会错位（1 号竿变 2 号）。
func TestShortcutBindingSurvivesItemContainerMove(t *testing.T) {
	db := shortcutTestDB(t)
	ctx := context.Background()
	if err := db.BindShortcut(ctx, 1, 1, shortcutOwnGUID); err != nil {
		t.Fatal(err)
	}
	for _, container := range []string{"rig", "tent", "car", "boat", "scooter", "house"} {
		if _, err := db.SQL.Exec("UPDATE item SET container=? WHERE guid=?", container, shortcutOwnGUID); err != nil {
			t.Fatal(err)
		}
		bindings, err := db.ListShortcuts(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(bindings) != 1 || bindings[0].Slot != 1 || bindings[0].GUID != shortcutOwnGUID {
			t.Fatalf("container=%s lost shortcut binding: %v", container, bindings)
		}
	}
	if _, err := db.SQL.Exec("UPDATE item SET container='backpack' WHERE guid=?", shortcutOwnGUID); err != nil {
		t.Fatal(err)
	}
	// 放进鱼护/导入的原生容器仍然清绑定（保持既有语义）。
	for _, container := range []string{"keepnet", "native:17"} {
		if err := db.BindShortcut(ctx, 1, 1, shortcutOwnGUID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.SQL.Exec("UPDATE item SET container=? WHERE guid=?", container, shortcutOwnGUID); err != nil {
			t.Fatal(err)
		}
		bindings, err := db.ListShortcuts(ctx, 1)
		if err != nil || len(bindings) != 0 {
			t.Fatalf("container=%s kept binding: %v %v", container, bindings, err)
		}
		if _, err := db.SQL.Exec("UPDATE item SET container='backpack' WHERE guid=?", shortcutOwnGUID); err != nil {
			t.Fatal(err)
		}
	}
}

// 绑定一个已移动到帐篷里的物品后再次保存快捷栏时，不能因为旧绑定所在容器
// 不是背包而拒绝整次保存。
func TestShortcutSavePreservesBindingInNonBackpackContainer(t *testing.T) {
	db := shortcutTestDB(t)
	ctx := context.Background()
	if _, err := db.SQL.Exec("UPDATE item SET container='backpack' WHERE guid=?", shortcutStoredGUID); err != nil {
		t.Fatal(err)
	}
	if err := db.BindShortcut(ctx, 1, 1, shortcutOwnGUID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec("UPDATE item SET container='tent' WHERE guid=?", shortcutOwnGUID); err != nil {
		t.Fatal(err)
	}
	if err := db.BindShortcut(ctx, 1, 2, shortcutStoredGUID); err != nil {
		t.Fatalf("save with tent binding failed: %v", err)
	}
	bindings, err := db.ListShortcuts(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 2 || bindings[0].Slot != 1 || bindings[0].GUID != shortcutOwnGUID || bindings[1].Slot != 2 || bindings[1].GUID != shortcutStoredGUID {
		t.Fatalf("unexpected bindings: %v", bindings)
	}
}
