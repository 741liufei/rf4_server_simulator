CREATE TABLE IF NOT EXISTS skill_fish_effect_def(
    cat_id      INTEGER NOT NULL,
    node_id     INTEGER NOT NULL,
    effect_key  TEXT NOT NULL,
    per_level   REAL NOT NULL,
    cap         REAL NOT NULL,
    source      TEXT NOT NULL DEFAULT 'local-sim',
    confidence  REAL NOT NULL DEFAULT 0.5,
    PRIMARY KEY(cat_id,node_id)
);
