-- 本杆实际抛投距离（14/2 上报，米）。资格「抛投 ≥N 米」必须按本杆落点判定，
-- 不能取钓具理论最大值；服务端在 14/2 后到咬钩前重启时也要保持该值。
ALTER TABLE fishing_state ADD COLUMN cast_m REAL NOT NULL DEFAULT 0;
