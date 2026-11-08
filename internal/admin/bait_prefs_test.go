package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"rf4server/internal/config"
	"rf4server/internal/logging"
)

// 鱼饵偏好页：规则表条目与饵效证据必须都出现在响应里。
func TestBaitPrefsAPI(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := db.EnsureAdmin(ctx, "admin", "admin123"); err != nil {
		t.Fatal(err)
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.SQL.Exec(q, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	// c.roach 的中文名用规则表里的"常见拟鲤"，可命中 252 条规则之一。
	mustExec(`INSERT INTO loc_wiki_fish(loc,fish_key,fish_zh,in_map_roster,in_habitat) VALUES('铜湖','c.roach','常见拟鲤',1,1)`)
	// 一条规则表里没有条目的鱼（虚构中文名，确保不命中 252 条规则）。
	mustExec(`INSERT INTO loc_wiki_fish(loc,fish_key,fish_zh,in_map_roster,in_habitat) VALUES('铜湖','atl_cod','测试鱼XYZ',1,1)`)
	mustExec(`INSERT INTO companion_bait_fish(bait_id,fish_key,tier,evidence) VALUES('worm_01','c.roach',3,900),('maggot','c.roach',2,120),('fly','c.roach',1,10)`)
	mustExec(`INSERT INTO companion_bait_fish(bait_id,fish_key,tier,evidence) VALUES('pilkerlures_2691','atl_cod',3,77)`)

	cfg := config.Admin{HTTPAddr: "127.0.0.1:0", JWTSecret: "test"}
	srv := NewServer(cfg, db, logging.Discard(), WebFS())
	if _, err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	base := "http://" + srv.Addr()
	client := &http.Client{Timeout: 5 * time.Second}

	token := func() string {
		r, _ := http.NewRequest("POST", base+"/api/login", strings.NewReader(`{"username":"admin","password":"admin123"}`))
		r.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out struct {
			Token string `json:"token"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		if out.Token == "" {
			t.Fatal("登录未拿到 token")
		}
		return out.Token
	}()

	get := func(path string) map[string]any {
		t.Helper()
		r, _ := http.NewRequest("GET", base+path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("解码失败: %v", err)
		}
		return out
	}

	out := get("/api/bait/prefs")
	if rc, _ := out["rule_count"].(float64); rc < 200 {
		t.Fatalf("规则表条目数异常: %v", out["rule_count"])
	}
	rows, _ := out["rows"].([]any)
	if len(rows) != 2 {
		t.Fatalf("应返回 2 条鱼，实得 %d", len(rows))
	}
	find := func(key string) map[string]any {
		for _, it := range rows {
			m := it.(map[string]any)
			if m["fish_key"] == key {
				return m
			}
		}
		t.Fatalf("响应里没有 %s", key)
		return nil
	}

	roach := find("c.roach")
	if roach["has_rule"] != true {
		t.Fatal("常见拟鲤 应命中规则表")
	}
	if allow, _ := roach["allow"].([]any); len(allow) == 0 {
		t.Fatal("规则允许列表为空")
	}
	if deny, _ := roach["deny"].([]any); len(deny) == 0 {
		t.Fatal("规则应含'不吃拟饵/饵鱼类'")
	}
	if t3, _ := roach["tier3"].(float64); t3 != 1 {
		t.Fatalf("c.roach tier3 应为 1，实得 %v", roach["tier3"])
	}
	if roach["in_strict"] != true {
		t.Fatal("有规则的鱼严格模式应可咬")
	}

	cod := find("atl_cod")
	if cod["has_rule"] != false {
		t.Fatalf("虚构鱼名不应命中规则表，实得 allow=%v", cod["allow"])
	}
	if t3, _ := cod["tier3"].(float64); t3 != 1 {
		t.Fatalf("atl_cod tier3 应为 1，实得 %v", cod["tier3"])
	}
	if cod["in_strict"] != true {
		t.Fatal("无规则但有 tier 证据的鱼，严格模式仍应可咬")
	}

	// 搜索过滤
	if got := get("/api/bait/prefs?q=pike"); len(got["rows"].([]any)) != 0 {
		t.Fatal("搜索 pike 不应命中任何鱼")
	}
	if got := get("/api/bait/prefs?loc=铜湖"); len(got["rows"].([]any)) != 2 {
		t.Fatal("水域过滤应返回 2 条")
	}
	_ = time.Now
}
