package admin

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/store"
)

func TestAdminFishRecordsRestore(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	for _, uid := range []int64{7, 8} {
		if _, err := db.EnsureProfile(ctx, uid); err != nil {
			t.Fatal(err)
		}
	}
	for _, sql := range []string{
		`INSERT INTO loc_alias(alias,loc) VALUES('level_001_lake','湖')`,
		`INSERT INTO map_fish_roster(loc,fish_key) VALUES('level_001_lake','perch')`,
		`INSERT INTO loc_wiki_fish(loc,fish_key,fish_zh) VALUES('湖','perch','河鲈')`,
	} {
		if _, err := db.SQL.Exec(sql); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.CatchLogInsert(ctx, 7, "loc1_02", 0, "perch", "河鲈", 200, 0.2, "keep", "worm_01", 2, 40, 20, "level_001_lake"); err != nil {
		t.Fatal(err)
	}
	if err := db.CatchLogInsert(ctx, 8, "loc1_02", 0, "perch", "河鲈", 300, 0.2, "keep", "", 2, 0, 0, "level_001_lake"); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(config.Admin{JWTSecret: "test-records"}, db, logging.Discard(), WebFS())
	tok, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{Username: "record-tester", RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}).SignedString(srv.jwtKey)
	call := func(method, path, body string, auth bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if auth {
			r.Header.Set("Authorization", "Bearer "+tok)
		}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		return w
	}
	body := `{"uid":7,"level":"level_001_lake","changes":[{"fish_key":"perch","grade":4}]}`
	for _, method := range []string{"GET", "POST"} {
		if w := call(method, "/api/players/records?uid=7", body, false); w.Code != 401 {
			t.Fatalf("authentication: %d", w.Code)
		}
	}
	w := call("POST", "/api/players/records", strings.Replace(body, "perch", "unknown", 1), true)
	if w.Code != 400 {
		t.Fatalf("map validation: %d %s", w.Code, w.Body.String())
	}
	w = call("POST", "/api/players/records", body, true)
	if w.Code != 200 {
		_, backupErr := db.BackupRecords(ctx)
		t.Fatalf("restore: %d %s backup=%v", w.Code, w.Body.String(), backupErr)
	}
	var saved struct {
		Backup string `json:"backup"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil || saved.Backup == "" {
		t.Fatal("missing backup", err)
	}
	backup, err := store.Open(filepath.Join(filepath.Dir(db.Path), "backups", "records", saved.Backup))
	if err != nil {
		t.Fatal(err)
	}
	before, err := backup.PlayerCatchRecords(ctx, 7)
	_ = backup.Close()
	if err != nil || len(before) != 1 || before[0].WeightG != 200 {
		t.Fatalf("online backup must retain committed pre-change catch: %+v %v", before, err)
	}
	// The same save replaces the imported row and leaves real catches intact.
	if w = call("POST", "/api/players/records", body, true); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	records, err := db.PlayerCatchRecords(ctx, 7)
	if err != nil || len(records) != 2 {
		t.Fatalf("duplicate rows: %+v %v", records, err)
	}
	if records[0].Source != "keep" || records[0].XP != 40 || records[1].Grade != 4 || records[1].XP != 0 || records[1].Silver != 0 {
		t.Fatalf("records: %+v", records)
	}
	other, err := db.PlayerCatchRecords(ctx, 8)
	if err != nil || len(other) != 1 || other[0].WeightG != 300 {
		t.Fatalf("other player: %+v %v", other, err)
	}
	w = call("GET", "/api/players/records?uid=7&level=level_001_lake", "", true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"restored_grade":4`) {
		t.Fatal(w.Body.String())
	}
	// Clearing the supplemental record must not remove the natural catch.
	w = call("POST", "/api/players/records", strings.Replace(body, `"grade":4`, `"grade":0`, 1), true)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	records, _ = db.PlayerCatchRecords(ctx, 7)
	if len(records) != 1 || records[0].Source != "keep" {
		t.Fatalf("natural catch lost: %+v", records)
	}
}
