package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// QualificationFish 一条已计入资格进度的渔获记录。
// 客户端把计数器 Guid[] 里的每个 GUID 拿去 5003 内嵌的鱼记录列表（nanehfeohjh）
// 查同一尾鱼来渲染任务鱼表，因此进度集合必须连同鱼记录一起持久化，
// 不能只存 GUID（鱼护清空后记录仍在）。
type QualificationFish struct {
	GUID    string  `json:"guid"`
	Species string  `json:"species"`
	NameZh  string  `json:"name_zh"`
	WeightG int64   `json:"weight_g"`
	LengthM float64 `json:"length_m"`
	Grade   int64   `json:"grade"`
	RigType int64   `json:"rig_type"`
	RodType int64   `json:"rod_type"`
}

// QualificationState 一条资格任务的持久化进度。
// 客户端进度分子 = Counted 长度（Guid[] 长度），CompletedTS 为达标时刻（0=未完成）。
type QualificationState struct {
	RuleID      int64
	Counted     []QualificationFish
	CompletedTS float64
}

// qualificationStates 用统一的查询接口读取进度，供普通连接与结算事务共用。
type qualificationQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func qualificationStates(ctx context.Context, q qualificationQuerier, uid int64) (map[int64]QualificationState, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT rule_id, counted_json, completed_ts FROM player_qualifications WHERE uid=?`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]QualificationState)
	for rows.Next() {
		var st QualificationState
		var raw string
		if err := rows.Scan(&st.RuleID, &raw, &st.CompletedTS); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &st.Counted); err != nil {
			return nil, err
		}
		out[st.RuleID] = st
	}
	return out, rows.Err()
}

func upsertQualificationState(ctx context.Context, q qualificationQuerier, uid int64, st QualificationState) error {
	raw, err := json.Marshal(st.Counted)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `
		INSERT INTO player_qualifications(uid, rule_id, counted_json, completed_ts)
		VALUES(?,?,?,?)
		ON CONFLICT(uid, rule_id) DO UPDATE SET
			counted_json=excluded.counted_json, completed_ts=excluded.completed_ts`,
		uid, st.RuleID, string(raw), st.CompletedTS)
	return err
}

// QualificationStates 纯查询：读取玩家全部资格任务进度。
func (d *DB) QualificationStates(ctx context.Context, uid int64) (map[int64]QualificationState, error) {
	return qualificationStates(ctx, d.SQL, uid)
}

// QualificationStatesTx 在结算事务内读取资格进度，保证判定与写入同一快照。
func QualificationStatesTx(ctx context.Context, tx *sql.Tx, uid int64) (map[int64]QualificationState, error) {
	return qualificationStates(ctx, tx, uid)
}

// UpsertQualificationState 写入一条资格任务进度（独立事务入口）。
func (d *DB) UpsertQualificationState(ctx context.Context, uid int64, st QualificationState) error {
	return upsertQualificationState(ctx, d.SQL, uid, st)
}

// UpsertQualificationStateTx 在调用方事务内写入一条资格任务进度。
func UpsertQualificationStateTx(ctx context.Context, tx *sql.Tx, uid int64, st QualificationState) error {
	return upsertQualificationState(ctx, tx, uid, st)
}
