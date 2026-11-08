package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// CampfireState 是玩家最近一次点燃的篝火。篝火按账号保存，重连后仍能恢复到期时间。
type CampfireState struct {
	UID         int64
	Loc         string
	Spot        int64
	DurationMin int64
	ExpiresFT   int64
	UpdatedTS   float64
}

type campfireDBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (d *DB) GetCampfireState(ctx context.Context, uid int64) (*CampfireState, error) {
	return getCampfireStateQ(ctx, d.SQL, uid)
}

// GetCampfireStateTx 在事务内读取篝火状态，用于与材料扣减保持同一写锁。
func GetCampfireStateTx(ctx context.Context, tx *sql.Tx, uid int64) (*CampfireState, error) {
	return getCampfireStateQ(ctx, tx, uid)
}

func getCampfireStateQ(ctx context.Context, q campfireDBTX, uid int64) (*CampfireState, error) {
	var v CampfireState
	err := q.QueryRowContext(ctx, `
		SELECT uid, loc, spot, duration_min, expires_ft, updated_ts
		FROM campfire_state WHERE uid=?`, uid).Scan(
		&v.UID, &v.Loc, &v.Spot, &v.DurationMin, &v.ExpiresFT, &v.UpdatedTS)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func (d *DB) UpsertCampfireState(ctx context.Context, v CampfireState) error {
	return upsertCampfireStateQ(ctx, d.SQL, v)
}

// UpsertCampfireStateTx 在经济事务内保存篝火状态。
func UpsertCampfireStateTx(ctx context.Context, tx *sql.Tx, v CampfireState) error {
	return upsertCampfireStateQ(ctx, tx, v)
}

func upsertCampfireStateQ(ctx context.Context, q campfireDBTX, v CampfireState) error {
	if v.UpdatedTS <= 0 {
		v.UpdatedTS = float64(time.Now().Unix())
	}
	_, err := q.ExecContext(ctx, `
		INSERT INTO campfire_state(uid, loc, spot, duration_min, expires_ft, updated_ts)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(uid) DO UPDATE SET
			loc=excluded.loc,
			spot=excluded.spot,
			duration_min=excluded.duration_min,
			expires_ft=excluded.expires_ft,
			updated_ts=excluded.updated_ts`,
		v.UID, v.Loc, v.Spot, v.DurationMin, v.ExpiresFT, v.UpdatedTS)
	return err
}
