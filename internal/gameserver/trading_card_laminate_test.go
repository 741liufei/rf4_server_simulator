package gameserver

import (
	"sync"
	"testing"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// 27/4 塑封：集齐才可塑封、f1=FILETIME 密封时间戳、奖座幂等补发、
// 塑封后入册/取回被拒；ACK 为 llgjfoldbcc/obj-809。
func TestTradingCardLaminate(t *testing.T) {
	s, db := newProgressSrv(t)
	seedTradingCards(t, db)
	// 补奖座定义与属性
	if _, err := db.SQL.Exec(`INSERT OR REPLACE INTO item_def(numeric_id,item_id,category,attrs_json)
		VALUES(60004,'statuette_test',72,'{"jnkpgolkkdj":4,"fjfejmomhed":21}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE item_def SET attrs_json=json_set(attrs_json,'$.pnaapkplkfo',json('["statuette_test"]'))
		WHERE item_id='tcdeck_test'`); err != nil {
		t.Fatal(err)
	}
	c := t.Context()
	perch, err := s.CreateItem(c, 7, "card_perch", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	crucian, err := s.CreateItem(c, 7, "card_crucian", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	addCtx := tradingAlbumRequest(t, s, 2, 700, proto.Str(proto.S("tcdeck_test")), proto.Guid(zeroGUID), proto.Guid(perch.GUID))
	runTradingAlbumRequest(t, s, addCtx, false)
	deckGUID := deckRowGUID(t, db, 7, "tcdeck_test")
	// 未集齐：塑封被拒
	lamCtx := tradingAlbumRequest(t, s, 4, 701, proto.Guid(deckGUID))
	runTradingAlbumRequest(t, s, lamCtx, true)
	// 集齐第二张 → 塑封成功
	addCtx = tradingAlbumRequest(t, s, 2, 702, proto.Str(proto.S("tcdeck_test")), proto.Guid(zeroGUID), proto.Guid(crucian.GUID))
	runTradingAlbumRequest(t, s, addCtx, false)
	lamCtx = tradingAlbumRequest(t, s, 4, 703, proto.Guid(deckGUID))
	body := runLaminateRequest(t, s, lamCtx)
	v, _, err := proto.DecodeValue(body, 0, s.Table())
	if err != nil {
		t.Fatal(err)
	}
	if v.Kind != proto.KindObj || v.TypeID != 809 || len(v.Fields) != 4 {
		t.Fatalf("塑封 ACK 应为 llgjfoldbcc(obj-809, 4 字段): %+v", v)
	}
	if f1 := v.Fields[1].Val; f1.Kind != proto.KindArgs || len(f1.Items) != 1 {
		t.Fatalf("f1 应含奖座树: %+v", f1)
	}
	// 数组字段恒发空数组（仓库硬约束）：f2=空 args(61)、f3=空 GuidArr
	if f2 := v.Fields[2].Val; f2.Kind != proto.KindArgs || f2.Code == nil || *f2.Code != "61" || len(f2.Items) != 0 {
		t.Fatalf("f2 应为空 args(61) 数组: %+v", f2)
	}
	if f3 := v.Fields[3].Val; f3.Kind != proto.KindGuidArr || len(f3.Items) != 0 {
		t.Fatalf("f3 应为空 GuidArr: %+v", f3)
	}
	// f1 时间戳 = FILETIME（约 1.3e17 量级；.NET Ticks 是 6.4e17 量级，
	// 发错单位客户端 FromFileTime 会渲染成 +1600 年）
	deck, err := db.GetItemByGUID(c, deckGUID)
	if err != nil {
		t.Fatal(err)
	}
	fields, _ := FieldsFromJSON(deck.FieldsJSON)
	if ts := deckSealed(fields); ts < 130_000_000_000_000_000 {
		t.Fatalf("密封时间戳应为 FILETIME: %d", ts)
	}
	// 奖座已发一座且不重复
	if n := countRows(t, db, 7, "statuette_test"); n != 1 {
		t.Fatalf("奖座应恰一座: %d", n)
	}
	// 塑封后：取回被拒、再入册被拒、重复塑封被拒
	rmCtx := tradingAlbumRequest(t, s, 3, 704, proto.Guid(perch.GUID))
	runTradingAlbumRequest(t, s, rmCtx, true)
	another, err := s.CreateItem(c, 7, "card_perch", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	addCtx = tradingAlbumRequest(t, s, 2, 705, proto.Str(proto.S("tcdeck_test")), proto.Guid(zeroGUID), proto.Guid(another.GUID))
	runTradingAlbumRequest(t, s, addCtx, true)
	lamCtx = tradingAlbumRequest(t, s, 4, 706, proto.Guid(deckGUID))
	runTradingAlbumRequest(t, s, lamCtx, true)
	if n := countRows(t, db, 7, "statuette_test"); n != 1 {
		t.Fatalf("重复塑封不得再发奖座: %d", n)
	}
}

func deckRowGUID(t *testing.T, db *store.DB, uid int64, itemID string) string {
	t.Helper()
	rows, err := db.ListItemsByContainer(t.Context(), uid, tradingCardContainer)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ItemID == itemID {
			return r.GUID
		}
	}
	t.Fatalf("卡册行缺失: %s", itemID)
	return ""
}

func countRows(t *testing.T, db *store.DB, uid int64, itemID string) int {
	t.Helper()
	var n int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM item WHERE uid=? AND item_id=?`, uid, itemID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// runLaminateRequest 执行 27/4 并断言成功响应（809 形状由具体用例校验）。
func runLaminateRequest(t *testing.T, s *Server, ctx *Ctx) []byte {
	t.Helper()
	result := s.dispatch.executeRequest(ctx, true)
	if result.errorText != "" {
		if fv, _, derr := proto.DecodeValue(result.body, 0, s.Table()); derr == nil {
			var parts []string
			for _, f := range fv.Fields {
				if f.Str != nil {
					parts = append(parts, *f.Str)
				}
			}
			t.Fatalf("laminate error: %s parts=%v", result.errorText, parts)
		}
		t.Fatalf("laminate error: %s native=%v", result.errorText, result.nativeFailure)
	}
	if !result.hasResponse || result.failed {
		t.Fatalf("laminate response: %+v", result)
	}
	return result.body
}

// 奖座可被 4/6 移到房屋等非背包容器；塑封 ACK 必须返回实际已拥有行的 GUID，
// 不能返回事务外预构造但最终未落库的幽灵奖励。
func TestTradingCardLaminateExistingStatuetteOutsideBackpack(t *testing.T) {
	s, db := newProgressSrv(t)
	seedTradingCards(t, db)
	if _, err := db.SQL.Exec(`INSERT OR REPLACE INTO item_def(numeric_id,item_id,category,attrs_json)
		VALUES(60004,'statuette_test',72,'{"jnkpgolkkdj":4,"fjfejmomhed":21}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE item_def SET attrs_json=json_set(attrs_json,'$.pnaapkplkfo',json('["statuette_test"]'))
		WHERE item_id='tcdeck_test'`); err != nil {
		t.Fatal(err)
	}
	c := t.Context()
	perch, err := s.CreateItem(c, 7, "card_perch", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	crucian, err := s.CreateItem(c, 7, "card_crucian", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	addCtx := tradingAlbumRequest(t, s, 2, 710, proto.Str(proto.S("tcdeck_test")), proto.Guid(zeroGUID), proto.Guid(perch.GUID))
	runTradingAlbumRequest(t, s, addCtx, false)
	addCtx = tradingAlbumRequest(t, s, 2, 711, proto.Str(proto.S("tcdeck_test")), proto.Guid(zeroGUID), proto.Guid(crucian.GUID))
	runTradingAlbumRequest(t, s, addCtx, false)
	deckGUID := deckRowGUID(t, db, 7, "tcdeck_test")
	trophy, err := s.CreateItem(c, 7, "statuette_test", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE item SET container='house' WHERE guid=?`, trophy.GUID); err != nil {
		t.Fatal(err)
	}
	body := runLaminateRequest(t, s, tradingAlbumRequest(t, s, 4, 712, proto.Guid(deckGUID)))
	v, _, err := proto.DecodeValue(body, 0, s.Table())
	if err != nil {
		t.Fatal(err)
	}
	if got := findTradingTreeGUID(v.Fields[1].Val); got != trophy.GUID {
		t.Fatalf("塑封 ACK 奖座 GUID=%s want 已拥有的 %s", got, trophy.GUID)
	}
	if n := countRows(t, db, 7, "statuette_test"); n != 1 {
		t.Fatalf("已有奖座不得重发: %d", n)
	}
	row, err := db.GetItemByGUID(c, trophy.GUID)
	if err != nil || row.Container != "house" {
		t.Fatalf("已有奖座容器被改变: %v %+v", err, row)
	}
}

func findTradingTreeGUID(v *proto.Value) string {
	if v == nil {
		return ""
	}
	if v.Kind == proto.KindObj {
		if idx := fieldIdxByCS(v.Class, csGUID); idx >= 0 && idx < len(v.Fields) {
			return v.Fields[idx].Hex
		}
		for _, f := range v.Fields {
			if guid := findTradingTreeGUID(f.Val); guid != "" {
				return guid
			}
		}
	}
	for _, item := range v.Items {
		if guid := findTradingTreeGUID(item); guid != "" {
			return guid
		}
	}
	return ""
}

// 不同 call_id 的并发塑封必须只有一个请求能发奖座；事务内复查是幂等防线。
func TestTradingCardLaminateConcurrent(t *testing.T) {
	s, db := newProgressSrv(t)
	seedTradingCards(t, db)
	if _, err := db.SQL.Exec(`INSERT OR REPLACE INTO item_def(numeric_id,item_id,category,attrs_json)
		VALUES(60004,'statuette_test',72,'{"jnkpgolkkdj":4,"fjfejmomhed":21}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE item_def SET attrs_json=json_set(attrs_json,'$.pnaapkplkfo',json('["statuette_test"]'))
		WHERE item_id='tcdeck_test'`); err != nil {
		t.Fatal(err)
	}
	c := t.Context()
	for i := 0; i < 20; i++ {
		if _, err := db.SQL.Exec(`DELETE FROM item WHERE uid=7`); err != nil {
			t.Fatal(err)
		}
		perch, err := s.CreateItem(c, 7, "card_perch", "backpack", 1)
		if err != nil {
			t.Fatal(err)
		}
		crucian, err := s.CreateItem(c, 7, "card_crucian", "backpack", 1)
		if err != nil {
			t.Fatal(err)
		}
		addCtx := tradingAlbumRequest(t, s, 2, uint32(720+i*10), proto.Str(proto.S("tcdeck_test")), proto.Guid(zeroGUID), proto.Guid(perch.GUID))
		runTradingAlbumRequest(t, s, addCtx, false)
		addCtx = tradingAlbumRequest(t, s, 2, uint32(721+i*10), proto.Str(proto.S("tcdeck_test")), proto.Guid(zeroGUID), proto.Guid(crucian.GUID))
		runTradingAlbumRequest(t, s, addCtx, false)
		deckGUID := deckRowGUID(t, db, 7, "tcdeck_test")
		start := make(chan struct{})
		var wg sync.WaitGroup
		for j := 0; j < 2; j++ {
			ctx := tradingAlbumRequest(t, s, 4, uint32(722+i*10+j), proto.Guid(deckGUID))
			wg.Add(1)
			go func(ctx *Ctx) {
				defer wg.Done()
				<-start
				_ = s.dispatch.executeRequest(ctx, true)
			}(ctx)
		}
		close(start)
		wg.Wait()
		if n := countRows(t, db, 7, "statuette_test"); n != 1 {
			t.Fatalf("iteration %d: concurrent laminate granted %d statuettes", i, n)
		}
	}
}
