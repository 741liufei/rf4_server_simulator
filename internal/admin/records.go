package admin

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"rf4server/internal/gameserver"
	"rf4server/internal/store"
)

func (s *Server) handlePlayerRecords(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	uid, err := strconv.ParseInt(r.URL.Query().Get("uid"), 10, 64)
	if err != nil || uid <= 0 {
		httpError(w, 400, "无效玩家")
		return
	}
	var exists int
	if err := s.db.SQL.QueryRowContext(ctx, `SELECT COUNT(*) FROM profile WHERE uid=?`, uid).Scan(&exists); err != nil || exists == 0 {
		httpError(w, 404, "玩家不存在")
		return
	}
	maps, err := s.db.RecordMaps(ctx)
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	level := r.URL.Query().Get("level")
	if level == "" && len(maps) > 0 {
		level = maps[0].Level
	}
	fish := []store.RecordFish{}
	if level != "" {
		fish, err = s.db.RecordFishCatalog(ctx, uid, level)
		if err != nil {
			httpError(w, 400, "地图不可用")
			return
		}
	}
	writeJSON(w, map[string]any{"maps": maps, "level": level, "fish": fish})
}

func (s *Server) handlePlayerRecordsSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UID     int64  `json:"uid"`
		Level   string `json:"level"`
		Changes []struct {
			FishKey string `json:"fish_key"`
			Grade   int64  `json:"grade"`
			WeightG int64  `json:"weight_g"`
		} `json:"changes"`
	}
	if err := readJSON(r, &req); err != nil || req.UID <= 0 || len(req.Changes) == 0 || len(req.Changes) > 500 {
		httpError(w, 400, "参数错误")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var exists int
	if err := s.db.SQL.QueryRowContext(ctx, `SELECT COUNT(*) FROM profile WHERE uid=?`, req.UID).Scan(&exists); err != nil || exists == 0 {
		httpError(w, 404, "玩家不存在")
		return
	}
	catalog, err := s.db.RecordFishCatalog(ctx, req.UID, req.Level)
	if err != nil {
		httpError(w, 400, "地图不可用")
		return
	}
	names := map[string]string{}
	for _, f := range catalog {
		names[f.Key] = f.Name
	}
	seen := map[string]bool{}
	records := make([]store.CatchRecord, 0, len(req.Changes))
	for _, c := range req.Changes {
		name, ok := names[c.FishKey]
		if !ok || seen[c.FishKey] {
			httpError(w, 400, "鱼种不属于该地图或重复")
			return
		}
		seen[c.FishKey] = true
		record, err := gameserver.PrepareRestoredRecord(ctx, s.db, c.FishKey, c.Grade, c.WeightG)
		if err != nil {
			httpError(w, 400, err.Error())
			return
		}
		record.NameZh = name
		records = append(records, record)
	}
	backup, err := s.db.BackupRecords(ctx)
	if err != nil {
		s.log.l.Error("备份鱼种记录失败", "err", err)
		httpError(w, 500, "备份失败，记录未修改")
		return
	}
	if err := s.db.RestoreFishRecords(ctx, adminOf(r), req.UID, req.Level, records, backup); err != nil {
		s.log.l.Error("保存鱼种记录失败", "err", err)
		httpError(w, 500, "保存失败，记录未修改")
		return
	}
	writeJSON(w, map[string]any{"ok": true, "backup": backup})
}
