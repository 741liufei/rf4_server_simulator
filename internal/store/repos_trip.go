package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// 钓鱼游状态（迁移 00035）。语义见迁移注释：
// 时长只在会话在线时消费，purchases 跨到期保留供限购判断。

// TripState 一个地点的钓鱼游状态。
type TripState struct {
	UID           int64
	Place         string
	RemainingSec  float64
	LastConsumeTS float64
	Purchases     int64
	UpdatedTS     float64
}

func (d *DB) GetTripState(ctx context.Context, uid int64, place string) (*TripState, error) {
	var v TripState
	err := d.SQL.QueryRowContext(ctx, `
		SELECT uid, place, remaining_sec, last_consume_ts, purchases, updated_ts
		FROM trip_state WHERE uid=? AND place=?`, uid, place).Scan(
		&v.UID, &v.Place, &v.RemainingSec, &v.LastConsumeTS, &v.Purchases, &v.UpdatedTS)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// TripStates 全部钓鱼游状态（到期清理与 keepalive 消费用）。
func (d *DB) TripStates(ctx context.Context, uid int64) ([]*TripState, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT uid, place, remaining_sec, last_consume_ts, purchases, updated_ts
		FROM trip_state WHERE uid=?`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*TripState
	for rows.Next() {
		var v TripState
		if err := rows.Scan(&v.UID, &v.Place, &v.RemainingSec, &v.LastConsumeTS, &v.Purchases, &v.UpdatedTS); err != nil {
			return nil, err
		}
		out = append(out, &v)
	}
	return out, rows.Err()
}

func (d *DB) UpsertTripState(ctx context.Context, v TripState) error {
	if v.UpdatedTS <= 0 {
		v.UpdatedTS = float64(time.Now().Unix())
	}
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO trip_state(uid, place, remaining_sec, last_consume_ts, purchases, updated_ts)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(uid, place) DO UPDATE SET
			remaining_sec=excluded.remaining_sec,
			last_consume_ts=excluded.last_consume_ts,
			purchases=excluded.purchases,
			updated_ts=excluded.updated_ts`,
		v.UID, v.Place, v.RemainingSec, v.LastConsumeTS, v.Purchases, v.UpdatedTS)
	return err
}

// TripGrant 一条钓鱼游装备发放记录（迁移 00039）。
type TripGrant struct {
	UID       int64
	Place     string
	GUID      string
	ItemID    string
	Recalled  bool
	GrantedTS float64
}

// AddTripGrants 批量登记发放记录（购票发放，见 gameserver/trip_equip.go）。
func (d *DB) AddTripGrants(ctx context.Context, uid int64, place string, guids, itemIDs []string) error {
	if len(guids) == 0 {
		return nil
	}
	if len(guids) != len(itemIDs) {
		return errors.New("trip_grant 参数不配对")
	}
	now := float64(time.Now().Unix())
	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for i, g := range guids {
		if _, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO trip_grant(uid, place, guid, item_id, recalled, granted_ts)
			VALUES(?,?,?,?,0,?)`, uid, place, g, itemIDs[i], now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UnrecalledTripGrants 某地点未回收的发放记录。
func (d *DB) UnrecalledTripGrants(ctx context.Context, uid int64, place string) ([]*TripGrant, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT uid, place, guid, item_id, recalled, granted_ts
		FROM trip_grant WHERE uid=? AND place=? AND recalled=0 ORDER BY granted_ts`, uid, place)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*TripGrant
	for rows.Next() {
		var v TripGrant
		if err := rows.Scan(&v.UID, &v.Place, &v.GUID, &v.ItemID, &v.Recalled, &v.GrantedTS); err != nil {
			return nil, err
		}
		out = append(out, &v)
	}
	return out, rows.Err()
}

// HasUnrecalledTripGrant 是否存在未回收发放（真则重复购票不再发放）。
func (d *DB) HasUnrecalledTripGrant(ctx context.Context, uid int64, place string) (bool, error) {
	var n int
	err := d.SQL.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM trip_grant WHERE uid=? AND place=? AND recalled=0`,
		uid, place).Scan(&n)
	return n > 0, err
}

// MarkTripGrantsRecalled 批量置回收完成。
func (d *DB) MarkTripGrantsRecalled(ctx context.Context, uid int64, place string, guids []string) error {
	if len(guids) == 0 {
		return nil
	}
	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, g := range guids {
		if _, err := tx.ExecContext(ctx, `
			UPDATE trip_grant SET recalled=1 WHERE uid=? AND place=? AND guid=?`,
			uid, place, g); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UnrecalledTripGrantPlaces 存在未回收发放的地点列表（去重）。
func (d *DB) UnrecalledTripGrantPlaces(ctx context.Context, uid int64) ([]string, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT DISTINCT place FROM trip_grant WHERE uid=? AND recalled=0`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ExpireTripAccessTx 钓鱼游到期落地：trip_state 清零与 place_access(kind='trip')
// 回收在同一事务，任一步失败整体回滚（下一拍补偿重试）。DELETE 带
// remaining_sec<=0 守卫，与重购并发时不会误删新资格。
func (d *DB) ExpireTripAccessTx(ctx context.Context, uid int64, place string, now float64) error {
	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`UPDATE trip_state SET remaining_sec=0, last_consume_ts=?, updated_ts=? WHERE uid=? AND place=?`,
		now, now, uid, place); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM place_access WHERE uid=? AND place=? AND kind='trip'
		AND EXISTS(SELECT 1 FROM trip_state t
			WHERE t.uid=place_access.uid AND t.place=place_access.place AND t.remaining_sec<=0)`,
		uid, place); err != nil {
		return err
	}
	return tx.Commit()
}

// RevokeExpiredTripAccess 残留补偿：仅删除已到期（remaining_sec<=0）钓鱼游
// 的 access 行，不动状态行。覆盖"清零成功、删除失败"的历史残留。
func (d *DB) RevokeExpiredTripAccess(ctx context.Context, uid int64, place string) error {
	_, err := d.SQL.ExecContext(ctx, `
		DELETE FROM place_access WHERE uid=? AND place=? AND kind='trip'
		AND EXISTS(SELECT 1 FROM trip_state t
			WHERE t.uid=place_access.uid AND t.place=place_access.place AND t.remaining_sec<=0)`,
		uid, place)
	return err
}
