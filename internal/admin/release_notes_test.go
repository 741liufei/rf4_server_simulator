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

func TestReleaseNotesReadsPackageFile(t *testing.T) {
	srv := NewServer(config.Admin{JWTSecret: "test"}, testDB(t), logging.Discard(), nil)
	dir := t.TempDir()
	srv.releaseNotesPath = filepath.Join(dir, releaseNotesFilename)
	if err := os.WriteFile(srv.releaseNotesPath, []byte("# 发布说明\n\n- 本地内容"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/release-notes", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("未认证请求状态 = %d, 期望 401", rec.Code)
	}

	rec = httptest.NewRecorder()
	srv.handleReleaseNotes(rec, httptest.NewRequest(http.MethodGet, "/api/release-notes", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("读取发布说明状态 = %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Filename string `json:"filename"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Filename != releaseNotesFilename || !strings.Contains(got.Content, "本地内容") {
		t.Fatalf("发布说明内容异常: %+v", got)
	}
}

func TestReleaseNotesMissingFile(t *testing.T) {
	srv := NewServer(config.Admin{JWTSecret: "test"}, testDB(t), logging.Discard(), nil)
	srv.releaseNotesPath = filepath.Join(t.TempDir(), releaseNotesFilename)
	rec := httptest.NewRecorder()
	srv.handleReleaseNotes(rec, httptest.NewRequest(http.MethodGet, "/api/release-notes", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("缺失发布说明状态 = %d, 期望 404", rec.Code)
	}
}
