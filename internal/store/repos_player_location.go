package store

import (
	"context"
	"database/sql"
	"time"
)

// PlayerLocation 是登录恢复所需的最小位置状态。它不是物品/钓组快照；
// LastLevelKey 必须是静态地图表 key，LastScene 保存客户端上次使用的场景名。
type PlayerLocation struct {
	LevelKey string
	Scene    string
	Spot     int64
	Updated  float64
}

// GetPlayerLocation 读取玩家上次位置。档案不存在时返回 (nil, nil)。
func (d *DB) GetPlayerLocation(ctx context.Context, uid int64) (*PlayerLocation, error) {
	var loc PlayerLocation
	err := d.SQL.QueryRowContext(ctx, `
		SELECT COALESCE(last_level_key,''), COALESCE(last_scene,''),
		       COALESCE(last_spot,0), COALESCE(location_updated_ts,0)
		FROM profile WHERE uid=?`, uid).
		Scan(&loc.LevelKey, &loc.Scene, &loc.Spot, &loc.Updated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &loc, nil
}

// setPlayerLocationSQL 位置写回的唯一语句，普通路径与切图事务路径共用，
// 避免两处 SQL 漂移（UPSERT 允许缺少 profile 的库建立最小档案）。
const setPlayerLocationSQL = `
	INSERT INTO profile(uid, last_level_key, last_scene, last_spot,
	                    location_updated_ts, created_ts, updated_ts)
	VALUES(?,?,?,?,?,?,?)
	ON CONFLICT(uid) DO UPDATE SET
		last_level_key=excluded.last_level_key,
		last_scene=excluded.last_scene,
		last_spot=excluded.last_spot,
		location_updated_ts=excluded.location_updated_ts,
		updated_ts=excluded.updated_ts`

// SetPlayerLocation 原子写回玩家位置。UPSERT 允许已有数据库或测试在缺少
// profile 时建立最小档案；正常登录路径仍会先调用 EnsureProfile。
func (d *DB) SetPlayerLocation(ctx context.Context, uid int64, levelKey, scene string, spot int64) error {
	now := float64(time.Now().Unix())
	_, err := d.SQL.ExecContext(ctx, setPlayerLocationSQL,
		uid, levelKey, scene, spot, now, now, now)
	return err
}

// SetPlayerLocationTx 事务版本：切图时位置写回与银币扣款、txn 流水共用同一个
// *sql.Tx，任一步失败整体回滚。store.DB 只有一个连接，事务持有期间不能访问
// d.SQL，因此本方法只使用调用方传入的 tx（见 gameserver/economy_tx.go）。
func (d *DB) SetPlayerLocationTx(ctx context.Context, tx *sql.Tx, uid int64,
	levelKey, scene string, spot int64) error {
	now := float64(time.Now().Unix())
	_, err := tx.ExecContext(ctx, setPlayerLocationSQL,
		uid, levelKey, scene, spot, now, now, now)
	return err
}
