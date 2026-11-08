package gameserver

import (
	"bytes"
	"testing"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

// TestFishLandedKVText 上鱼播报 KV 文案：键1 为中文鱼名、键2 为重量克，
// 档位前缀由客户端本地化模板补（达标 kind3 无前缀）。
func TestFishLandedKVText(t *testing.T) {
	srv, db := newDetachTestServer(t)
	if _, err := db.SQL.Exec(`INSERT INTO loc_wiki_fish(loc,fish_key,fish_zh)
		VALUES('broadcast-test','g_tench','丁鱵')`); err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	b, err := proto.EncodeValue(srv.fishLandedKV(&RolledFish{Species: "g_tench", WeightG: 425}), sess.Table())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte("丁鱵")) || bytes.Contains(b, []byte("g_tench")) {
		t.Fatalf("键1 应为中文鱼名而非鱼种 key: %x", b)
	}
	// 重量 425g 原样进键2（客户端按克/公斤显示）。
	if !bytes.Contains(b, []byte{0xa9, 0x01, 0x00, 0x00}) {
		t.Fatalf("键2 重量应为 425 克: %x", b)
	}
	// 长度前缀必须是 UTF-8 字节数，而不是字符数。
	name := []byte("丁鱵")
	if prefix := append([]byte{0x01, 0x14, byte(len(name))}, name...); !bytes.Contains(b, prefix) {
		t.Fatalf("键1 UTF-8 长度前缀不符: %x", b)
	}
}

// 没有中文名时，键1 回退到鱼种 key。
func TestFishLandedKVFallbackUsesSpeciesKey(t *testing.T) {
	srv, _ := newDetachTestServer(t)
	sess := newProbeSess(srv)
	b, err := proto.EncodeValue(srv.fishLandedKV(&RolledFish{Species: "missing_species", WeightG: 425}), sess.Table())
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("missing_species")
	if prefix := append([]byte{0x01, 0x14, byte(len(key))}, key...); !bytes.Contains(b, prefix) {
		t.Fatalf("回退 key 长度前缀不符: %x", b)
	}
}

// TestFishTVShape30MatchesProduction 回归：面板 kv_shape=30 的编码字节必须与
// 生产 fishTVKv 完全一致（typed 数组 "135"）。否则面板探测的结果代表不了
// 线上行为——旧实现把 Items 重包成 KindKVList，编码器落到硬编码的
// dejdolfnooh 类型名，字节与生产分叉。
func TestFishTVShape30MatchesProduction(t *testing.T) {
	srv := NewServer(config.Game{}, logging.Discard(), nil)
	sess := newProbeSess(srv)
	table := sess.Table()

	prod, err := proto.EncodeValue(fishTVKv("g_tench", 3280), table)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := proto.EncodeValue(fishTVKvShape(30, "g_tench", 3280), table)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(prod, probe) {
		t.Fatalf("shape30 bytes differ from production fishTVKv:\n prod=%x\nprobe=%x", prod, probe)
	}
	if !bytes.Contains(probe, []byte("135")) {
		t.Fatalf("shape30 must use typed array type name 135, got %x", probe)
	}
}
