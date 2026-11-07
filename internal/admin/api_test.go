package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/store"
)

func testDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("打开测试 SQLite: %v", err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestAdminAPI 全 API 流程：登录 → 仪表盘 → 调参 → 玩家列表/改数据/发物品/发邮件 → 审计。
func TestAdminAPI(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	_ = db.EnsureAdmin(context.Background(), "admin", "admin123")
	uid := int64(1000001)
	_, _ = db.EnsureProfile(context.Background(), uid)

	cfg := config.Admin{HTTPAddr: "127.0.0.1:0", JWTSecret: "test"}
	srv := NewServer(cfg, db, logging.Discard(), WebFS())
	errCh, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + srv.Addr()
	client := &http.Client{Timeout: 5 * time.Second}

	call := func(method, path, token, body string) (int, string) {
		var rd *bytes.Reader
		if body == "" {
			rd = bytes.NewReader(nil)
		} else {
			rd = bytes.NewReader([]byte(body))
		}
		r, _ := http.NewRequest(method, base+path, rd)
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return resp.StatusCode, buf.String()
	}

	// 错误密码
	code, _ := call("POST", "/api/login", "", `{"username":"admin","password":"wrong"}`)
	if code != 401 {
		t.Fatalf("错误密码应 401, 实得 %d", code)
	}
	// 登录
	code, body := call("POST", "/api/login", "", `{"username":"admin","password":"admin123"}`)
	if code != 200 {
		t.Fatalf("登录应 200: %d %s", code, body)
	}
	var lr struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal([]byte(body), &lr)
	token := lr.Token

	// 内嵌前端可达
	code, _ = call("GET", "/", token, "")
	if code != 200 {
		t.Fatalf("内嵌前端应 200, 实得 %d", code)
	}

	// 未认证拒绝
	code, _ = call("GET", "/api/dashboard", "", "")
	if code != 401 {
		t.Fatalf("未认证应 401, 实得 %d", code)
	}

	// 仪表盘
	code, body = call("GET", "/api/dashboard", token, "")
	if code != 200 || !strings.Contains(body, "players") {
		t.Fatalf("仪表盘异常: %d %s", code, body)
	}

	// 关于页：README 由二进制内嵌并提供给管理后台
	code, body = call("GET", "/api/about", token, "")
	if code != 200 {
		t.Fatalf("关于页应 200, 实得 %d: %s", code, body)
	}
	var about struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(body), &about); err != nil || !strings.Contains(about.Content, "# rf4_server_simulator") {
		t.Fatalf("README 内容异常: %v %s", err, body)
	}

	// 调参
	if code, _ = call("POST", "/api/tunables", token, `{"key":"bite_probability","value":"0.5"}`); code != 200 {
		t.Fatalf("调参失败 %d", code)
	}
	v, ok, _ := db.TunableGet(context.Background(), "bite_probability")
	if !ok || v != "0.5" {
		t.Fatalf("参数未落库: %q", v)
	}
	_ = db.TunableSet(context.Background(), "bite_probability", "1.0", "test")

	// 玩家列表
	code, body = call("GET", "/api/players?q=1000001", token, "")
	if code != 200 || !strings.Contains(body, `"uid":1000001`) {
		t.Fatalf("玩家列表异常: %d %s", code, body)
	}

	// 改数据
	if code, _ = call("POST", "/api/players/update", token, `{"uid":1000001,"silver":77777}`); code != 200 {
		t.Fatal("改数据失败")
	}
	p, _ := db.GetProfile(context.Background(), uid)
	if p.Silver != 77777 {
		t.Fatalf("银币未生效: %d", p.Silver)
	}

	// 清空队列（前面调参步骤投递过 reload_tunables）
	for {
		_, ok, _ := db.QueuePop(context.Background())
		if !ok {
			break
		}
	}

	// 发物品（指令入共享队列）
	if code, _ = call("POST", "/api/players/grant", token, `{"uid":1000001,"item_id":"worm_01","qty":5}`); code != 200 {
		t.Fatal("发物品请求失败")
	}
	if raw, ok, _ := db.QueuePop(context.Background()); !ok || !strings.Contains(raw, "grant_item") {
		t.Fatalf("发物品指令未入队: %q", raw)
	}

	// 发邮件
	if code, _ = call("POST", "/api/mail/send", token, `{"uid":1000001,"text":"测试","items":[{"item_id":"worm_01","qty":3}]}`); code != 200 {
		t.Fatal("发邮件请求失败")
	}
	if raw, ok, _ := db.QueuePop(context.Background()); !ok || !strings.Contains(raw, "send_mail") {
		t.Fatalf("发邮件指令未入队: %q", raw)
	}

	// 封禁/解封
	if code, _ = call("POST", "/api/players/ban", token, `{"uid":1000001,"banned":true}`); code != 200 {
		t.Fatal("封禁失败")
	}
	banned, _ := db.IsBanned(context.Background(), uid)
	if !banned {
		t.Fatal("封禁未落库")
	}
	_ = db.SetBanned(context.Background(), uid, false)

	// 审计
	code, body = call("GET", "/api/audit?limit=10", token, "")
	if code != 200 || !strings.Contains(body, "grant_item") {
		t.Fatalf("审计缺失: %s", body)
	}

	select {
	case err := <-errCh:
		t.Fatalf("服务异常退出: %v", err)
	default:
	}
}
