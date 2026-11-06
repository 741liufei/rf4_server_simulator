package gameserver

import (
	"context"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

// TestFishStats321FromCatchLog 验证 3/2 统计由玩家 catch_log 实时生成。
func TestFishStats321FromCatchLog(t *testing.T) {
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	insert := func(loc string, spot int64, key string, weight, grade, xp, silver int64, bait string) {
		t.Helper()
		if err := db.CatchLogInsert(ctx, uid, loc, spot, key, "", weight, 0.2, "catch",
			bait, grade, xp, silver, "level_001_lake"); err != nil {
			t.Fatal(err)
		}
	}
	insert("loc1_02", 1, "c.roach", 186, 1, 35, 0, "worm_01")
	insert("loc1_02", 2, "bream", 650, 3, 260, 42, "worm_01")
	insert("loc1_02", 3, "c.roach", 500, 2, 100, 10, "spinnerbaits_1103")
	insert("loc1_02", 4, "perch", 800, 4, 300, 100, "worm_01")

	sess := newProbeSess(srv)
	body, ok := hFishStats(&Ctx{Sess: sess, Main: 3, Sub: 2, DecodeOK: true, Log: logging.Discard()})
	if !ok || len(body) == 0 {
		t.Fatal("3/2 未处理")
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatalf("3/2 响应不可解码: %v", err)
	}
	if dec.Class != "jahbacemfof" {
		t.Fatalf("3/2 应为 jahbacemfof，实得 %s", dec.Class)
	}
	f0 := dec.Field(0)
	if got := f0.U32(); got != 4 {
		t.Fatalf("f0 总钓获=%d，期望4", got)
	}
	f1 := dec.Field(1)
	if got := f1.U32(); got != 2 {
		t.Fatalf("f1 稀有=%d，期望2（grade>=3）", got)
	}
	f2 := dec.Field(2)
	if got := f2.U32(); got != 1 {
		t.Fatalf("f2 蓝冠=%d，期望1（grade>=4）", got)
	}
	f4 := dec.Field(4)
	if got := f4.U32(); got != 4 {
		t.Fatalf("f4 单日数量纪录=%d，期望4", got)
	}
	// f5 客户端 DTO cpenllldpnk 是 float（kg），必须按 F32 位型解码。
	f5 := dec.Field(5)
	if got := float64(proto.F32FromBits(f5.U32())); got < 2.1359 || got > 2.1361 {
		t.Fatalf("f5 单日重量纪录=%f kg，期望2.136", got)
	}
	// f6 鱼列表：每鱼种最重一张 320 卡，按重量降序（c.roach 500 / bream 650 / perch 800）。
	fishList := dec.Field(6).Val
	if got := fishList.ArgCount(); got != 3 {
		t.Fatalf("f6 鱼列表条数=%d，期望3（每鱼种一张卡）", got)
	}
	if got := fishList.Items[0].Field(0).Str; got == nil || *got != "perch" {
		t.Fatalf("f6 首卡鱼种=%v，期望 perch（800g 最重）", got)
	}
	if got := fishList.Items[0].Field(2).Num; got == nil || *got != 800 {
		t.Fatalf("f6 首卡重量=%v，期望800", got)
	}
	if got := fishList.Items[2].Field(0).Str; got == nil || *got != "c.roach" {
		t.Fatalf("f6 末卡鱼种=%v，期望 c.roach（500g 最轻）", got)
	}
	counts := []struct {
		name                       string
		total, trophy, superTrophy int64
	}{
		{"perch", 1, 1, 1},
		{"bream", 1, 1, 0},
		{"c.roach", 2, 0, 0},
	}
	for i, want := range counts {
		card := fishList.Items[i]
		nameField := card.Field(0)
		if got := nameField.StrOr(); got != want.name {
			t.Fatalf("f6[%d] 鱼种=%q，期望 %q", i, got, want.name)
		}
		totalField := card.Field(9)
		if got := int64(totalField.U32()); got != want.total {
			t.Fatalf("f6[%d] %s 总钓获=%d，期望 %d", i, want.name, got, want.total)
		}
		trophyField := card.Field(10)
		if got := int64(trophyField.U32()); got != want.trophy {
			t.Fatalf("f6[%d] %s 奖杯数=%d，期望 %d", i, want.name, got, want.trophy)
		}
		superTrophyField := card.Field(11)
		if got := int64(superTrophyField.U32()); got != want.superTrophy {
			t.Fatalf("f6[%d] %s 超稀有数=%d，期望 %d", i, want.name, got, want.superTrophy)
		}
	}
	card := dec.Field(7).Val
	if card == nil || card.Class != "ippcfiphgfl" {
		t.Fatalf("f7 最大渔获卡缺失: %#v", card)
	}
	cardFish := card.Field(0)
	if got := cardFish.StrOr(); got != "perch" {
		t.Fatalf("记录卡鱼种=%q，期望 perch", got)
	}
	cardWeight := card.Field(2)
	if got := cardWeight.U32(); got != 800 {
		t.Fatalf("记录卡重量=%d，期望800g", got)
	}
	f14 := dec.Field(14).Val.Field(2)
	if got := f14.U32(); got != 800 {
		t.Fatalf("f14 浮钓最大=%d，期望800g", got)
	}
	f15 := dec.Field(15).Val.Field(2)
	if got := f15.U32(); got != 500 {
		t.Fatalf("f15 路亚最大=%d，期望500g", got)
	}
	if got := dec.Field(17).Val.ArgCount(); got != 2 {
		t.Fatalf("f17 竿类统计项=%d，期望2（浮钓/纺车）", got)
	}
	if got := dec.Field(21).Val.ArgCount(); got != 2 {
		t.Fatalf("f21 诱饵统计项=%d，期望2", got)
	}
}

func TestRecordCardDefaultsAndCategory81(t *testing.T) {
	// category 81 只有 rengine_1/rengine_2（渔轮电机）。渔轮 DTO 里 f14
	// dfjpnambjcj 声明的类型是 odlknjlbooh，对应 wire 类 kbpmmclfipg/1470；
	// 原来的 dbjjakbbbfc/12601 会让客户端解出未知类型，电机槽无法使用。
	if got := categoryWireClass[81]; got.Class != "kbpmmclfipg" || got.TypeID != 1470 {
		t.Fatalf("category 81 映射=%+v，期望 kbpmmclfipg/1470", got)
	}
	card := recordCard320(catchStatRow{
		TS: 1.25, FishKey: "roach", Grade: 0, WeightG: 100,
		LengthM: 0.2, Bait: "worm", LocLevel: "level",
	}, true)
	if card == nil {
		t.Fatal("recordCard320 不应返回 nil")
	}
	gradeField := card.Field(1)
	if got := gradeField.U32(); got != 1 {
		t.Fatalf("grade=0 时 f1=%d，期望默认值 1", got)
	}
	categoryField := card.Field(12)
	if got := categoryField.U32(); got != 1 {
		t.Fatalf("grade=0 时 f12=%d，期望默认值 1", got)
	}
}
