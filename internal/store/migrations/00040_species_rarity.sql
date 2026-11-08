-- 物种稀有度（ecknbkpeeji.f4，jmpghlofkoc：0 普通 1 稀有 2 超稀有）。
-- 数据由 seed 从静态库 server_data/static.sqlite3 的 species_rarity 表整表导入
-- （rf4db.com 2026-11-06 抓取，252 条）。结算卡 tag5 与图鉴按它渲染紫/粉标。
CREATE TABLE IF NOT EXISTS species_rarity(
    fish_key TEXT PRIMARY KEY,
    rarity   INTEGER NOT NULL
) WITHOUT ROWID;
