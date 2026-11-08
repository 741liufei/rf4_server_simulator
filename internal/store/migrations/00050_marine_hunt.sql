-- 海图狩猎与淡水窝料狩猎共用进度/保底，但触发器不同：
-- 淡水看窝料，海图看声呐区号、海图动作和鱼群习性。
ALTER TABLE fish_hunt_state ADD COLUMN hunt_mode INTEGER NOT NULL DEFAULT 0;
ALTER TABLE fish_hunt_state ADD COLUMN sea_zone INTEGER NOT NULL DEFAULT 0;
ALTER TABLE fish_hunt_state ADD COLUMN sea_zone_known INTEGER NOT NULL DEFAULT 0;
