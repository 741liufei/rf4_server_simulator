package admin

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"rf4server/internal/gameserver"
)

// baitPrefRow 一条鱼的饵料偏好摘要（管理后台只读展示）。
type baitPrefRow struct {
	FishKey  string             `json:"fish_key"`
	NameZh   string             `json:"name_zh"`
	HasRule  bool               `json:"has_rule"`
	Allow    []string           `json:"allow"`       // 规则表"吃"列表
	Deny     []string           `json:"deny"`        // 规则表"不吃"列表
	BottomOh []string           `json:"bottom_only"` // 底钓只吃
	Cond     string             `json:"condition"`
	Tier3    int                `json:"tier3"`     // tier3 饵数量
	Tier2    int                `json:"tier2"`     // tier2 饵数量
	Total    int                `json:"total"`     // 有证据的饵总数
	Baits    []baitPrefEvidence `json:"baits"`     // 证据最强的若干条
	Locs     []string           `json:"locs"`      // 出现水域（截断）
	InStrict bool               `json:"in_strict"` // 严格模式下是否还能咬（规则命中或 tier>0）
}

type baitPrefEvidence struct {
	BaitID   string `json:"bait_id"`
	Tier     int    `json:"tier"`
	Evidence int    `json:"evidence"`
}

// handleBaitPrefs 鱼饵偏好总览：每条鱼一行，含规则表条目与饵效证据。
//
// 查询参数：
//
//	q     按 fish_key / 中文名模糊匹配
//	loc   只看某个水域出现过的鱼
//	limit 默认 400，最多 2000
func (s *Server) handleBaitPrefs(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	loc := strings.TrimSpace(r.URL.Query().Get("loc"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 2000 {
		limit = 400
	}
	like := "%" + q + "%"

	// 1) 鱼清单 + 中文名（优先 wiki 名，退回 loc_fish.name_zh）
	rows, err := s.db.SQL.QueryContext(r.Context(), `
		SELECT x.fish_key, COALESCE(x.name_zh, x.fish_key)
		FROM (
			SELECT w.fish_key,
			       COALESCE(MAX(NULLIF(w.fish_zh,'')), MAX(NULLIF(l.name_zh,'')), w.fish_key) AS name_zh
			FROM loc_wiki_fish w
			LEFT JOIN loc_fish l ON l.fish_key = w.fish_key
			WHERE (? = '' OR w.fish_key LIKE ? OR w.fish_zh LIKE ?)
			  AND (? = '' OR w.loc = ?)
			GROUP BY w.fish_key
		) x
		ORDER BY x.fish_key
		LIMIT ?`, q, like, like, loc, loc, limit)
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	type fishEntry struct{ key, name string }
	fish := make([]fishEntry, 0, 256)
	nameByKey := map[string]string{}
	for rows.Next() {
		var k, n string
		if err := rows.Scan(&k, &n); err != nil {
			rows.Close()
			httpError(w, 500, err.Error())
			return
		}
		fish = append(fish, fishEntry{k, n})
		nameByKey[k] = n
	}
	rows.Close()

	// 2) 饵效证据：一次性取全部，再在内存里按鱼聚合（3 万行，够快且只读）。
	evRows, err := s.db.SQL.QueryContext(r.Context(), `
		SELECT fish_key, bait_id, tier, evidence
		FROM companion_bait_fish
		ORDER BY fish_key, tier DESC, evidence DESC`)
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	type agg struct {
		tier3, tier2, total int
		baits               []baitPrefEvidence
	}
	byFish := map[string]*agg{}
	for evRows.Next() {
		var fk, bait string
		var tier, ev int
		if err := evRows.Scan(&fk, &bait, &tier, &ev); err != nil {
			evRows.Close()
			httpError(w, 500, err.Error())
			return
		}
		a := byFish[fk]
		if a == nil {
			a = &agg{}
			byFish[fk] = a
		}
		a.total++
		switch tier {
		case 3:
			a.tier3++
		case 2:
			a.tier2++
		}
		if len(a.baits) < 8 {
			a.baits = append(a.baits, baitPrefEvidence{BaitID: bait, Tier: tier, Evidence: ev})
		}
	}
	evRows.Close()

	// 3) 水域归属（可选，用于筛选后的展示）
	locsByFish := map[string][]string{}
	if locRows, err := s.db.SQL.QueryContext(r.Context(),
		`SELECT fish_key, loc FROM loc_wiki_fish WHERE loc <> '' ORDER BY loc`); err == nil {
		for locRows.Next() {
			var fk, l string
			if locRows.Scan(&fk, &l) == nil {
				if len(locsByFish[fk]) < 6 {
					locsByFish[fk] = append(locsByFish[fk], l)
				}
			}
		}
		locRows.Close()
	}

	// 4) 合并规则表（内存 map）
	out := make([]baitPrefRow, 0, len(fish))
	for _, f := range fish {
		row := baitPrefRow{FishKey: f.key, NameZh: f.name, Locs: locsByFish[f.key]}
		if info, ok := gameserver.ExportedBaitRules(f.name); ok {
			row.HasRule = true
			row.Allow = info.Allow
			row.Deny = info.Deny
			row.BottomOh = info.BottomOnly
			row.Cond = info.Condition
		}
		if a := byFish[f.key]; a != nil {
			row.Tier3, row.Tier2, row.Total = a.tier3, a.tier2, a.total
			row.Baits = a.baits
		}
		// 严格模式判定：命中规则（有允许列表）或该鱼在证据表里有 >=1 条记录才会被放行。
		row.InStrict = row.HasRule || row.Total > 0
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FishKey < out[j].FishKey })

	writeJSON(w, map[string]any{
		"total":       len(out),
		"rule_count":  gameserver.ExportedBaitRuleCount(),
		"rows":        out,
		"fish_key_ok": len(nameByKey),
	})
}
