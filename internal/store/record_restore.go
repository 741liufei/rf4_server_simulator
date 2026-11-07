package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	sqlite "modernc.org/sqlite"
)

const restoredRecordSource = "admin_record_restore"

type RecordMap struct {
	Level string `json:"level"`
	Name  string `json:"name"`
}

type RecordFish struct {
	Key           string `json:"fish_key"`
	Name          string `json:"name"`
	Grade         int64  `json:"grade"`
	WeightG       int64  `json:"weight_g"`
	NaturalGrade  int64  `json:"natural_grade"`
	RestoredGrade int64  `json:"restored_grade"`
	RestoredG     int64  `json:"restored_weight_g"`
}

func (d *DB) RecordMaps(ctx context.Context) ([]RecordMap, error) {
	rows, err := d.SQL.QueryContext(ctx, `SELECT level,name FROM (
		SELECT alias level,loc name FROM loc_alias WHERE alias LIKE 'level_%' AND
		(EXISTS(SELECT 1 FROM map_fish_roster r WHERE r.loc=loc_alias.alias OR r.loc=loc_alias.loc)
		OR EXISTS(SELECT 1 FROM loc_wiki_fish r WHERE r.loc=loc_alias.loc))
		UNION SELECT r.loc,COALESCE(a.loc,r.loc) FROM map_fish_roster r LEFT JOIN loc_alias a ON a.alias=r.loc WHERE r.loc LIKE 'level_%'
	) ORDER BY level`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RecordMap{}
	for rows.Next() {
		var m RecordMap
		if err := rows.Scan(&m.Level, &m.Name); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// RecordFishCatalog joins the map roster with this player's real and restored records.
func (d *DB) RecordFishCatalog(ctx context.Context, uid int64, level string) ([]RecordFish, error) {
	var loc string
	if err := d.SQL.QueryRowContext(ctx, `SELECT COALESCE((SELECT loc FROM loc_alias WHERE alias=?),?)`, level, level).Scan(&loc); err != nil {
		return nil, err
	}
	rows, err := d.SQL.QueryContext(ctx, `WITH roster AS (
		SELECT fish_key FROM map_fish_roster WHERE loc=? OR loc=?
		UNION SELECT fish_key FROM loc_wiki_fish WHERE loc=?
		AND NOT EXISTS(SELECT 1 FROM map_fish_roster WHERE loc=? OR loc=?)
	), catches AS (
		SELECT fish_key,MAX(COALESCE(grade,1)) grade,MAX(weight_g) weight_g,
		MAX(CASE WHEN source NOT IN ('admin_record_restore','official_restore_console') THEN COALESCE(grade,1) ELSE 0 END) natural_grade,
		MAX(CASE WHEN source IN ('admin_record_restore','official_restore_console') THEN COALESCE(grade,1) ELSE 0 END) restored_grade,
		MAX(CASE WHEN source IN ('admin_record_restore','official_restore_console') THEN weight_g ELSE 0 END) restored_g
		FROM catch_log WHERE uid=? AND (loc_level=? OR loc=? OR loc=?) GROUP BY fish_key
	)
	SELECT r.fish_key,COALESCE((SELECT NULLIF(fish_zh,'') FROM loc_wiki_fish w WHERE w.fish_key=r.fish_key ORDER BY w.loc LIMIT 1),r.fish_key),
		COALESCE(c.grade,0),COALESCE(c.weight_g,0),COALESCE(c.natural_grade,0),COALESCE(c.restored_grade,0),COALESCE(c.restored_g,0)
	FROM roster r LEFT JOIN catches c ON c.fish_key=r.fish_key ORDER BY 2,r.fish_key`, level, loc, loc, level, loc, uid, level, level, loc)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RecordFish{}
	for rows.Next() {
		var f RecordFish
		if err := rows.Scan(&f.Key, &f.Name, &f.Grade, &f.WeightG, &f.NaturalGrade, &f.RestoredGrade, &f.RestoredG); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// BackupRecords uses SQLite's online backup API, including committed WAL pages.
// Only the main player database is copied; the attached static database stays read only.
func (d *DB) BackupRecords(ctx context.Context) (string, error) {
	dir := filepath.Join(filepath.Dir(d.Path), "backups", "records")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "records-*.sqlite3")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		return "", err
	}
	conn, err := d.SQL.Conn(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	err = conn.Raw(func(raw any) error {
		c, ok := raw.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		})
		if !ok {
			return fmt.Errorf("SQLite 在线备份不可用")
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		slash := filepath.ToSlash(abs)
		if !strings.HasPrefix(slash, "/") {
			slash = "/" + slash
		}
		u := url.URL{Scheme: "file", Path: slash}
		b, err := c.NewBackup(u.String())
		if err != nil {
			return err
		}
		_, stepErr := b.Step(-1)
		return errors.Join(stepErr, b.Finish())
	})
	if err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return filepath.Base(path), nil
}

// RestoreFishRecords replaces only tool-created rows for each selected map/species.
// Natural catches, rewards and inventory are never rewritten by this operation.
func (d *DB) RestoreFishRecords(ctx context.Context, admin string, uid int64, level string, records []CatchRecord, backup string) error {
	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var player int64
	if err := tx.QueryRowContext(ctx, `SELECT uid FROM profile WHERE uid=?`, uid).Scan(&player); err != nil {
		return err
	}
	var loc string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT loc FROM loc_alias WHERE alias=?),?)`, level, level).Scan(&loc); err != nil {
		return err
	}
	now := float64(time.Now().UnixNano()) / 1e9
	for _, r := range records {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM map_fish_roster WHERE (loc=? OR loc=?) AND fish_key=?) +
		(SELECT COUNT(*) FROM loc_wiki_fish WHERE loc=? AND fish_key=? AND NOT EXISTS(SELECT 1 FROM map_fish_roster WHERE loc=? OR loc=?))`, level, loc, r.FishKey, loc, r.FishKey, level, loc).Scan(&count); err != nil {
			return err
		}
		if count == 0 || r.Grade < 0 || r.Grade > 4 || (r.Grade > 0 && (r.WeightG <= 0 || r.LengthM <= 0)) {
			return fmt.Errorf("无效鱼种记录：%s", r.FishKey)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM catch_log WHERE uid=? AND fish_key=? AND (loc_level=? OR loc=? OR loc=?) AND source IN ('admin_record_restore','official_restore_console')`, uid, r.FishKey, level, level, loc); err != nil {
			return err
		}
		if r.Grade == 0 {
			continue
		}
		var identifier [16]byte
		if _, err := rand.Read(identifier[:]); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO catch_log(ts,loc,spot,fish_key,name_zh,weight_g,length_m,source,bait,grade,xp,silver,loc_level,uid,catch_guid)
			VALUES(?,?,0,?,?,?,?,?,'',?,0,0,?,?,?)`, now, loc, r.FishKey, r.NameZh, r.WeightG, r.LengthM, restoredRecordSource, r.Grade, level, uid, hex.EncodeToString(identifier[:])); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_log(admin,action,target,detail,ts) VALUES(?,'fish_records_restore',?,?,?)`, admin, fmt.Sprint(uid), fmt.Sprintf("map=%s count=%d backup=%s", level, len(records), backup), now); err != nil {
		return err
	}
	return tx.Commit()
}
