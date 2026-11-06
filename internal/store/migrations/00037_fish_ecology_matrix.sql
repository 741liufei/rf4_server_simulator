ALTER TABLE fish_ecology ADD COLUMN season_mask INTEGER NOT NULL DEFAULT 15;
ALTER TABLE fish_ecology ADD COLUMN substrate_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE fish_ecology ADD COLUMN color_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE fish_ecology ADD COLUMN current_pref REAL NOT NULL DEFAULT 0.5;
ALTER TABLE fish_ecology ADD COLUMN activity REAL NOT NULL DEFAULT 1.0;
ALTER TABLE fish_ecology ADD COLUMN light_pref REAL NOT NULL DEFAULT 0.5;
ALTER TABLE fish_ecology ADD COLUMN clarity_pref REAL NOT NULL DEFAULT 0.5;
ALTER TABLE fish_ecology ADD COLUMN profile_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE fish_ecology ADD COLUMN source TEXT NOT NULL DEFAULT 'local-sim:deterministic-v3';
ALTER TABLE fish_ecology ADD COLUMN rarity_tier INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_fish_ecology_season
    ON fish_ecology(season_mask);
