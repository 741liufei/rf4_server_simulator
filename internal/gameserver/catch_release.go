package gameserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"rf4server/internal/store"
)

type releaseReceipt struct {
	Level, XP, LevelsGained, Bonus int64
}

// releaseCatchOnce 放生奖励与消费 pending 同事务，重启后的重试不重复入账。
// rigType 是放生时所用钓组类型（11 级礼物按钓法分套的依据）。
// 第二返回值是本次直发的升级礼物行（仅非重放；重放回执返回 nil），
// 供调用方推 4/22——807 无奖励列表字段，礼物只在背包同步中体现。
func (s *Server) releaseCatchOnce(ctx context.Context, uid int64, fishGUID string, bonus, rigType int64) (releaseReceipt, []*store.Item, bool, error) {
	var result releaseReceipt
	var gifts []*store.Item
	if fishGUID == "" {
		return result, nil, false, fmt.Errorf("缺少放生鱼标识")
	}
	if _, err := s.DB.EnsureProfile(ctx, uid); err != nil {
		return result, nil, false, err
	}
	key := fmt.Sprintf("catch-release:%d:%s", uid, strings.ToLower(fishGUID))
	tx, err := s.DB.SQL.BeginTx(ctx, nil)
	if err != nil {
		return result, nil, false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE kv SET v=v WHERE k=?`, key); err != nil {
		return result, nil, false, err
	}
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT v FROM kv WHERE k=?`, key).Scan(&raw); err == nil {
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			return result, nil, true, err
		}
		return result, nil, true, nil
	} else if err != sql.ErrNoRows {
		return result, nil, false, err
	}
	var oldLevel, oldXP int64
	if err := tx.QueryRowContext(ctx, `SELECT level,xp FROM profile WHERE uid=?`, uid).Scan(&oldLevel, &oldXP); err != nil {
		return result, nil, false, err
	}
	if bonus < 0 {
		bonus = 0
	}
	result.XP = oldXP + bonus
	result.Level = levelForXp(result.XP)
	if result.Level < oldLevel {
		result.Level = oldLevel
	}
	if result.Level > maxLevel {
		result.Level = maxLevel
	}
	result.LevelsGained = max64(0, result.Level-oldLevel)
	result.Bonus = bonus
	if bonus > 0 {
		now := float64(time.Now().UnixNano()) / 1e9
		if _, err := tx.ExecContext(ctx, `UPDATE profile SET xp=?,level=?,updated_ts=? WHERE uid=?`, result.XP, result.Level, now, uid); err != nil {
			return result, nil, false, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO txn(uid,ts,kind,silver_delta,gold_delta,note) VALUES(?,?,'xp',0,0,'fishing_release')`, uid, now); err != nil {
			return result, nil, false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE fishing_state SET pending_json=NULL,cast_active=0,
		hooked=0,resolved=1 WHERE uid=? AND json_extract(pending_json,'$.fish_guid')=?`, uid, fishGUID); err != nil {
		return result, nil, false, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return result, nil, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO kv(k,v) VALUES(?,?)`, key, string(encoded)); err != nil {
		return result, nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return result, nil, false, err
	}
	// 官方升级礼物：真实升级（非重放）后逐级直发背包，内部按等级幂等。
	// bonus=0 时不写档，等级不可能真实变化，保持与结算同口径；
	// 聊天播报在发放函数内完成。
	if result.Bonus > 0 && result.LevelsGained > 0 {
		for _, g := range s.grantLevelUpGifts(ctx, uid, oldLevel, result.Level, fishingSkillCatForRig(rigType)) {
			gifts = append(gifts, g.Items...)
		}
	}
	return result, gifts, false, nil
}
