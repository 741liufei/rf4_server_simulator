package gameserver

import (
	"strings"
	"testing"
)

func TestFishQuerySpeciesPinyinPrefix(t *testing.T) {
	s, db := seedBaitWeightTest(t)
	// Insert the later key first; repeated names may differ only by a BOM.
	for _, row := range [][3]string{
		{"prefix-test", "z.orfe", "圆腹测试鱼"},
		{"prefix-test", "s.orfe", "圆腹雅罗鱼"},
		{"prefix-duplicate", "s.orfe", "圆腹雅罗鱼\uFEFF"},
		{"prefix-bom", "s.orfe", "\uFEFF 圆腹雅罗鱼 \uFEFF"},
		{"prefix-test", "a.roach_variant", "常见拟鲤测试鱼"},
	} {
		if _, err := db.SQL.Exec(`INSERT INTO loc_wiki_fish(loc,fish_key,fish_zh) VALUES(?,?,?)`, row[0], row[1], row[2]); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		input, want string
	}{
		{"圆腹", "s.orfe"},
		{"元腹", "s.orfe"},
		{"圆副", "s.orfe"},
		{"yuanfu", "s.orfe"},
		{"YUANFUYALUOYU", "s.orfe"},
		{"圆fu", "s.orfe"},
		{"\uFEFF 圆腹 \uFEFF", "s.orfe"},
		{"圆腹雅罗鱼", "s.orfe"},
		{"s.orfe", "s.orfe"},
		{"圆腹测试鱼", "z.orfe"},
		{"常见", "a.roach_variant"},
		{"常见拟鲤", "c.roach"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			if got, err := s.resolveFishQuerySpecies(t.Context(), tc.input); err != nil || got != tc.want {
				t.Fatalf("resolve(%q) = %q, %v; want %q", tc.input, got, err, tc.want)
			}
		})
	}
	for _, input := range []string{"", " \uFEFF ", "%", "圆腹%", "雅罗", "腹测试", "yaluoyu", "yfy", "c.ro", "不存在"} {
		t.Run("missing/"+input, func(t *testing.T) {
			if got, err := s.resolveFishQuerySpecies(t.Context(), input); err == nil || got != "" {
				t.Fatalf("non-prefix %q matched %q: %v", input, got, err)
			}
		})
	}
}

func TestFishQueryPinyinReply(t *testing.T) {
	s, db := seedBaitWeightTest(t)
	const uid int64 = 1000001
	const mapKey = "level_015_tunguska"
	if _, err := db.SQL.Exec(`UPDATE loc_alias SET alias=? WHERE alias='level_900_rules'`, mapKey); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE loc_wiki_fish SET fish_zh=? WHERE fish_key='c.roach'`, "常见拟鲤\uFEFF"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.EnsureProfile(t.Context(), uid); err != nil {
		t.Fatal(err)
	}
	s.setLevel(uid, mapKey)
	sess := &Session{Srv: s, Userid: uid}
	for _, input := range []string{"常见", "changjian", "常见 rules-test", "CHANGJIAN " + mapKey} {
		t.Run(input, func(t *testing.T) {
			got := s.fishQueryReply(t.Context(), sess, input)
			if !strings.HasPrefix(got, "rules-test · 常见拟鲤\n1. ") {
				t.Fatalf("prefix query did not return the full fish name: %q", got)
			}
		})
	}
	if !strings.Contains(fishQueryHelp, "拼音前缀") || !strings.Contains(fishQueryHelp, "第一种") {
		t.Fatalf("help does not explain prefix matching: %s", fishQueryHelp)
	}
}
