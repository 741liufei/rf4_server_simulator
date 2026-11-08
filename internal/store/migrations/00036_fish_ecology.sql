CREATE TABLE IF NOT EXISTS fish_ecology(
    fish_key       TEXT PRIMARY KEY,
    layer_min      REAL NOT NULL DEFAULT 0.0,
    layer_pref     REAL NOT NULL DEFAULT 0.5,
    layer_max      REAL NOT NULL DEFAULT 1.0,
    temp_min_c     REAL NOT NULL DEFAULT 5.0,
    temp_pref_c    REAL NOT NULL DEFAULT 18.0,
    temp_max_c     REAL NOT NULL DEFAULT 28.0,
    diel_mask      INTEGER NOT NULL DEFAULT 15,
    diet_json      TEXT NOT NULL DEFAULT '{}',
    weather_json   TEXT NOT NULL DEFAULT '{}',
    action_json    TEXT NOT NULL DEFAULT '{}',
    aggression     REAL NOT NULL DEFAULT 1.0,
    enabled        INTEGER NOT NULL DEFAULT 1,
    updated_ts     REAL NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_fish_ecology_layer
    ON fish_ecology(layer_min, layer_max);
