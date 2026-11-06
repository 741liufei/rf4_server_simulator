package store

import (
	"context"
	"database/sql"
)

// SpeciesRarityMap 返回 fish_key -> 物种稀有度（0=普通 1=稀有 2=超稀有）。
// 来源静态表 species_rarity（rf4db.com 2026-11-06 抓取）；缺表或缺条目视为普通。
func (d *DB) SpeciesRarityMap(ctx context.Context) (map[string]int64, error) {
	rows, err := d.SQL.QueryContext(ctx, `SELECT fish_key, rarity FROM species_rarity`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]int64)
	for rows.Next() {
		var key string
		var rarity int64
		if err := rows.Scan(&key, &rarity); err != nil {
			return nil, err
		}
		out[key] = rarity
	}
	return out, rows.Err()
}

// SpeciesRarity 查单条鱼种稀有度；未知条目返回 0（普通）。
func (d *DB) SpeciesRarity(ctx context.Context, fishKey string) int64 {
	if d == nil || fishKey == "" {
		return 0
	}
	var rarity int64
	_ = d.SQL.QueryRowContext(ctx,
		`SELECT rarity FROM species_rarity WHERE fish_key=?`, fishKey).Scan(&rarity)
	return rarity
}

// SpeciesRarityTx 事务内查稀有度。连接池上限为 1（staticdb），经济事务
// 开启后禁止再走池化查询，必须复用事务连接，否则死等到 context 超时。
func SpeciesRarityTx(ctx context.Context, tx *sql.Tx, fishKey string) int64 {
	if tx == nil || fishKey == "" {
		return 0
	}
	var rarity int64
	_ = tx.QueryRowContext(ctx,
		`SELECT rarity FROM species_rarity WHERE fish_key=?`, fishKey).Scan(&rarity)
	return rarity
}
