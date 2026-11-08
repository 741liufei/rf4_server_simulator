-- 钓鱼游状态（3/38 购买，见 gameserver/world.go hPlaceTripTx）。
--
-- remaining_sec 是剩余在线时长（现实秒）：只在会话在线时被 keepalive tick
-- 扣减，last_consume_ts 是上次扣减时刻；会话关闭即停表（离线暂停计时），
-- 新会话首次消费时只重置 last_consume_ts，不扣离线间隔。
-- purchases 记录该地点已购次数（客户端限购 3 次），到期后保留供限购判断；
-- place_access(kind='trip') 只表达"当前有效"，到期由服务端删除。
CREATE TABLE IF NOT EXISTS trip_state (
    uid INTEGER NOT NULL,
    place TEXT NOT NULL,
    remaining_sec REAL NOT NULL,
    last_consume_ts REAL NOT NULL,
    purchases INTEGER NOT NULL DEFAULT 0,
    updated_ts REAL NOT NULL,
    PRIMARY KEY(uid, place)
);
