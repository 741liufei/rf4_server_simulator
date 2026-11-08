package gameserver

import (
	"context"
	"database/sql"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

// backpackDownloadDetails 通过背包的计数、索引和详情三个真实处理器检查下载完整性。
// 单件编解码成功不足以证明列表没有漏项，因此逐一核对返回的 GUID。
//
// backpackDownloadDetails checks download completeness through the actual count,
// index, and detail handlers. Every returned GUID is matched because successful
// per-item serialization alone cannot detect missing list entries.
func backpackDownloadDetails(t *testing.T, srv *Server, uid int64) []*proto.Value {
	t.Helper()
	sess := newProbeSess(srv)
	sess.Userid = uid
	request := func(handler Handler, args ...*proto.Value) *proto.Value {
		t.Helper()
		c := economyTestCtx(srv, uid, rigRequestTree(t, args...))
		c.Sess = sess
		body, ok := handler(c)
		if !ok || len(body) == 0 {
			t.Fatal("背包请求没有响应")
		}
		value, _, err := proto.DecodeValue(body, 0, srv.Table())
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	count := request(hItemCount, proto.U32(0))
	guids := request(hItemGUIDs, proto.U32(0))
	inventoryGUIDs := deepGUIDs(guids)
	if got := u64Of(count.Fields[0].Num); got != uint64(len(inventoryGUIDs)) {
		t.Fatalf("背包计数与索引不符: count=%d GUIDs=%v", got, inventoryGUIDs)
	}
	details := request(hItemDetails, proto.U32(0), proto.GuidArr(inventoryGUIDs))
	if len(details.Items) != len(inventoryGUIDs) {
		t.Fatalf("背包详情漏项: requested=%d received=%d", len(inventoryGUIDs), len(details.Items))
	}
	remaining := make(map[string]bool, len(inventoryGUIDs))
	for _, guid := range inventoryGUIDs {
		remaining[guid] = true
	}
	for _, item := range details.Items {
		idx := fieldIdxByCS(item.Class, csGUID)
		if idx < 0 || idx >= len(item.Fields) || !remaining[item.Fields[idx].Hex] {
			t.Fatalf("背包详情缺少、重复或返回了未知 GUID: %+v", item)
		}
		delete(remaining, item.Fields[idx].Hex)
	}
	if len(remaining) != 0 {
		t.Fatalf("背包详情缺少 GUID: %v", remaining)
	}
	return details.Items
}

func TestBackpackLegacyBundleRecoveryKeepsInventoryDownloadComplete(t *testing.T) {
	t.Parallel()
	srv, db := newBundleTestServer(t)
	ctx := context.Background()
	const uid int64 = 4201
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE profile SET silver=1234,gold=56 WHERE uid=?`, uid); err != nil {
		t.Fatal(err)
	}
	healthy := economyTestItem(t, srv, uid, "tool_shovel", "backpack", 1)
	legacy, err := srv.prepareNewItem(ctx, uid, "mono_15065", "backpack", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	def, err := db.GetItemDefByItemID(ctx, testBundleID)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := FieldsFromJSON(legacy.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	fields[fieldIdxByCS(legacy.WireClass, csNumericID)].Num = ptrU64(uint64(def.NumericID))
	legacy.FieldsJSON, err = FieldsToJSON(fields)
	if err != nil {
		t.Fatal(err)
	}
	legacy.ItemID, legacy.NumericID = def.ItemID, def.NumericID
	if err := db.InsertItem(ctx, legacy); err != nil {
		t.Fatal(err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		if err := srv.RepairItemPhysics(ctx); err != nil {
			t.Fatal(err)
		}
		if err := srv.EnsurePlayer(ctx, uid); err != nil {
			t.Fatal(err)
		}
		repaired, err := db.GetItemByGUID(ctx, legacy.GUID)
		if err != nil {
			t.Fatal(err)
		}
		if repaired.ItemID != testBundleID || repaired.WireClass != "coacllgmnml" || repaired.TypeID != 3100 || repaired.Qty != 1 {
			t.Fatalf("旧组合包未恢复正确实例: %+v", repaired)
		}
		currentHealthy, err := db.GetItemByGUID(ctx, healthy.GUID)
		if err != nil || *currentHealthy != *healthy {
			t.Fatalf("恢复改变正常物品: before=%+v after=%+v err=%v", healthy, currentHealthy, err)
		}
		var silver, gold int64
		if err := db.SQL.QueryRow(`SELECT silver,gold FROM profile WHERE uid=?`, uid).Scan(&silver, &gold); err != nil {
			t.Fatal(err)
		}
		if silver != 1234 || gold != 56 {
			t.Fatalf("恢复改变余额: silver=%d gold=%d", silver, gold)
		}
		details := backpackDownloadDetails(t, srv, uid)
		if len(details) != 2 {
			t.Fatalf("恢复前后背包数量改变: %d", len(details))
		}
		classes := map[string]uint32{}
		for _, item := range details {
			classes[item.Class] = item.TypeID
		}
		if classes["coacllgmnml"] != 3100 || classes["jceogpbfpjh"] != 163 {
			t.Fatalf("背包详情类型不符: %v", classes)
		}
	}
}

func TestBackpackDownloadAfterDiscardingStarterRodAndRestart(t *testing.T) {
	t.Parallel()
	srv, db := newBundleTestServer(t)
	ctx := context.Background()
	sess := newProbeSess(srv)
	if err := srv.EnsurePlayer(ctx, sess.Userid); err != nil {
		t.Fatal(err)
	}
	var rod, line string
	if err := db.SQL.QueryRow(`SELECT guid FROM item WHERE uid=? AND item_id='tele_10012_tl16'`, sess.Userid).Scan(&rod); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL.QueryRow(`SELECT guid FROM item WHERE uid=? AND item_id='mono_15065' LIMIT 1`, sess.Userid).Scan(&line); err != nil {
		t.Fatal(err)
	}
	runRodSet(t, srv, sess, rod, line, 0)
	st := srv.stateFor(sess.Userid)
	st.mu.Lock()
	st.GearGuid = rod
	st.mu.Unlock()
	before := backpackDownloadDetails(t, srv, sess.Userid)
	capture := &ctxReplyCapture{}
	body, ok := hItemRemove(&Ctx{Sess: sess, Main: 4, Sub: 7, Tree: rigRequestTree(t, proto.Guid(rod)),
		DecodeOK: true, ExpectResponse: true, replyCapture: capture, Log: logging.Discard()})
	if _, _, _, failure := capture.snapshot(); !ok || len(body) == 0 || failure {
		t.Fatal("丢弃鱼竿失败")
	}
	restarted := newFishingRecoveryServer(t, db)
	if err := restarted.RepairItemPhysics(ctx); err != nil {
		t.Fatal(err)
	}
	if err := restarted.EnsurePlayer(ctx, sess.Userid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetItemByGUID(ctx, rod); err != sql.ErrNoRows {
		t.Fatalf("被丢弃鱼竿被重新创建: %v", err)
	}
	attached, err := db.GetItemByGUID(ctx, line)
	if err != nil || attached.Container != "backpack" || attached.RigGUID != "" {
		t.Fatalf("配件仍引用被删除鱼竿: row=%+v err=%v", attached, err)
	}
	after := backpackDownloadDetails(t, restarted, sess.Userid)
	if len(after) != len(before)-1 {
		t.Fatalf("丢竿后其他物品丢失或重复: before=%d after=%d", len(before), len(after))
	}
	for _, item := range after {
		for _, guid := range deepGUIDs(item) {
			if guid == rod {
				t.Fatalf("背包仍下发已删除的鱼竿 GUID: class=%s", item.Class)
			}
		}
	}
}
