package gameserver

import (
	"context"
	"testing"

	"rf4server/internal/store"
)

// seedKeepnetFish 往鱼护里放一尾鱼。物种 id 直接用配方 alternatives 里的字面量
// （如 ed_crab）——这类 id 在 item_def 里没有定义，只存在于 keepnet_items。
func seedKeepnetFish(t *testing.T, db *store.DB, uid int64, guid, species string, ts float64) {
	t.Helper()
	if _, err := db.SQL.Exec(`
		INSERT INTO keepnet_items(echo_guid,uid,species,name_zh,weight_g,length_m,cat,
		                          keep_loc,keep_spot,keep_bait,keep_level,ts)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		guid, uid, species, "蟹", 1200, 0.40, 2, "loc1_02", 1, "worm", "level_001_lake", ts); err != nil {
		t.Fatalf("写入鱼护渔获 %s 失败: %v", species, err)
	}
}

// keepnetGUIDs 返回该玩家鱼护里现存条目的 echo guid 集合。
func keepnetGUIDs(t *testing.T, db *store.DB, uid int64) map[string]bool {
	t.Helper()
	rows, err := db.SQL.Query(`SELECT echo_guid FROM keepnet_items WHERE uid=?`, uid)
	if err != nil {
		t.Fatalf("读取鱼护失败: %v", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			t.Fatal(err)
		}
		out[g] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// craftOK 执行一次 9/1 并返回 803.f0（制作成功位）。
func craftOK(t *testing.T, srv *Server, uid int64, recipe string, slots []craftSlot, qty uint64) bool {
	t.Helper()
	body, ok := hCraft(economyTestCtx(srv, uid, economyCraftTree(recipe, slots, qty)))
	if !ok || len(body) == 0 {
		t.Fatalf("9/1 未处理: ok=%v body=%x", ok, body)
	}
	resp := decodeTreeValue(t, srv, body)
	if len(resp.Fields) == 0 || resp.Fields[0].Num == nil {
		t.Fatalf("803 缺少 f0: %+v", resp)
	}
	return u64Of(resp.Fields[0].Num) != 0
}

// TestCraftCrabMeatConsumesKeepnetCrab 覆盖鱼类加工配方的核心路径：
// 配方材料是鱼护里的渔获（如 craft_bait_from_crab 需要 ed_crab），
// 这类鱼种没有 item_def 定义，只能按 keepnet_items.species 匹配。
func TestCraftCrabMeatConsumesKeepnetCrab(t *testing.T) {
	db := openRecipeTestDB(t)
	srv := newSkillsCraftServer(t, db)
	ctx0 := context.Background()
	const uid = int64(4101)
	if _, err := db.EnsureProfile(ctx0, uid); err != nil {
		t.Fatal(err)
	}
	const crabGUID = "c1a00000000000000000000000000001"
	seedKeepnetFish(t, db, uid, crabGUID, "ed_crab", 1.0)

	slots := []craftSlot{{ItemID: "fish_crab", GUID: crabGUID, Species: "ed_crab"}}
	if !craftOK(t, srv, uid, "craft_bait_from_crab", slots, 1) {
		t.Fatal("蟹肉制作应成功（803.f0=1），实得失败")
	}
	if got := countOfItem(t, srv, ctx0, uid, "crab_meat"); got != 30 {
		// item_def.ljnnpokapig=30，一次制作 = 一个定义单位量（与 bread 同语义，
		// 见 skills_craft_test.go 的面包用例）。
		t.Fatalf("应产出 30 份蟹肉（定义单位量），实得 %d", got)
	}
	if keepnetGUIDs(t, db, uid)[crabGUID] {
		t.Fatal("被吃掉的蟹应已从鱼护移除")
	}
}

// TestCraftCrabMeatWithoutKeepnetCrabFails 材料不存在时必须失败，且不产物、
// 不动鱼护——回滚后不留下任何痕迹。
func TestCraftCrabMeatWithoutKeepnetCrabFails(t *testing.T) {
	db := openRecipeTestDB(t)
	srv := newSkillsCraftServer(t, db)
	ctx0 := context.Background()
	const uid = int64(4102)
	if _, err := db.EnsureProfile(ctx0, uid); err != nil {
		t.Fatal(err)
	}

	slots := []craftSlot{{ItemID: "fish_crab", GUID: "c1a00000000000000000000000000099"}}
	if craftOK(t, srv, uid, "craft_bait_from_crab", slots, 1) {
		t.Fatal("鱼护里没有蟹时制作必须失败")
	}
	if got := countOfItem(t, srv, ctx0, uid, "crab_meat"); got != 0 {
		t.Fatalf("失败时不应产出蟹肉，实得 %d", got)
	}
}

// TestCraftFishBaitRejectsForeignKeepnetFish 别人的渔获不能当材料。
func TestCraftFishBaitRejectsForeignKeepnetFish(t *testing.T) {
	db := openRecipeTestDB(t)
	srv := newSkillsCraftServer(t, db)
	ctx0 := context.Background()
	const uid, other = int64(4103), int64(4104)
	for _, u := range []int64{uid, other} {
		if _, err := db.EnsureProfile(ctx0, u); err != nil {
			t.Fatal(err)
		}
	}
	const crabGUID = "c1a00000000000000000000000000002"
	seedKeepnetFish(t, db, other, crabGUID, "ed_crab", 1.0)

	slots := []craftSlot{{ItemID: "fish_crab", GUID: crabGUID, Species: "ed_crab"}}
	if craftOK(t, srv, uid, "craft_bait_from_crab", slots, 1) {
		t.Fatal("别人的渔获不应能当材料")
	}
	if !keepnetGUIDs(t, db, other)[crabGUID] {
		t.Fatal("别人的鱼护条目不该被删")
	}
}

// TestCraftConsumesMultipleKeepnetFishForBatch 一次要多份时从多条鱼护条目扣足。
func TestCraftConsumesMultipleKeepnetFishForBatch(t *testing.T) {
	db := openRecipeTestDB(t)
	srv := newSkillsCraftServer(t, db)
	ctx0 := context.Background()
	const uid = int64(4105)
	if _, err := db.EnsureProfile(ctx0, uid); err != nil {
		t.Fatal(err)
	}
	guids := []string{
		"c1a00000000000000000000000000011",
		"c1a00000000000000000000000000012",
		"c1a00000000000000000000000000013",
	}
	for i, g := range guids {
		seedKeepnetFish(t, db, uid, g, "ed_crab", float64(i+1))
	}

	slots := []craftSlot{{ItemID: "fish_crab", GUID: guids[0], Species: "ed_crab"}}
	if !craftOK(t, srv, uid, "craft_bait_from_crab", slots, 3) {
		t.Fatal("三尾蟹做三份蟹肉应成功")
	}
	if got := countOfItem(t, srv, ctx0, uid, "crab_meat"); got != 3*30 {
		t.Fatalf("应产出 90 份蟹肉（3 套 × 定义单位量 30），实得 %d", got)
	}
	if left := keepnetGUIDs(t, db, uid); len(left) != 0 {
		t.Fatalf("三尾蟹都应被消费，实际还剩 %d 条", len(left))
	}
}

// countOfItem 统计背包/钓组里某 item_id 的总数量，按堆叠份数累加。
func countOfItem(t *testing.T, srv *Server, ctx context.Context, uid int64, itemID string) int64 {
	t.Helper()
	rows, err := srv.DB.ListItemsByContainers(ctx, uid, []string{"backpack", "rig"})
	if err != nil {
		t.Fatalf("读取背包失败: %v", err)
	}
	var n int64
	for _, it := range rows {
		if it.ItemID == itemID {
			n += srv.stackCountOf(ctx, it)
		}
	}
	return n
}
