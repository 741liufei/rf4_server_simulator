package store

import (
	"context"
	"database/sql"
	"time"
)

// AchievementState 是 player_achievements 的单条权威状态。
type AchievementState struct {
	AchID    int64
	Unlocked bool
	Progress float64
	Claimed  bool
	TS       float64
}

// AchievementDefinitions 返回协议展示顺序中的成就 id。
func (d *DB) AchievementDefinitions(ctx context.Context) ([]int64, error) {
	rows, err := d.SQL.QueryContext(ctx, `SELECT id FROM achievement_def ORDER BY sort, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]int64, 0, 130)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// PlayerAchievementStates 返回指定玩家已有的成就状态；缺失行表示全零状态。
func (d *DB) PlayerAchievementStates(ctx context.Context, uid int64) (map[int64]AchievementState, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT ach_id, unlocked, progress, claimed, ts
		FROM player_achievements WHERE uid=?`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]AchievementState)
	for rows.Next() {
		var st AchievementState
		var unlocked, claimed int64
		if err := rows.Scan(&st.AchID, &unlocked, &st.Progress, &claimed, &st.TS); err != nil {
			return nil, err
		}
		st.Unlocked = unlocked != 0
		st.Claimed = claimed != 0
		out[st.AchID] = st
	}
	return out, rows.Err()
}

// UnlockAchievement 幂等解锁；完成时间只在首次写入时落库。
func (d *DB) UnlockAchievement(ctx context.Context, uid, achID int64, progress, ts float64) error {
	if ts <= 0 {
		ts = float64(time.Now().Unix())
	}
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO player_achievements(uid, ach_id, unlocked, progress, claimed, ts)
		VALUES(?, ?, 1, ?, 0, ?)
		ON CONFLICT(uid, ach_id) DO UPDATE SET
			unlocked=1,
			progress=CASE WHEN excluded.progress > player_achievements.progress
			              THEN excluded.progress ELSE player_achievements.progress END,
			ts=CASE WHEN player_achievements.ts > 0 THEN player_achievements.ts
			        ELSE excluded.ts END`,
		uid, achID, progress, ts)
	return err
}

// SetAchievementProgress 记录未解锁进度。
func (d *DB) SetAchievementProgress(ctx context.Context, uid, achID int64, progress float64) error {
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO player_achievements(uid, ach_id, unlocked, progress, claimed, ts)
		VALUES(?, ?, 0, ?, 0, 0)
		ON CONFLICT(uid, ach_id) DO UPDATE SET progress=excluded.progress`,
		uid, achID, progress)
	return err
}

// ClaimAchievement 原子领取；unlocked=0 或已领取时返回 false。
func (d *DB) ClaimAchievement(ctx context.Context, uid, achID int64) (bool, error) {
	res, err := d.SQL.ExecContext(ctx, `
		UPDATE player_achievements SET claimed=1
		WHERE uid=? AND ach_id=? AND unlocked=1 AND claimed=0`, uid, achID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// UnlockAndClaimAchievement 用一条 SQL 同时写入 unlocked=1 / progress / claimed=1，
// 返回本次是否首次点亮。客户端只有 claimed=1 会点亮图标，本模拟器无发奖链路，
// 所以解锁与点亮本就是同一次状态变更；单条 UPSERT 不会留下
// unlocked=1, claimed=0 的中间态，WHERE 子句保证已点亮的成就不再返回 true。
func (d *DB) UnlockAndClaimAchievement(ctx context.Context, uid, achID int64, progress, ts float64) (bool, error) {
	if ts <= 0 {
		ts = float64(time.Now().Unix())
	}
	res, err := d.SQL.ExecContext(ctx, `
		INSERT INTO player_achievements(uid, ach_id, unlocked, progress, claimed, ts)
		VALUES(?, ?, 1, ?, 1, ?)
		ON CONFLICT(uid, ach_id) DO UPDATE SET
			unlocked=1,
			progress=CASE WHEN excluded.progress > player_achievements.progress
			              THEN excluded.progress ELSE player_achievements.progress END,
			claimed=1,
			ts=CASE WHEN player_achievements.ts > 0 THEN player_achievements.ts
			        ELSE excluded.ts END
		WHERE player_achievements.claimed=0`,
		uid, achID, progress, ts)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// CatchRecord 是 catch_log 的个人纪录行，也是 3/17、24/22 与竞赛计分的数据源。
type CatchRecord struct {
	ID       int64
	TS       float64
	Loc      string
	Spot     int64
	FishKey  string
	NameZh   string
	WeightG  int64
	LengthM  float64
	Source   string
	Bait     string
	Grade    int64
	XP       int64
	Silver   int64
	LocLevel string
}

// PlayerCatchRecords 按记录顺序返回玩家的全部个人钓获。
func (d *DB) PlayerCatchRecords(ctx context.Context, uid int64) ([]CatchRecord, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT id, COALESCE(ts,0), COALESCE(loc,''), COALESCE(spot,0),
		       COALESCE(fish_key,''), COALESCE(name_zh,''), COALESCE(weight_g,0),
		       COALESCE(length_m,0), COALESCE(source,''), COALESCE(bait,''),
		       COALESCE(grade,0), COALESCE(xp,0), COALESCE(silver,0), COALESCE(loc_level,'')
		FROM catch_log WHERE uid=? ORDER BY id`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]CatchRecord, 0)
	for rows.Next() {
		var r CatchRecord
		if err := rows.Scan(&r.ID, &r.TS, &r.Loc, &r.Spot, &r.FishKey, &r.NameZh,
			&r.WeightG, &r.LengthM, &r.Source, &r.Bait, &r.Grade, &r.XP, &r.Silver,
			&r.LocLevel); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// BestCatchByWater 返回玩家在指定水域/地点的最重纪录；water 为空时查该玩家全部纪录。
func (d *DB) BestCatchByWater(ctx context.Context, uid int64, water string) (*CatchRecord, error) {
	const cols = `id, COALESCE(ts,0), COALESCE(loc,''), COALESCE(spot,0),
		COALESCE(fish_key,''), COALESCE(name_zh,''), COALESCE(weight_g,0),
		COALESCE(length_m,0), COALESCE(source,''), COALESCE(bait,''),
		COALESCE(grade,0), COALESCE(xp,0), COALESCE(silver,0), COALESCE(loc_level,'')`
	var row *sql.Row
	if water == "" {
		row = d.SQL.QueryRowContext(ctx, `SELECT `+cols+` FROM catch_log
			WHERE uid=? AND COALESCE(fish_key,'')<>''
			ORDER BY weight_g DESC, ts ASC, id ASC LIMIT 1`, uid)
	} else {
		row = d.SQL.QueryRowContext(ctx, `SELECT `+cols+` FROM catch_log
			WHERE uid=? AND (loc=? OR loc_level=?) AND COALESCE(fish_key,'')<>''
			ORDER BY weight_g DESC, ts ASC, id ASC LIMIT 1`, uid, water, water)
	}
	var r CatchRecord
	err := row.Scan(&r.ID, &r.TS, &r.Loc, &r.Spot, &r.FishKey, &r.NameZh,
		&r.WeightG, &r.LengthM, &r.Source, &r.Bait, &r.Grade, &r.XP, &r.Silver,
		&r.LocLevel)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}
