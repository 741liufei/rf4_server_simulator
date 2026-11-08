package gameserver

import "testing"

// setSatiety 把测试玩家的饱食度写成指定值（不存在行时按默认值插入）。
func setSatiety(t *testing.T, s *Server, value float64) {
	t.Helper()
	if _, err := s.DB.SQL.Exec(`INSERT INTO player_condition(uid, primary_value, secondary_value, comfort_value, luck_penalty, updated_ts)
		VALUES(1000001, 0.85, ?, 1.0, 0, 0)
		ON CONFLICT(uid) DO UPDATE SET secondary_value=?`, value, value); err != nil {
		t.Fatal(err)
	}
}

// 饱食度满时进食被拒，物品数量不变。
func TestFoodUseRejectedWhenSatietyFull(t *testing.T) {
	s := newDrinkUseTestServer(t)
	row := drinkUseTestItem(t, s, "carrot", 3, 0)
	setSatiety(t, s, 1.0)
	ctx := drinkUseTestCtx(t, s, 60, row.GUID, 0)
	body, handled := hItemUse(ctx)
	if !handled {
		t.Fatal("full-satiety use must still be handled")
	}
	if body != nil {
		t.Fatal("full satiety must reject the use with a failure reply")
	}
	drinkUseTestQty(t, s, row.GUID, 3, 0)
}

// 饱食度未满时正常扣减。
func TestFoodUseAllowedBelowFullSatiety(t *testing.T) {
	s := newDrinkUseTestServer(t)
	row := drinkUseTestItem(t, s, "carrot", 3, 0)
	setSatiety(t, s, 0.5)
	ctx := drinkUseTestCtx(t, s, 61, row.GUID, 0)
	body, handled := hItemUse(ctx)
	if !handled || len(body) == 0 {
		t.Fatal("food use must be handled")
	}
	drinkUseTestQty(t, s, row.GUID, 2, 0)
}
