package store

import (
	"context"
	"database/sql"
	"time"
)

// conditionDBTX 同时被 *sql.DB 与 *sql.Tx 满足，让状态条读写可以在
// 经济事务内复用同一实现。
type conditionDBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// 玩家状态条默认值：对应 308 的 f0/f1/f3/f4。
// f0/f1 抓包锚定（官服静止样本）；f3/f4 官服观测恒 1.0/0。
const (
	DefaultPlayerConditionPrimary     = 0.85
	DefaultPlayerConditionSecondary   = 0.9946
	DefaultPlayerConditionComfort     = 1.0
	DefaultPlayerConditionLuckPenalty = 0.0
)

// PlayerCondition 玩家状态条持久化行。
// Primary=f0 精力比例、Secondary=f1 饱食度、Comfort=f3 舒适度、
// LuckPenalty=f4 幸运惩罚（客户端 HUD 显示 1-f4）。
type PlayerCondition struct {
	UID         int64
	Primary     float64
	Secondary   float64
	Comfort     float64
	LuckPenalty float64
	UpdatedTS   float64
}

func (d *DB) EnsurePlayerCondition(ctx context.Context, uid int64) (*PlayerCondition, error) {
	return ensurePlayerConditionQ(ctx, d.SQL, uid)
}

func ensurePlayerConditionQ(ctx context.Context, q conditionDBTX, uid int64) (*PlayerCondition, error) {
	_, err := q.ExecContext(ctx, `
		INSERT INTO player_condition(uid, primary_value, secondary_value, comfort_value, luck_penalty, updated_ts)
		VALUES(?,?,?,?,?,?) ON CONFLICT(uid) DO NOTHING`,
		uid, DefaultPlayerConditionPrimary, DefaultPlayerConditionSecondary,
		DefaultPlayerConditionComfort, DefaultPlayerConditionLuckPenalty, float64(time.Now().Unix()))
	if err != nil {
		return nil, err
	}
	var v PlayerCondition
	err = q.QueryRowContext(ctx, `
		SELECT uid, primary_value, secondary_value, comfort_value, luck_penalty, updated_ts
		FROM player_condition WHERE uid=?`, uid).Scan(
		&v.UID, &v.Primary, &v.Secondary, &v.Comfort, &v.LuckPenalty, &v.UpdatedTS)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func (d *DB) UpsertPlayerCondition(ctx context.Context, v PlayerCondition) error {
	return upsertPlayerConditionQ(ctx, d.SQL, v)
}

func upsertPlayerConditionQ(ctx context.Context, q conditionDBTX, v PlayerCondition) error {
	if v.Primary < 0 {
		v.Primary = 0
	}
	if v.Secondary < 0 {
		v.Secondary = 0
	}
	v.Comfort = clampUnit(v.Comfort)
	v.LuckPenalty = clampUnit(v.LuckPenalty)
	if v.UpdatedTS <= 0 {
		v.UpdatedTS = float64(time.Now().Unix())
	}
	_, err := q.ExecContext(ctx, `
		INSERT INTO player_condition(uid, primary_value, secondary_value, comfort_value, luck_penalty, updated_ts)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(uid) DO UPDATE SET
			primary_value=excluded.primary_value,
			secondary_value=excluded.secondary_value,
			comfort_value=excluded.comfort_value,
			luck_penalty=excluded.luck_penalty,
			updated_ts=excluded.updated_ts`,
		v.UID, v.Primary, v.Secondary, v.Comfort, v.LuckPenalty, v.UpdatedTS)
	return err
}

// EnsurePlayerConditionTx / UpsertPlayerConditionTx：经济事务内使用的同实现版本。
func EnsurePlayerConditionTx(ctx context.Context, tx *sql.Tx, uid int64) (*PlayerCondition, error) {
	return ensurePlayerConditionQ(ctx, tx, uid)
}

func UpsertPlayerConditionTx(ctx context.Context, tx *sql.Tx, v PlayerCondition) error {
	return upsertPlayerConditionQ(ctx, tx, v)
}

// clampUnit 把比例值钳到 [0,1]；comfort/luck_penalty 是纯比例字段。
func clampUnit(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
