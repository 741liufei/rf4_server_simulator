-- 玩家状态条扩列：舒适度（308.f3）与幸运惩罚（308.f4，HUD 显示 1-f4）。
-- 本轮只落库与透传常量，动态机制留待后续 spec（specs/changes/player-condition-core）；
-- 精力与饱食度沿用 primary_value / secondary_value 原列。
ALTER TABLE player_condition ADD COLUMN comfort_value REAL NOT NULL DEFAULT 1.0;
ALTER TABLE player_condition ADD COLUMN luck_penalty REAL NOT NULL DEFAULT 0.0;
