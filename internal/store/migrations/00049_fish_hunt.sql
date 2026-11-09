-- 窝料实际落点与逐钓点大鱼狩猎状态。
-- 旧记录没有 x/z，运行时回退到 bx/bz 格子中心。
ALTER TABLE groundbait_deposit ADD COLUMN landing_x REAL;
ALTER TABLE groundbait_deposit ADD COLUMN landing_z REAL;

CREATE INDEX IF NOT EXISTS groundbait_deposit_uid_loc
    ON groundbait_deposit(uid, loc);

CREATE TABLE IF NOT EXISTS fish_hunt_state(
    uid             INTEGER NOT NULL,
    loc             TEXT    NOT NULL,
    spot            INTEGER NOT NULL,
    anchor_x        REAL    NOT NULL DEFAULT 0,
    anchor_z        REAL    NOT NULL DEFAULT 0,
    anchor_known    INTEGER NOT NULL DEFAULT 0,
    active_seconds  REAL    NOT NULL DEFAULT 0,
    valid_bites     INTEGER NOT NULL DEFAULT 0,
    pity            INTEGER NOT NULL DEFAULT 0,
    big_miss        INTEGER NOT NULL DEFAULT 0,
    comp_fish       INTEGER NOT NULL DEFAULT 0,
    comp_grade      INTEGER NOT NULL DEFAULT 0,
    comp_until      REAL    NOT NULL DEFAULT 0,
    last_catch_guid TEXT    NOT NULL DEFAULT '',
    last_active_ts  REAL    NOT NULL DEFAULT 0,
    updated_ts      REAL    NOT NULL DEFAULT 0,
    PRIMARY KEY(uid, loc, spot)
);

CREATE INDEX IF NOT EXISTS fish_hunt_state_uid_loc
    ON fish_hunt_state(uid, loc);
