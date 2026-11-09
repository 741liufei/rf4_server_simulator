package admin

import (
	"math"
	"net/http"
	"strconv"
	"strings"
)

type hookProfileRow struct {
	ItemID                string  `json:"item_id"`
	NumericID             int64   `json:"numeric_id"`
	Size                  int64   `json:"size"`
	LengthMM              float64 `json:"length_mm"`
	StrengthKg            float64 `json:"strength_kg"`
	WireWeightG           float64 `json:"wire_weight_g"`
	Quality               int64   `json:"quality"`
	Family                string  `json:"family"`
	Brand                 string  `json:"brand"`
	EntryCapacityG        float64 `json:"entry_capacity_g"`
	SolidScale            float64 `json:"solid_scale"`
	HoldScale             float64 `json:"hold_scale"`
	SmallFishFloor        float64 `json:"small_fish_floor"`
	OverCapacityHoldFloor float64 `json:"over_capacity_hold_floor"`
	UpdatedTS             float64 `json:"updated_ts"`
}

func (s *Server) handleHookProfiles(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.ListHookBiteProfiles(r.Context())
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	family := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("family")))
	sizeText := strings.TrimSpace(r.URL.Query().Get("size"))
	size := int64(-1)
	if sizeText != "" {
		if v, err := strconv.ParseInt(sizeText, 10, 64); err == nil {
			size = v
		}
	}
	limit := 100
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 500 {
		limit = v
	}
	out := make([]hookProfileRow, 0, limit)
	total := 0
	for _, p := range rows {
		if family != "" && strings.ToLower(p.Family) != family {
			continue
		}
		if size >= 0 && p.Size != size {
			continue
		}
		if q != "" {
			hay := strings.ToLower(p.ItemID + " " + p.Brand + " " + p.Family)
			if !strings.Contains(hay, q) {
				continue
			}
		}
		total++
		if len(out) >= limit {
			continue
		}
		capG := 8 * math.Pow(p.LengthMM, 3) * 1.35
		if capG < 50 {
			capG = 50
		}
		if capG > 2_000_000 {
			capG = 2_000_000
		}
		out = append(out, hookProfileRow{
			ItemID: p.ItemID, NumericID: p.NumericID, Size: p.Size,
			LengthMM: p.LengthMM, StrengthKg: p.StrengthKg, WireWeightG: p.WireWeightG,
			Quality: p.Quality, Family: p.Family, Brand: p.Brand,
			EntryCapacityG: capG, SolidScale: p.SolidScale, HoldScale: p.HoldScale,
			SmallFishFloor: p.SmallFishFloor, OverCapacityHoldFloor: p.OverCapacityHoldFloor,
			UpdatedTS: p.UpdatedTS,
		})
	}
	writeJSON(w, map[string]any{"profiles": out, "total": total})
}

func (s *Server) handleHookProfileSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ItemID                string  `json:"item_id"`
		SolidScale            float64 `json:"solid_scale"`
		HoldScale             float64 `json:"hold_scale"`
		SmallFishFloor        float64 `json:"small_fish_floor"`
		OverCapacityHoldFloor float64 `json:"over_capacity_hold_floor"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.ItemID) == "" {
		httpError(w, 400, "参数错误")
		return
	}
	if req.SolidScale < 0 || req.SolidScale > 3 || req.HoldScale < 0 || req.HoldScale > 3 ||
		req.SmallFishFloor < 0 || req.SmallFishFloor > 1 ||
		req.OverCapacityHoldFloor < 0 || req.OverCapacityHoldFloor > 1 {
		httpError(w, 400, "倍率必须在 0~3，兜底概率必须在 0~1")
		return
	}
	profile, err := s.db.GetHookBiteProfile(r.Context(), req.ItemID)
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	if profile == nil {
		httpError(w, 404, "鱼钩型号不存在")
		return
	}
	if err := s.db.SetHookBiteProfileTuning(r.Context(), req.ItemID,
		req.SolidScale, req.HoldScale, req.SmallFishFloor, req.OverCapacityHoldFloor); err != nil {
		httpError(w, 500, err.Error())
		return
	}
	_ = s.db.AuditLogWrite(r.Context(), adminOf(r), "hook_profile", req.ItemID, mustJSON(req))
	_ = s.db.QueuePush(r.Context(), mustJSON(map[string]any{"op": "reload_tunables"}))
	writeJSON(w, map[string]any{"ok": true})
}
