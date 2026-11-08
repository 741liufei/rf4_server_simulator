package gameserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

type storePurchase struct {
	StoreID      string
	ItemID       string
	Price        uint64
	Gold         bool
	BoughtMinute int64
	Items        []*store.Item
}

const (
	// 4.0.25034 客户端退货列表：当前游戏分钟 - 69120 >= 购买分钟。
	storeReturnWindowGameMin int64 = 69120
	// fbankbpbdbf.pfnbedifjcm 对 hgaijieklek 的 0x800000 位做 test；命中不显示退货。
	storeReturnBlockedState uint64 = 1 << 23
)

// RecoverStorePurchases 仅恢复流水、创建时刻及出厂状态可唯一对应的旧商品。
// 无法证明原数量/状态的堆叠、已使用件和套盒保持原状。
func (s *Server) RecoverStorePurchases(c context.Context) (int, error) {
	rows, err := s.DB.SQL.QueryContext(c, `SELECT t.uid,t.item_id,t.silver_delta,t.gold_delta,t.note,i.guid
		FROM txn t JOIN item i ON i.uid=t.uid AND i.item_id=t.item_id AND i.created_ts=t.ts
		WHERE t.kind='buy' AND i.container='backpack' AND COALESCE(i.rig_guid,'')=''
		AND i.buy_price=0 AND i.durability=1 AND i.workmanship=1
		AND (SELECT COUNT(*) FROM txn b WHERE b.uid=t.uid AND b.item_id=t.item_id AND b.kind='buy')=1
		AND (SELECT COUNT(*) FROM item j WHERE j.uid=i.uid AND j.item_id=i.item_id)=1`)
	if err != nil {
		return 0, err
	}
	type candidate struct {
		uid          int64
		item         string
		silver, gold int64
		note, guid   string
	}
	var candidates []candidate
	for rows.Next() {
		var r candidate
		if err := rows.Scan(&r.uid, &r.item, &r.silver, &r.gold, &r.note, &r.guid); err != nil {
			rows.Close()
			return 0, err
		}
		candidates = append(candidates, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range candidates {
		note := strings.Fields(r.note)
		if len(note) != 2 || !strings.HasPrefix(note[0], "store=") {
			continue
		}
		shop := strings.TrimPrefix(note[0], "store=")
		gold := r.gold < 0 && r.silver == 0 && note[1] == "currency=gold"
		if !gold && !(r.silver < 0 && r.gold == 0 && note[1] == "currency=silver") {
			continue
		}
		amount := r.silver
		if gold {
			amount = r.gold
		}
		if amount == math.MinInt64 {
			continue
		}
		def, err := s.DB.GetItemDefByItemID(c, r.item)
		if err != nil {
			return n, err
		}
		if def.Category == bundleCategory {
			continue
		}
		original, err := s.prepareNewItem(c, r.uid, r.item, "backpack", 1, r.guid)
		if err != nil {
			continue
		}
		row, err := s.DB.GetItemByGUID(c, r.guid)
		if err != nil {
			return n, err
		}
		if row.Qty != original.Qty || !sameStoreItemFields(row, original) {
			continue
		}
		tx, err := beginEconomyTx(c, s.DB)
		if err != nil {
			return n, err
		}
		current, err := getOwnedItemTx(c, tx, r.uid, r.guid)
		if err == nil && current.FieldsJSON == row.FieldsJSON && current.Qty == row.Qty && current.Container == "backpack" && current.RigGUID == "" {
			err = s.saveStorePurchaseTx(c, tx, r.uid, shop, r.item, uint64(-amount), gold, []*store.Item{current})
		} else if err == nil {
			err = errors.New("恢复期间商品发生变化")
		}
		if err != nil {
			tx.Rollback()
			return n, err
		}
		if err := tx.Commit(); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

type storeReturnReceipt struct {
	RequestSHA     string
	ExpectResponse bool
	Body           []byte
}

func storePurchaseKey(uid int64, guid string) string {
	return fmt.Sprintf("store-purchase:%d:%s", uid, guid)
}

// storeReturnCategoryAllowed 同步 4.0.25034 fbankbpbdbf.pfnbedifjcm(onobdmohahp)
// 的品类白名单。清单之外的饵料、窝料、添加剂、票据、地图等不进入退货。
func storeReturnCategoryAllowed(category int64) bool {
	switch category {
	case 3, 6, 11, 12, 13, 14, 15, 17,
		28, 29, 31, 38,
		41, 44, 45, 47,
		55, 58, 59,
		69, 70, 79, 80:
		return true
	default:
		return false
	}
}

// storeReturnStateAllowed 同步客户端的 0x800000 不可退状态位。
func storeReturnStateAllowed(row *store.Item) bool {
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return false
	}
	idx := fieldIdxByCS(row.WireClass, csStateEnum)
	if idx < 0 || idx >= len(fields) {
		return true
	}
	return u64Of(fields[idx].Num)&storeReturnBlockedState == 0
}

type storeRigComponentKey struct {
	Slot    int64
	Ordinal int64
	GUID    string
	Extra   int64
}

// storeReturnFactoryRigComponents 从购买时的 f5/f3/f4 快照计算出厂装配集合。
// 启动回填会把出厂件投影进 rig_component，因此不能用“有任何组件行”判断
// 玩家是否装配过；只拦截投影中不属于出厂快照的组件。
func storeReturnFactoryRigComponents(original *store.Item) (map[storeRigComponentKey]bool, bool) {
	fields, err := FieldsFromJSON(original.FieldsJSON)
	if err != nil {
		return nil, false
	}
	idx := fieldIdxByCS(original.WireClass, csRigObject)
	if idx < 0 || idx >= len(fields) {
		return nil, false
	}
	stored := fields[idx].Val
	if stored == nil {
		return nil, false
	}
	comps := rigCompsOf(stored)
	_, rigType, _ := rigIdentity(stored)
	out := make(map[storeRigComponentKey]bool, len(comps)+2)
	for key, comp := range comps {
		if comp.GUID == "" {
			continue
		}
		cat, ordinal := rigCompCategory(key), rigCompOrdinal(key)
		if _, special := categorySlotField[cat]; special {
			continue
		}
		out[storeRigComponentKey{Slot: cat, Ordinal: ordinal, GUID: comp.GUID,
			Extra: canonicalIndexForOrdinal(cat, comp.Extra, rigType, ordinal)}] = true
	}
	for _, cat := range []int64{catReel, catLine} {
		csName := categorySlotField[cat]
		sidx := fieldIdxByCS(original.WireClass, csName)
		if sidx < 0 || sidx >= len(fields) || !validRigGUID(fields[sidx].Hex) {
			continue
		}
		out[storeRigComponentKey{Slot: cat, GUID: fields[sidx].Hex}] = true
	}
	return out, true
}

// storeReturnHasExtraRig 只允许出厂投影；任何玩家后加的物理子件或投影组件都会拒绝退货。
func (s *Server) storeReturnHasExtraRig(c context.Context, tx *sql.Tx, current, original *store.Item) (bool, error) {
	expected, isRig := storeReturnFactoryRigComponents(original)
	if !isRig {
		var attached int
		err := tx.QueryRowContext(c, `SELECT (SELECT COUNT(*) FROM item WHERE rig_guid=?)+(SELECT COUNT(*) FROM rig_component WHERE rig_guid=? OR guid=?)`,
			current.GUID, current.GUID, current.GUID).Scan(&attached)
		return attached != 0, err
	}
	rows, err := tx.QueryContext(c, `SELECT slot, ordinal, COALESCE(guid,''), extra FROM rig_component WHERE rig_guid=?`,
		current.GUID)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var key storeRigComponentKey
		if err := rows.Scan(&key.Slot, &key.Ordinal, &key.GUID, &key.Extra); err != nil {
			rows.Close()
			return false, err
		}
		if !expected[key] {
			rows.Close()
			return true, nil
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	var physical int
	err = tx.QueryRowContext(c, `SELECT (SELECT COUNT(*) FROM item WHERE rig_guid=?)+(SELECT COUNT(*) FROM rig_component WHERE guid=?)`,
		current.GUID, current.GUID).Scan(&physical)
	return physical != 0, err
}

// 与扣款、入库同事务保存完整批次，包含套盒的所有组成物品。
func (s *Server) saveStorePurchaseTx(c context.Context, tx *sql.Tx, uid int64, storeID, itemID string, price uint64, gold bool, rows []*store.Item) error {
	if price == 0 || price > math.MaxInt64 || len(rows) == 0 {
		return errors.New("无效购买凭据")
	}
	level := spawnLevelKey
	if v, ok := s.levelByUID(uid); ok && v != "" {
		level = v
	}
	boughtMinute := worldTotalGameMin(time.Now())
	if rows[0].CreatedTS > 0 {
		boughtMinute = worldTotalGameMin(time.Unix(int64(rows[0].CreatedTS), 0))
	}
	for _, row := range rows {
		fields, err := FieldsFromJSON(row.FieldsJSON)
		if err != nil {
			return err
		}
		tree := proto.Obj(row.WireClass, uint32(row.TypeID), fields...)
		// 客户端按世界总游戏分钟筛选退货，0 会被判为早已过期。
		attachReceipt(tree, wiregen.MustMakeObj("pcfflcdpjgk", map[int]*proto.Value{
			0: proto.StrVal(storeID), 1: proto.U32(uint32(uid)), 2: proto.U32(uint32(boughtMinute)),
			3: proto.U64(price), 4: proto.U32(uint32(boolToI(gold))), 5: proto.StrVal(level),
		}))
		row.FieldsJSON, err = FieldsToJSON(tree.Fields)
		if err != nil {
			return err
		}
		row.BuyPrice = int64(price)
		if _, err := tx.ExecContext(c, `UPDATE item SET fields_json=?,buy_price=? WHERE guid=? AND uid=?`, row.FieldsJSON, row.BuyPrice, row.GUID, uid); err != nil {
			return err
		}
	}
	data, err := json.Marshal(storePurchase{StoreID: storeID, ItemID: itemID, Price: price, Gold: gold, BoughtMinute: boughtMinute, Items: rows})
	if err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := tx.ExecContext(c, `INSERT INTO kv(k,v) VALUES(?,?)`, storePurchaseKey(uid, row.GUID), string(data)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) returnStorePurchase(ctx *Ctx) ([]byte, bool) {
	if !s.requireDB(ctx) {
		return nil, false
	}
	var tx *sql.Tx
	failResult := func(err error, code int) ([]byte, bool) {
		if tx != nil {
			_ = tx.Rollback()
		}
		ctx.Log.Warn("商店退货未完成", "err", err)
		ctx.ReplyOperationResult(code)
		return nil, true
	}
	fail := func(err error) ([]byte, bool) {
		return failResult(err, 196) // 暂时无法提供服务；详细原因保留在服务端日志。
	}
	if ctx.Tree == nil || len(ctx.Tree.Items) != 2 || ctx.Tree.Items[0] == nil || ctx.Tree.Items[1] == nil || ctx.Tree.Items[0].Kind != proto.KindStr || ctx.Tree.Items[1].Kind != proto.KindGuid {
		return fail(errors.New("退货参数应为 store、guid"))
	}
	storeID, guid := ctx.Tree.Items[0].StrOr(), ctx.Tree.Items[1].GuidHex()
	if storeID == "" || !validRigGUID(guid) {
		return fail(errors.New("无效退货参数"))
	}
	c, uid := ctx.Sess.pushCtx(), ctx.Sess.Userid
	key, sha := requestReceiptKey(ctx)
	receiptKey := fmt.Sprintf("store-return:%d:%s:%d:%d:%d", uid, key.TokenHash, key.Main, key.Sub, key.CallID)
	var err error
	tx, err = beginEconomyTx(c, s.DB)
	if err != nil {
		return fail(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(c, `UPDATE kv SET v=v WHERE k=?`, receiptKey); err != nil {
		return fail(err)
	}
	var saved string
	if err := tx.QueryRowContext(c, `SELECT v FROM kv WHERE k=?`, receiptKey).Scan(&saved); err == nil {
		var r storeReturnReceipt
		if err := json.Unmarshal([]byte(saved), &r); err != nil {
			return fail(err)
		}
		if r.RequestSHA != sha || r.ExpectResponse != ctx.ExpectResponse {
			return fail(errors.New("退货回执冲突"))
		}
		return r.Body, true
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fail(err)
	}
	var raw string
	if err := tx.QueryRowContext(c, `SELECT v FROM kv WHERE k=?`, storePurchaseKey(uid, guid)).Scan(&raw); err != nil {
		return fail(err)
	}
	var purchase storePurchase
	if err := json.Unmarshal([]byte(raw), &purchase); err != nil {
		return fail(err)
	}
	if purchase.StoreID != storeID || purchase.Price == 0 || purchase.Price > math.MaxInt64 || len(purchase.Items) == 0 {
		return fail(errors.New("原店或购买金额不符"))
	}
	boughtMinute := purchase.BoughtMinute
	if boughtMinute <= 0 && purchase.Items[0] != nil && purchase.Items[0].CreatedTS > 0 {
		boughtMinute = worldTotalGameMin(time.Unix(int64(purchase.Items[0].CreatedTS), 0))
	}
	if boughtMinute <= 0 || worldTotalGameMin(time.Now())-boughtMinute >= storeReturnWindowGameMin {
		return failResult(errors.New("退货时限已过"), 149)
	}
	guids := make([]string, 0, len(purchase.Items))
	for _, original := range purchase.Items {
		if original == nil || original.UID != uid {
			return fail(errors.New("无效购买记录"))
		}
		row, err := getOwnedItemTx(c, tx, uid, original.GUID)
		if err != nil {
			return fail(err)
		}
		if row.RigGUID != "" {
			return failResult(errors.New("商品仍有装配关系"), 151)
		}
		if row.Container != "backpack" {
			return failResult(errors.New("商品不在背包"), 111)
		}
		if row.ItemID != original.ItemID || row.Qty != original.Qty || row.Durability != original.Durability || row.Workmanship != original.Workmanship || !sameStoreItemFields(row, original) {
			// 4.0.25034 原生150提示“非全新货”，普通异常会变成内部错误。
			return failResult(errors.New("商品已使用或状态已改变"), 150)
		}
		def, err := itemDefByItemIDTx(c, tx, original.ItemID)
		if err != nil || def == nil || !storeReturnCategoryAllowed(def.Category) || !storeReturnStateAllowed(original) {
			return fail(errors.New("商品品类不支持退货"))
		}
		extra, err := s.storeReturnHasExtraRig(c, tx, row, original)
		if err != nil {
			return fail(err)
		}
		if extra {
			return failResult(errors.New("商品仍有装配关系"), 151)
		}
		guids = append(guids, row.GUID)
	}
	for _, g := range guids {
		if _, err := tx.ExecContext(c, `DELETE FROM rig_component WHERE rig_guid=? AND EXISTS (SELECT 1 FROM item WHERE guid=? AND uid=?)`, g, g, uid); err != nil {
			return fail(err)
		}
		if _, err := tx.ExecContext(c, `DELETE FROM rod_setting WHERE guid=? AND EXISTS (SELECT 1 FROM item WHERE guid=? AND uid=?)`, g, g, uid); err != nil {
			return fail(err)
		}
		if _, err := tx.ExecContext(c, `DELETE FROM rig WHERE guid=? AND uid=?`, g, uid); err != nil {
			return fail(err)
		}
	}
	removed, err := removeItemsTx(c, tx, uid, guids)
	if err != nil || removed != int64(len(guids)) {
		return fail(fmt.Errorf("退货扣物失败: %v", err))
	}
	silver, gold := int64(purchase.Price), int64(0)
	if purchase.Gold {
		silver, gold = 0, int64(purchase.Price)
	}
	if err := applyWalletTx(c, tx, uid, silver, gold, "return", purchase.ItemID, guid, "store="+storeID); err != nil {
		return fail(err)
	}
	for _, g := range guids {
		if _, err := tx.ExecContext(c, `DELETE FROM kv WHERE k=?`, storePurchaseKey(uid, g)); err != nil {
			return fail(err)
		}
	}
	body, err := proto.EncodeValue(build800(proto.Args(proto.S("61")), proto.Args(proto.S("61")), proto.GuidArr(guids)), s.Table())
	if err != nil {
		return fail(err)
	}
	rawReceipt, err := json.Marshal(storeReturnReceipt{sha, ctx.ExpectResponse, body})
	if err != nil {
		return fail(err)
	}
	if _, err := tx.ExecContext(c, `INSERT INTO kv(k,v) VALUES(?,?)`, receiptKey, string(rawReceipt)); err != nil {
		return fail(err)
	}
	if err := tx.Commit(); err != nil {
		return fail(err)
	}
	ctx.Sess.Push2_1()
	return body, true
}

// 忽略纯时间戳变化；数量、装配、耐久、标记等字段必须与购入时一致。
func sameStoreItemFields(row, original *store.Item) bool {
	a, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return false
	}
	b, err := FieldsFromJSON(original.FieldsJSON)
	if err != nil || row.WireClass != original.WireClass || len(a) != len(b) {
		return false
	}
	for _, cs := range []string{csCreated, csUpdated} {
		if i := fieldIdxByCS(row.WireClass, cs); i >= 0 && i < len(a) {
			a[i] = b[i]
		}
	}
	x, err := FieldsToJSON(a)
	if err != nil {
		return false
	}
	y, err := FieldsToJSON(b)
	return err == nil && x == y
}
