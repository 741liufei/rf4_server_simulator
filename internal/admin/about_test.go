package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rf4server/internal/config"
	"rf4server/internal/logging"
)

func TestAboutReadsLocalREADME(t *testing.T) {
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# 本地 README\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(config.Admin{JWTSecret: "test"}, testDB(t), logging.Discard(), nil)
	rec := httptest.NewRecorder()
	srv.handleAbout(rec, httptest.NewRequest(http.MethodGet, "/api/about", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("关于页状态 = %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Content, "# 本地 README") {
		t.Fatalf("应优先读取本地 README: %q", got.Content)
	}
}
