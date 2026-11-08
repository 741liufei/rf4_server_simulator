-- 保留旧快照，按玩家和不可变竿键分别保存。
CREATE TABLE IF NOT EXISTS fishing_state_by_rod (
    uid INTEGER NOT NULL,
    gear_guid TEXT NOT NULL DEFAULT '',
    rig_type INTEGER NOT NULL DEFAULT 0,
    loc TEXT NOT NULL DEFAULT '',
    spot INTEGER NOT NULL DEFAULT 0,
    depth_cm INTEGER NOT NULL DEFAULT 0,
    cast_active INTEGER NOT NULL DEFAULT 0,
    cast_ts_ns INTEGER NOT NULL DEFAULT 0,
    fall_ts_ns INTEGER NOT NULL DEFAULT 0,
    fall_seen INTEGER NOT NULL DEFAULT 0,
    rig_settled INTEGER NOT NULL DEFAULT 0,
    tick_seen INTEGER NOT NULL DEFAULT 0,
    tick_seq INTEGER NOT NULL DEFAULT 0,
    last_anim INTEGER NOT NULL DEFAULT 0,
    push_sent INTEGER NOT NULL DEFAULT 0,
    hooked INTEGER NOT NULL DEFAULT 0,
    resolved INTEGER NOT NULL DEFAULT 0,
    pending_json TEXT,
    bait TEXT NOT NULL DEFAULT '',
    last_skill INTEGER NOT NULL DEFAULT 0,
    armed_json TEXT,
    bite_speed REAL NOT NULL DEFAULT 1.0,
    trophy_luck REAL NOT NULL DEFAULT 0.0,
    fight_tolerance REAL NOT NULL DEFAULT 0.0,
    bite_at_ns INTEGER NOT NULL DEFAULT 0,
    updated_ts REAL NOT NULL DEFAULT 0.0,
    rod_guid TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(uid, rod_guid)
);
INSERT OR IGNORE INTO fishing_state_by_rod
    SELECT uid,gear_guid,rig_type,loc,spot,depth_cm,cast_active,
        cast_ts_ns,fall_ts_ns,fall_seen,rig_settled,tick_seen,tick_seq,last_anim,
        push_sent,hooked,resolved,pending_json,bait,last_skill,armed_json,
        bite_speed,trophy_luck,fight_tolerance,bite_at_ns,updated_ts,gear_guid
    FROM fishing_state;
DROP TABLE fishing_state;
ALTER TABLE fishing_state_by_rod RENAME TO fishing_state;
