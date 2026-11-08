-- 资格任务进度：counted_json 为已计数对象 GUID 集合（客户端计数器 5004.f1 的 Guid[]，
-- 进度分子 = 集合长度），completed_ts 为达标时刻（客户端 5004.f2 > 0 即显示完成）。
CREATE TABLE IF NOT EXISTS player_qualifications(
    uid INTEGER NOT NULL,
    rule_id INTEGER NOT NULL,
    counted_json TEXT NOT NULL DEFAULT '[]',
    completed_ts REAL NOT NULL DEFAULT 0,
    PRIMARY KEY(uid, rule_id)
);