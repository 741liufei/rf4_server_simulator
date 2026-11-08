package gameserver

import (
	"context"
	"math"
	"testing"

	"rf4server/internal/proto"
)

func TestReelClipDefaultsOff(t *testing.T) {
	t.Parallel()
	rig := buildRig153("rig_FloatSimple", 1, nil, 100)
	if got := rigClipOf(rig); got != 0 {
		t.Fatalf("默认轮管卡应为关闭，实得 %d", got)
	}
}

func TestReelClipRoundTrip(t *testing.T) {
	t.Parallel()
	rig := buildRig153WithClip("rig_FloatSimple", 1, nil, 100, 42)
	if got := rigClipOf(rig); got != 42 {
		t.Fatalf("轮管卡读取失败，实得 %d", got)
	}
	if got := math.Float32frombits(castBaseDistanceBits(1)); math.Abs(float64(got-7.5461488)) > 1e-6 {
		t.Fatalf("轮管卡不应改变基准抛距，实得 %.7f", got)
	}
}

func TestParseRodGuidIntClipOff(t *testing.T) {
	t.Parallel()
	tree := proto.Args(proto.S("524"), proto.Guid("10000000000000000000000000000001"), proto.U32(0))
	guid, value := parseRodGuidInt(tree)
	if guid == "" || value != 0 {
		t.Fatalf("解析失败: guid=%q value=%d", guid, value)
	}
}

func TestReelClipRodSetting(t *testing.T) {
	t.Parallel()
	db := connectTestDB(t)
	srv := &Server{DB: db}
	guid := "10000000000000000000000000000001"
	if err := db.UpsertRodSetting(context.Background(), guid, reelClipSettingKey, "27"); err != nil {
		t.Fatal(err)
	}
	got, ok := srv.savedReelClipM(context.Background(), guid)
	if !ok || got != 27 {
		t.Fatalf("轮管卡设置读取失败: got=%d ok=%v", got, ok)
	}
}

func TestReelClipRestoresMissingRigState(t *testing.T) {
	t.Parallel()
	db := connectTestDB(t)
	srv := &Server{DB: db}
	seedShopRodDef(t, db)
	insertBareRod(t, db, testUID, testShopRodGUID, "feeder_10126_FD390", 5559)
	ctx := context.Background()
	if err := db.UpsertRodSetting(ctx, testShopRodGUID, reelClipSettingKey, "33"); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetItemByGUID(ctx, testShopRodGUID)
	if err != nil || row == nil {
		t.Fatalf("读取竿实例失败: %v", err)
	}
	if got := rigClipOf(srv.defaultRig153(ctx, row)); got != 33 {
		t.Fatalf("缺失 f5 时轮管卡未恢复，实得 %d", got)
	}
	if !srv.rodRigSetClipRow(ctx, row, 18) {
		t.Fatal("写入竿实例轮管卡失败")
	}
	row, err = db.GetItemByGUID(ctx, testShopRodGUID)
	if err != nil || row == nil {
		t.Fatalf("重读竿实例失败: %v", err)
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	idx := fieldIdxByCS(row.WireClass, csRigObject)
	if idx < 0 || idx >= len(fields) || rigClipOf(fields[idx].Val) != 18 {
		t.Fatal("竿实例 f5 未持久化轮管卡")
	}
}
