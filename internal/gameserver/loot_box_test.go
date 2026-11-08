package gameserver

import (
	"context"
	"testing"

	"rf4server/internal/store"
)

// enableLootBox 打开总开关（默认关闭，测试里显式开启）。
func enableLootBox(t *testing.T, srv *Server, db *store.DB) {
	t.Helper()
	if _, err := db.SQL.Exec(`INSERT INTO tunables(key,value) VALUES('loot_box_enabled','1')
		ON CONFLICT(key) DO UPDATE SET value='1'`); err != nil {
		t.Fatalf("开启礼包: %v", err)
	}
	srv.tunMu.Lock()
	srv.tunCache = nil
	srv.tunMu.Unlock()
}

// 礼包：概率 1.0 时必开，奖励落库并返回物品 ID。
func TestLootBoxAlwaysOpensWhenChanceOne(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	sess := newProbeSess(srv)
	enableLootBox(t, srv, db)

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.SQL.Exec(q, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	mustExec(`INSERT INTO item_def(numeric_id,item_id,category,kind,brand,attrs_json) VALUES(900001,'loot_worm',27,'bait','','{}')`)
	mustExec(`INSERT INTO item_catalog(item_id,name,name_zh,kind,numeric_id) VALUES('loot_worm','Loot Worm','礼包红虫','bait',900001)`)
	mustExec(`INSERT INTO shop_item(store_id,item_id,silver,gold,stock) VALUES('fishing_store_01','loot_worm',100,0,10)`)
	mustExec(`INSERT INTO kv(k,v) VALUES('loot_box_defs','[{"id":"test_box","name":"测试礼包","chance":1.0}]')`)

	got := srv.rollLootBox(ctx, sess, &RolledFish{Species: "c.roach"})
	if got != "loot_worm" {
		t.Fatalf("应开出 loot_worm，实得 %q", got)
	}
	var n int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM item WHERE uid=? AND item_id='loot_worm'`, sess.Userid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("奖励应落库 1 件，实得 %d", n)
	}
}

// 配置了 rewards 时按权重抽取，不再去商店随机。
func TestLootBoxUsesConfiguredRewards(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	sess := newProbeSess(srv)
	enableLootBox(t, srv, db)

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.SQL.Exec(q, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	// 用计件类（27 饵料）验证数量：非计件物品每件一个实例，数量恒为 1。
	mustExec(`INSERT INTO item_def(numeric_id,item_id,category,kind,brand,attrs_json) VALUES(900002,'loot_bait',27,'bait','','{}')`)
	mustExec(`INSERT INTO item_catalog(item_id,name,name_zh,kind,numeric_id) VALUES('loot_bait','Loot Bait','礼包饵料','bait',900002)`)
	mustExec(`INSERT INTO kv(k,v) VALUES('loot_box_defs','[{"id":"b","name":"礼包","chance":1.0,"rewards":[{"item_id":"loot_bait","qty":2,"weight":100}]}]')`)

	got := srv.rollLootBox(ctx, sess, nil)
	if got != "loot_bait" {
		t.Fatalf("应开出 loot_bait，实得 %q", got)
	}
	var qty int64
	if err := db.SQL.QueryRow(`SELECT qty FROM item WHERE uid=? AND item_id='loot_bait'`, sess.Userid).Scan(&qty); err != nil {
		t.Fatal(err)
	}
	if qty != 2 {
		t.Fatalf("数量应为 2，实得 %d", qty)
	}
}

// 关闭开关后不触发。
func TestLootBoxDisabledByTunable(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	sess := newProbeSess(srv)
	if _, err := db.SQL.Exec(`INSERT INTO tunables(key,value) VALUES('loot_box_enabled','0') ON CONFLICT(key) DO UPDATE SET value='0'`); err != nil {
		t.Fatal(err)
	}
	srv.tunMu.Lock()
	srv.tunCache = nil
	srv.tunMu.Unlock()
	if got := srv.rollLootBox(ctx, sess, nil); got != "" {
		t.Fatalf("关闭后不应开箱，实得 %q", got)
	}
}

// 商店随机抽取必须排除套盒(55)与礼包(73)，避免开出套娃或错误物化的物品。
func TestLootBoxShopPickExcludesBundles(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.SQL.Exec(q, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	mustExec(`INSERT INTO item_def(numeric_id,item_id,category,kind,brand,attrs_json) VALUES(900003,'loot_only_bundle',55,'bundle','','{}')`)
	mustExec(`INSERT INTO item_def(numeric_id,item_id,category,kind,brand,attrs_json) VALUES(900004,'loot_only_gift',73,'gift','','{}')`)
	mustExec(`INSERT INTO shop_item(store_id,item_id,silver,gold,stock) VALUES('s','loot_only_bundle',10,0,5),('s','loot_only_gift',10,0,5)`)
	for i := 0; i < 30; i++ {
		id, _, _ := srv.pickRandomShopItem(ctx)
		if id == "loot_only_bundle" || id == "loot_only_gift" {
			t.Fatalf("不应开出被排除的品类 %s", id)
		}
	}
}
