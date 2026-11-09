CREATE TABLE IF NOT EXISTS hook_bite_profile (
    item_id TEXT PRIMARY KEY,
    numeric_id INTEGER NOT NULL DEFAULT 0,
    size INTEGER NOT NULL DEFAULT 0,
    length_mm REAL NOT NULL DEFAULT 0,
    strength_kg REAL NOT NULL DEFAULT 0,
    wire_weight_g REAL NOT NULL DEFAULT 0,
    quality INTEGER NOT NULL DEFAULT 0,
    family TEXT NOT NULL DEFAULT '',
    brand TEXT NOT NULL DEFAULT '',
    solid_scale_bp INTEGER NOT NULL DEFAULT 10000,
    hold_scale_bp INTEGER NOT NULL DEFAULT 10000,
    small_fish_floor_bp INTEGER NOT NULL DEFAULT 0,
    over_capacity_hold_floor_bp INTEGER NOT NULL DEFAULT 0,
    updated_ts REAL NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_hook_bite_profile_size_family
    ON hook_bite_profile(size, family);
