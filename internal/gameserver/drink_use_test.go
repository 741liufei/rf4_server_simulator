package gameserver

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
)

func newDrinkUseTestServer(t *testing.T, paths ...string) *Server {
	t.Helper()
	path := filepath.Join(t.TempDir(), "drink.db")
	if len(paths) > 0 {
		path = paths[0]
	}
	s, db := newReceiptTestServer(t, path)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.EnsureProfile(t.Context(), 1000001); err != nil {
		t.Fatal(err)
	}
	for i, itemID := range []string{"tungus_vodka", "rd_red_vine", "ff_wheat_flour",
		"carrot", "ab_tea_can", "big_coffee", "food_cacao"} {
		if _, err := db.SQL.Exec(`INSERT INTO item_def(numeric_id,item_id,category,attrs_json)
			VALUES(?,?,26,'{"ljnnpokapig":1}') ON CONFLICT(numeric_id) DO NOTHING`, 900000+i, itemID); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func drinkUseTestItem(t *testing.T, s *Server, itemID string, qty int64, f13 uint64) *store.Item {
	t.Helper()
	row, err := s.CreateItem(t.Context(), 1000001, itemID, "backpack", max(1, qty))
	if err != nil {
		t.Fatal(err)
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	fields[fieldIdxByCS(row.WireClass, csQty)].Num = ptrU64(uint64(qty))
	fields[fieldIdxByCS(row.WireClass, "dnmfciolgep")].Num = ptrU64(f13)
	row.Qty = qty
	row.FieldsJSON, err = FieldsToJSON(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.UpdateItemFields(t.Context(), row.GUID, row.FieldsJSON, row.Qty, row.Durability, row.Workmanship, row.RigGUID); err != nil {
		t.Fatal(err)
	}
	return row
}

func drinkUseTestCtx(t *testing.T, s *Server, call uint32, guid string, storage uint32) *Ctx {
	t.Helper()
	sess := newProbeSess(s)
	sess.Token = "drink-use-test"
	tree := proto.Args524(proto.U32(storage), proto.Guid(guid))
	body, err := proto.EncodeValue(tree, s.Table())
	if err != nil {
		t.Fatal(err)
	}
	return &Ctx{Sess: sess, Main: 4, Sub: 8, CallID: call, Tree: tree, Body: body,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard(), replyCapture: &ctxReplyCapture{}}
}

func drinkUseTestFailure(t *testing.T, s *Server, ctx *Ctx) {
	t.Helper()
	body, handled := s.useAlcoholDrink(ctx)
	_, present, _, failure := ctx.replyCapture.snapshot()
	if !handled || body != nil || !present || !failure || len(ctx.afterResponse) != 0 {
		t.Fatalf("failure response: handled=%v body=%v present=%v failure=%v pushes=%d", handled, body, present, failure, len(ctx.afterResponse))
	}
}

func drinkUseTestQty(t *testing.T, s *Server, guid string, want int64, f13 uint64) {
	t.Helper()
	row, err := s.DB.GetItemByGUID(t.Context(), guid)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if row.Qty != want || u64Of(fields[fieldIdxByCS(row.WireClass, csQty)].Num) != uint64(want) ||
		u64Of(fields[fieldIdxByCS(row.WireClass, "dnmfciolgep")].Num) != f13 {
		t.Fatalf("quantity or f13 changed incorrectly: qty=%d fields=%+v", row.Qty, fields)
	}
}

func drinkUseTestCount(t *testing.T, s *Server, query string, want int) {
	t.Helper()
	var count int
	if err := s.DB.SQL.QueryRow(query).Scan(&count); err != nil || count != want {
		t.Fatalf("%s: count=%d want=%d err=%v", query, count, want, err)
	}
}

func TestDrinkUseAtomicQuantityAndF13(t *testing.T) {
	for _, f13 := range []uint64{0, 7} {
		t.Run(fmt.Sprint(f13), func(t *testing.T) {
			s := newDrinkUseTestServer(t)
			row := drinkUseTestItem(t, s, "rd_red_vine", 3, f13)
			ctx := drinkUseTestCtx(t, s, 1, row.GUID, 0)
			body, handled := hItemUse(ctx)
			if !handled || len(body) == 0 || len(ctx.afterResponse) != 1 {
				t.Fatalf("4/8 did not complete: handled=%v body=%v pushes=%d", handled, body, len(ctx.afterResponse))
			}
			drinkUseTestQty(t, s, row.GUID, 2, f13)
			value, end, err := proto.DecodeValue(body, 0, s.Table())
			if err != nil || end != len(body) || value.TypeID != 800 {
				t.Fatalf("inventory response: value=%+v end=%d err=%v", value, end, err)
			}
			updated := value.Field(1).Val.ArgItem(0)
			if updated == nil || u64Of(updated.Field(1).Num) != 2 || u64Of(updated.Field(13).Num) != f13 {
				t.Fatalf("inventory update lost quantity/f13: %+v", updated)
			}
			var remaining, duration int64
			var source string
			if err := s.DB.SQL.QueryRow(`SELECT remaining_millis,duration_millis,source_guid
				FROM drink_bonus_effect WHERE uid=1000001 AND category='wine'`).Scan(&remaining, &duration, &source); err != nil {
				t.Fatal(err)
			}
			if remaining != 600000 || duration != remaining || source != row.GUID {
				t.Fatalf("wine effect: remaining=%d duration=%d source=%s", remaining, duration, source)
			}
			drinkUseTestCount(t, s, `SELECT COUNT(*) FROM kv WHERE k LIKE 'drink-use:%'`, 1)
			drinkUseTestCount(t, s, `SELECT COUNT(*) FROM kv WHERE k LIKE 'drink-use:%' AND json_extract(v,'$.created_at') > 0`, 1)
		})
	}
}

func TestDrinkUseRejectsUnavailableInventory(t *testing.T) {
	for _, scenario := range []string{"empty", "quantity-mismatch", "other-owner", "other-container", "other-storage", "wrong-class"} {
		t.Run(scenario, func(t *testing.T) {
			s := newDrinkUseTestServer(t)
			qty := int64(2)
			if scenario == "empty" {
				qty = 0
			}
			row := drinkUseTestItem(t, s, "tungus_vodka", qty, 0)
			storage := uint32(0)
			var err error
			switch scenario {
			case "quantity-mismatch":
				_, err = s.DB.SQL.Exec(`UPDATE item SET qty=1 WHERE guid=?`, row.GUID)
			case "other-owner":
				_, err = s.DB.SQL.Exec(`UPDATE item SET uid=1000002 WHERE guid=?`, row.GUID)
			case "other-container":
				_, err = s.DB.SQL.Exec(`UPDATE item SET container='tent' WHERE guid=?`, row.GUID)
			case "other-storage":
				storage = 1
			case "wrong-class":
				_, err = s.DB.SQL.Exec(`UPDATE item SET type_id=140,wire_class='ohciikgjghe' WHERE guid=?`, row.GUID)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := s.DB.GetItemByGUID(t.Context(), row.GUID)
			if err != nil {
				t.Fatal(err)
			}
			drinkUseTestFailure(t, s, drinkUseTestCtx(t, s, 2, row.GUID, storage))
			after, err := s.DB.GetItemByGUID(t.Context(), row.GUID)
			if err != nil || *before != *after {
				t.Fatalf("rejection changed inventory: before=%+v after=%+v err=%v", before, after, err)
			}
			drinkUseTestCount(t, s, `SELECT COUNT(*) FROM drink_bonus_effect`, 0)
			drinkUseTestCount(t, s, `SELECT COUNT(*) FROM drink_bonus_clock`, 0)
			drinkUseTestCount(t, s, `SELECT COUNT(*) FROM kv WHERE k LIKE 'drink-use:%'`, 0)
		})
	}
}

func TestDrinkUseReplayAfterExhaustionAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.db")
	s := newDrinkUseTestServer(t, path)
	row := drinkUseTestItem(t, s, "tungus_vodka", 1, 0)
	if _, err := s.DB.SQL.Exec(`INSERT INTO container_slot(uid,container,slot,guid) VALUES(1000001,0,6,?)`, row.GUID); err != nil {
		t.Fatal(err)
	}
	ctx := drinkUseTestCtx(t, s, 10, row.GUID, 0)
	key, bodySHA := requestReceiptKey(ctx)
	if _, claimed, err := s.DB.ClaimRequestReceipt(t.Context(), key, bodySHA, true, time.Now().Add(-time.Minute)); err != nil || !claimed {
		t.Fatalf("initial request receipt: claimed=%v err=%v", claimed, err)
	}
	body, handled := s.useAlcoholDrink(ctx)
	if !handled || len(body) == 0 || len(ctx.afterResponse) != 1 {
		t.Fatal("last portion was not consumed")
	}
	if _, err := s.DB.GetItemByGUID(t.Context(), row.GUID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("exhausted drink still exists: %v", err)
	}
	drinkUseTestCount(t, s, `SELECT COUNT(*) FROM container_slot`, 0)
	value, _, err := proto.DecodeValue(body, 0, s.Table())
	if err != nil || value.Field(2).Val == nil || value.Field(2).Val.Kind != proto.KindGuidArr ||
		len(value.Field(2).Val.Items) != 1 || value.Field(2).Val.Items[0].Hex != row.GUID {
		t.Fatalf("missing native removal: %+v %v", value, err)
	}
	// 模拟业务已提交、通用回执尚未落定就中断。
	if _, err := s.DB.SQL.Exec(`UPDATE request_receipts SET updated_at=0`); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Close(); err != nil {
		t.Fatal(err)
	}
	fresh := newDrinkUseTestServer(t, path)
	retry := drinkUseTestCtx(t, fresh, 10, row.GUID, 0)
	replayed, handled := fresh.useAlcoholDrink(retry)
	if !handled || !bytes.Equal(body, replayed) || len(retry.afterResponse) != 0 {
		t.Fatalf("business receipt did not survive restart: handled=%v body=%v callbacks=%d", handled, replayed, len(retry.afterResponse))
	}
	drinkUseTestCount(t, fresh, `SELECT COUNT(*) FROM drink_bonus_effect`, 1)
	drinkUseTestCount(t, fresh, `SELECT COUNT(*) FROM kv WHERE k LIKE 'drink-use:%'`, 1)
	connection := newReceiptTestSession(t, fresh, 99, ctx.Sess.Token)
	connection.send(4, 8, 10, ctx.Body, 0x00)
	if response := waitReceiptResponse(t, connection); !bytes.Equal(body, response.Body) {
		t.Fatal("stale general receipt did not recover the committed drink response")
	}
	general, err := fresh.DB.GetRequestReceipt(t.Context(), key)
	if err != nil || general.Status != store.RequestReceiptCompleted {
		t.Fatalf("general receipt did not finish recovery: %+v %v", general, err)
	}

	// 同 key 更换请求不能消费另一瓶。
	other := drinkUseTestItem(t, fresh, "rd_red_vine", 2, 0)
	drinkUseTestFailure(t, fresh, drinkUseTestCtx(t, fresh, 10, other.GUID, 0))
	drinkUseTestQty(t, fresh, other.GUID, 2, 0)
	drinkUseTestCount(t, fresh, `SELECT COUNT(*) FROM drink_bonus_effect WHERE category='wine'`, 0)
}

func TestDrinkUseDifferentCallsRefreshSameCategory(t *testing.T) {
	s := newDrinkUseTestServer(t)
	row := drinkUseTestItem(t, s, "tungus_vodka", 3, 0)
	ctx := drinkUseTestCtx(t, s, 20, row.GUID, 0)
	body, handled := s.useAlcoholDrink(ctx)
	if !handled || len(body) == 0 {
		t.Fatal("first use failed")
	}
	retry := drinkUseTestCtx(t, s, 20, row.GUID, 0)
	if replayed, handled := s.useAlcoholDrink(retry); !handled || !bytes.Equal(body, replayed) {
		t.Fatal("same call did not replay")
	}
	drinkUseTestQty(t, s, row.GUID, 2, 0)
	if _, err := s.DB.SQL.Exec(`UPDATE drink_bonus_effect SET remaining_millis=1`); err != nil {
		t.Fatal(err)
	}
	if body, handled := s.useAlcoholDrink(drinkUseTestCtx(t, s, 21, row.GUID, 0)); !handled || len(body) == 0 {
		t.Fatal("different call must consume another portion")
	}
	drinkUseTestQty(t, s, row.GUID, 1, 0)
	var remaining int64
	if err := s.DB.SQL.QueryRow(`SELECT remaining_millis FROM drink_bonus_effect WHERE category='spirit'`).Scan(&remaining); err != nil || remaining != 900000 {
		t.Fatalf("same category did not refresh: %d %v", remaining, err)
	}
	drinkUseTestCount(t, s, `SELECT COUNT(*) FROM drink_bonus_effect`, 1)
	drinkUseTestCount(t, s, `SELECT COUNT(*) FROM kv WHERE k LIKE 'drink-use:%'`, 2)
}

func TestDrinkUseClosedSessionCannotConsumeOrRestartClock(t *testing.T) {
	s := newDrinkUseTestServer(t)
	row := drinkUseTestItem(t, s, "tungus_vodka", 3, 0)
	first := drinkUseTestCtx(t, s, 25, row.GUID, 0)
	body, handled := s.useAlcoholDrink(first)
	if !handled || len(body) == 0 {
		t.Fatal("initial drink failed")
	}
	var beforeTick int64
	if err := s.DB.SQL.QueryRow(`SELECT last_tick FROM drink_bonus_clock WHERE uid=1000001`).Scan(&beforeTick); err != nil {
		t.Fatal(err)
	}
	late := drinkUseTestCtx(t, s, 26, row.GUID, 0)
	late.Sess.closed.Store(true)
	drinkUseTestFailure(t, s, late)
	drinkUseTestQty(t, s, row.GUID, 2, 0)
	var tick int64
	var online bool
	if err := s.DB.SQL.QueryRow(`SELECT last_tick,online FROM drink_bonus_clock WHERE uid=1000001`).Scan(&tick, &online); err != nil || tick != beforeTick || online {
		t.Fatalf("closed request restarted clock: tick=%d before=%d online=%v err=%v", tick, beforeTick, online, err)
	}
	drinkUseTestCount(t, s, `SELECT COUNT(*) FROM kv WHERE k LIKE 'drink-use:%'`, 1)
	// 已提交的请求仍可取回回执，不依赖物品或会话继续存活。
	first.Sess.closed.Store(true)
	if replayed, handled := s.useAlcoholDrink(first); !handled || !bytes.Equal(body, replayed) {
		t.Fatal("closed session prevented committed receipt recovery")
	}
}

func TestDrinkUseRollsBackConsumptionEffectAndReceipt(t *testing.T) {
	for _, stage := range []string{"effect", "receipt"} {
		for _, qty := range []int64{1, 2} {
			t.Run(fmt.Sprintf("%s/%d", stage, qty), func(t *testing.T) {
				s := newDrinkUseTestServer(t)
				row := drinkUseTestItem(t, s, "tungus_vodka", qty, 7)
				trigger := `CREATE TRIGGER fail_drink BEFORE INSERT ON drink_bonus_effect BEGIN SELECT RAISE(ABORT,'test effect failure'); END`
				if stage == "receipt" {
					trigger = `CREATE TRIGGER fail_drink BEFORE INSERT ON kv WHEN NEW.k LIKE 'drink-use:%' BEGIN SELECT RAISE(ABORT,'test receipt failure'); END`
				}
				if _, err := s.DB.SQL.Exec(trigger); err != nil {
					t.Fatal(err)
				}
				drinkUseTestFailure(t, s, drinkUseTestCtx(t, s, 30, row.GUID, 0))
				drinkUseTestQty(t, s, row.GUID, qty, 7)
				fresh, err := s.DB.GetItemByGUID(t.Context(), row.GUID)
				if err != nil || fresh.FieldsJSON != row.FieldsJSON {
					t.Fatalf("rollback lost original item fields: %+v %v", fresh, err)
				}
				drinkUseTestCount(t, s, `SELECT COUNT(*) FROM drink_bonus_effect`, 0)
				drinkUseTestCount(t, s, `SELECT COUNT(*) FROM drink_bonus_clock`, 0)
				drinkUseTestCount(t, s, `SELECT COUNT(*) FROM kv WHERE k LIKE 'drink-use:%'`, 0)
			})
		}
	}
}

func TestDrinkUseLeavesOtherItemsAndMoveRouteUnchanged(t *testing.T) {
	s := newDrinkUseTestServer(t)
	// 面粉是烹饪材料，不在消耗品恢复白名单内，必须维持旧落回行为。
	flour := drinkUseTestItem(t, s, "ff_wheat_flour", 4, 0)
	ctx := drinkUseTestCtx(t, s, 40, flour.GUID, 0)
	if body, handled := s.useAlcoholDrink(ctx); handled || body != nil {
		t.Fatal("non-whitelisted item must use existing handler")
	}
	if body, handled := hItemUse(ctx); !handled || len(body) == 0 {
		t.Fatal("existing ordinary-food response changed")
	}
	drinkUseTestQty(t, s, flour.GUID, 4, 0)
	unknown := drinkUseTestCtx(t, s, 41, NewItemGUID(), 0)
	if body, handled := s.useAlcoholDrink(unknown); handled || body != nil {
		t.Fatal("unknown item must use existing handler")
	}
	drink := drinkUseTestItem(t, s, "tungus_vodka", 2, 0)
	move := drinkUseTestCtx(t, s, 42, drink.GUID, 0)
	move.Main, move.Sub = 4, 6
	move.Tree = proto.Args524(proto.GuidArr([]string{drink.GUID}), proto.U32(0), proto.U32(1))
	if body, handled := s.dispatch.lookup(4, 6)(move); !handled || len(body) == 0 {
		t.Fatal("4/6 move failed")
	}
	drinkUseTestQty(t, s, drink.GUID, 2, 0)
	row, err := s.DB.GetItemByGUID(t.Context(), drink.GUID)
	if err != nil || row.Container != "tent" {
		t.Fatalf("4/6 did not keep move behavior: %+v %v", row, err)
	}
	drinkUseTestCount(t, s, `SELECT COUNT(*) FROM drink_bonus_effect`, 0)
	drinkUseTestCount(t, s, `SELECT COUNT(*) FROM kv WHERE k LIKE 'drink-use:%'`, 0)
}

func TestDrinkUseReceiptCleanup(t *testing.T) {
	s := newDrinkUseTestServer(t)
	if _, err := s.DB.SQL.Exec(`
		INSERT INTO kv(k,v) VALUES
		('drink-use:old', '{"created_at":9999}'),
		('drink-use:fresh', '{"created_at":10001}'),
		('drink-use:malformed', 'not-json'),
		('unrelated', '{"created_at":1}')`); err != nil {
		t.Fatal(err)
	}
	s.cleanupDrinkUseReceipts(time.UnixMilli(drinkUseReceiptRetention.Milliseconds() + 10000))
	drinkUseTestCount(t, s, `SELECT COUNT(*) FROM kv WHERE k='drink-use:old' OR k='drink-use:malformed'`, 0)
	drinkUseTestCount(t, s, `SELECT COUNT(*) FROM kv WHERE k='drink-use:fresh'`, 1)
	drinkUseTestCount(t, s, `SELECT COUNT(*) FROM kv WHERE k='unrelated'`, 1)
}

// TestConsumableRestoreClassification 消耗品恢复分类：三类热饮各归各位，
// 食品进 food；烹饪材料、酒类（走经验规则）和未知物品不进白名单。
func TestConsumableRestoreClassification(t *testing.T) {
	for item, want := range map[string]string{
		"big_coffee": "coffee", "thermos_coffee": "coffee", "brand_coffe": "coffee",
		"ab_tea_can": "tea", "thermos_tea": "tea",
		"food_cacao": "cocoa", "thermos_cacao": "cocoa",
		"carrot": "food", "apples": "food", "cake_rf4": "food", "cf_squid": "food",
	} {
		if got, ok := consumableRestoreForItem(item); !ok || got != want {
			t.Fatalf("%s 分类错误: got=%q ok=%v", item, got, ok)
		}
	}
	for _, item := range []string{"ff_wheat_flour", "ff_sugar", "ff_ins_coffe", "ff_black_tea",
		"kvas_bottle", "tungus_vodka", "barrel_honey", "unknown_item"} {
		if _, ok := consumableRestoreForItem(item); ok {
			t.Fatalf("%s 不应进消耗品恢复白名单", item)
		}
	}
}

// TestConsumableUseRestoresCondition 食品使用：扣 1 份、饱食度恢复、
// 精力/舒适度不动、使用后补推 2/2（变更推送 + 2/2 共两个回调）。
func TestConsumableUseRestoresCondition(t *testing.T) {
	s := newDrinkUseTestServer(t)
	ctx0 := t.Context()
	const uid = int64(1000001)
	if err := s.DB.UpsertPlayerCondition(ctx0, store.PlayerCondition{
		UID: uid, Primary: 0.50, Secondary: 0.30, Comfort: 0.10,
		UpdatedTS: float64(time.Now().Unix()) + 3600,
	}); err != nil {
		t.Fatal(err)
	}
	carrot := drinkUseTestItem(t, s, "carrot", 2, 0)
	ctx := drinkUseTestCtx(t, s, 50, carrot.GUID, 0)
	body, handled := s.useAlcoholDrink(ctx)
	if !handled || len(body) == 0 {
		t.Fatalf("食品 4/8 应被消耗通道处理: handled=%v body=%d", handled, len(body))
	}
	drinkUseTestQty(t, s, carrot.GUID, 1, 0)
	row, err := s.DB.EnsurePlayerCondition(ctx0, uid)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(row.Secondary-0.55) > 1e-9 {
		t.Fatalf("食品应恢复饱食度 0.30+0.25: %v", row.Secondary)
	}
	if row.Primary != 0.50 || row.Comfort != 0.10 {
		t.Fatalf("食品不应改动精力/舒适度: %+v", row)
	}
	if len(ctx.afterResponse) != 2 {
		t.Fatalf("食品使用后应补推 2/2: pushes=%d", len(ctx.afterResponse))
	}
}

// TestDrinkRestoreCategories 三类热饮各回各条，互不串扰。
func TestDrinkRestoreCategories(t *testing.T) {
	s := newDrinkUseTestServer(t)
	ctx0 := t.Context()
	const uid = int64(1000001)
	reset := func() {
		if err := s.DB.UpsertPlayerCondition(ctx0, store.PlayerCondition{
			UID: uid, Primary: 0.50, Secondary: 0.30, Comfort: 0.10,
			UpdatedTS: float64(time.Now().Unix()) + 3600,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// 茶→舒适度。
	reset()
	tea := drinkUseTestItem(t, s, "ab_tea_can", 1, 0)
	if body, handled := s.useAlcoholDrink(drinkUseTestCtx(t, s, 51, tea.GUID, 0)); !handled || len(body) == 0 {
		t.Fatal("茶应被消耗通道处理")
	}
	row, err := s.DB.EnsurePlayerCondition(ctx0, uid)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(row.Comfort-0.40) > 1e-9 {
		t.Fatalf("茶应恢复舒适度 0.10+0.30: %v", row.Comfort)
	}
	if row.Secondary != 0.30 || row.Primary != 0.50 {
		t.Fatalf("茶不应动饱食/精力: %+v", row)
	}
	// 咖啡→精力。
	reset()
	coffee := drinkUseTestItem(t, s, "big_coffee", 1, 0)
	if body, handled := s.useAlcoholDrink(drinkUseTestCtx(t, s, 52, coffee.GUID, 0)); !handled || len(body) == 0 {
		t.Fatal("咖啡应被消耗通道处理")
	}
	row, err = s.DB.EnsurePlayerCondition(ctx0, uid)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(row.Primary-0.65) > 1e-9 {
		t.Fatalf("咖啡应恢复精力 0.50+0.15: %v", row.Primary)
	}
	if row.Secondary != 0.30 || row.Comfort != 0.10 {
		t.Fatalf("咖啡不应动饱食/舒适度: %+v", row)
	}
	// 可可→饱食 + 少量精力。
	reset()
	cocoa := drinkUseTestItem(t, s, "food_cacao", 1, 0)
	if body, handled := s.useAlcoholDrink(drinkUseTestCtx(t, s, 53, cocoa.GUID, 0)); !handled || len(body) == 0 {
		t.Fatal("可可应被消耗通道处理")
	}
	row, err = s.DB.EnsurePlayerCondition(ctx0, uid)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(row.Secondary-0.65) > 1e-9 || math.Abs(row.Primary-0.55) > 1e-9 {
		t.Fatalf("可可应恢复饱食 0.30+0.35 / 精力 0.50+0.05: %+v", row)
	}
	if row.Comfort != 0.10 {
		t.Fatalf("可可不应动舒适度: %+v", row)
	}
}

// TestConsumableUseGateOffFallsThrough 总开关关闭：食品维持旧行为（不扣不恢复）。
func TestConsumableUseGateOffFallsThrough(t *testing.T) {
	s := newDrinkUseTestServer(t)
	if _, err := s.DB.SQL.Exec(`INSERT INTO tunables(key,value) VALUES('player_condition_enabled','0')`); err != nil {
		t.Fatal(err)
	}
	// tunableF 有 30 秒缓存，服务器装配期间可能已预热；强制下次重读。
	s.tunMu.Lock()
	s.tunCache, s.tunAt = nil, time.Time{}
	s.tunMu.Unlock()
	carrot := drinkUseTestItem(t, s, "carrot", 2, 0)
	ctx := drinkUseTestCtx(t, s, 54, carrot.GUID, 0)
	if body, handled := s.useAlcoholDrink(ctx); handled || body != nil {
		t.Fatal("开关关闭时食品应落回通用 handler")
	}
	drinkUseTestQty(t, s, carrot.GUID, 2, 0)
}
