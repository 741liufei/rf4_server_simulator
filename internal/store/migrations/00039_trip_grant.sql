-- 钓鱼游装备发放记录（gameserver/trip_equip.go）：购票发放、到期回收。
--
-- 一行 = 一件已发放物品。发放时登记 guid，回收后置 recalled=1（保留行做
-- 幂等护栏：HasUnrecalledTripGrant 为真时重复购票不重复发放）。
CREATE TABLE IF NOT EXISTS trip_grant (
    uid INTEGER NOT NULL,
    place TEXT NOT NULL,
    guid TEXT NOT NULL,
    item_id TEXT NOT NULL,
    recalled INTEGER NOT NULL DEFAULT 0,
    granted_ts REAL NOT NULL,
    PRIMARY KEY(uid, place, guid)
);
CREATE INDEX IF NOT EXISTS idx_trip_grant_pending ON trip_grant(uid, place, recalled);
