package gameserver

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/mozillazg/go-pinyin"
)

func fishQueryName(text string) string {
	return strings.TrimFunc(text, func(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' })
}

func fishQueryPinyin(text string) string {
	args := pinyin.Args{
		Style: pinyin.Normal,
		// Preserve Latin pinyin and unknown characters instead of dropping them.
		Fallback: func(r rune, _ pinyin.Args) []string { return []string{string(r)} },
	}
	return strings.Join(pinyin.LazyPinyin(strings.ToLower(text), args), "")
}

func (s *Server) resolveFishQuerySpecies(ctx context.Context, text string) (string, error) {
	text = fishQueryName(text)
	prefix := fishQueryPinyin(text)
	if prefix == "" {
		return "", fmt.Errorf("未找到鱼种，请输入鱼名、拼音前缀或鱼种编号")
	}
	rows, err := s.DB.SQL.QueryContext(ctx, `SELECT DISTINCT fish_key,COALESCE(fish_zh,'') FROM loc_wiki_fish ORDER BY fish_key,fish_zh`)
	if err != nil {
		return "", fmt.Errorf("鱼种或地图资料暂不可用")
	}
	defer rows.Close()
	first := ""
	for rows.Next() {
		var key, name string
		if err := rows.Scan(&key, &name); err != nil {
			return "", fmt.Errorf("鱼种或地图资料暂不可用")
		}
		name = fishQueryName(name)
		if text == key || text == name {
			return key, nil
		}
		// Keep the first prefix match, but let an exact name or key take priority.
		if first == "" && strings.HasPrefix(fishQueryPinyin(name), prefix) {
			first = key
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("鱼种或地图资料暂不可用")
	}
	if first != "" {
		return first, nil
	}
	return "", fmt.Errorf("未找到鱼种，请输入鱼名、拼音前缀或鱼种编号")
}

func (s *Server) resolveFishQueryMap(ctx context.Context, text string) (string, error) {
	rows, err := s.DB.SQL.QueryContext(ctx, `SELECT alias FROM loc_alias WHERE alias=? OR loc=? ORDER BY alias LIMIT 2`, text, text)
	if err != nil {
		return "", fmt.Errorf("鱼种或地图资料暂不可用")
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return "", fmt.Errorf("鱼种或地图资料暂不可用")
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("鱼种或地图资料暂不可用")
	}
	if len(keys) == 1 {
		return keys[0], nil
	}
	return "", fmt.Errorf("地图未识别，请用地图全名或level编号")
}
