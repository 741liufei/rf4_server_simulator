package store

import (
	"context"
	"database/sql"
	"time"
)

// HookBiteProfile 是具体鱼钩型号的咬口配置。物理尺寸、强度和品质从 item_def
// 同步；四个 scale/floor 是服务端调参覆盖，默认值保持 1.0 / 0。
type HookBiteProfile struct {
	ItemID                string
	NumericID             int64
	Size                  int64
	LengthMM              float64
	StrengthKg            float64
	WireWeightG           float64
	Quality               int64
	Family                string
	Brand                 string
	SolidScale            float64
	HoldScale             float64
	SmallFishFloor        float64
	OverCapacityHoldFloor float64
	UpdatedTS             float64
}

func (d *DB) UpsertHookBiteProfile(ctx context.Context, p HookBiteProfile) error {
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO hook_bite_profile(
			item_id,numeric_id,size,length_mm,strength_kg,wire_weight_g,quality,family,brand,
			updated_ts
		) VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(item_id) DO UPDATE SET
			numeric_id=excluded.numeric_id,
			size=excluded.size,
			length_mm=excluded.length_mm,
			strength_kg=excluded.strength_kg,
			wire_weight_g=excluded.wire_weight_g,
			quality=excluded.quality,
			family=excluded.family,
			brand=excluded.brand,
			updated_ts=excluded.updated_ts`,
		p.ItemID, p.NumericID, p.Size, p.LengthMM, p.StrengthKg, p.WireWeightG,
		p.Quality, p.Family, p.Brand, p.UpdatedTS)
	return err
}

func scanHookBiteProfile(row interface{ Scan(...any) error }) (*HookBiteProfile, error) {
	var p HookBiteProfile
	if err := row.Scan(
		&p.ItemID, &p.NumericID, &p.Size, &p.LengthMM, &p.StrengthKg,
		&p.WireWeightG, &p.Quality, &p.Family, &p.Brand,
		&p.SolidScale, &p.HoldScale, &p.SmallFishFloor,
		&p.OverCapacityHoldFloor, &p.UpdatedTS,
	); err != nil {
		return nil, err
	}
	return &p, nil
}

func (d *DB) GetHookBiteProfile(ctx context.Context, itemID string) (*HookBiteProfile, error) {
	row := d.SQL.QueryRowContext(ctx, `
		SELECT item_id,numeric_id,size,length_mm,strength_kg,wire_weight_g,quality,family,brand,
		       solid_scale_bp/10000.0,hold_scale_bp/10000.0,
		       small_fish_floor_bp/10000.0,over_capacity_hold_floor_bp/10000.0,updated_ts
		FROM hook_bite_profile WHERE item_id=?`, itemID)
	p, err := scanHookBiteProfile(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return p, err
}

func (d *DB) ListHookBiteProfiles(ctx context.Context) ([]HookBiteProfile, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT item_id,numeric_id,size,length_mm,strength_kg,wire_weight_g,quality,family,brand,
		       solid_scale_bp/10000.0,hold_scale_bp/10000.0,
		       small_fish_floor_bp/10000.0,over_capacity_hold_floor_bp/10000.0,updated_ts
		FROM hook_bite_profile ORDER BY size, family, item_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HookBiteProfile
	for rows.Next() {
		p, err := scanHookBiteProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (d *DB) SetHookBiteProfileTuning(ctx context.Context, itemID string, solidScale, holdScale, smallFishFloor, overCapacityHoldFloor float64) error {
	_, err := d.SQL.ExecContext(ctx, `
		UPDATE hook_bite_profile
		SET solid_scale_bp=?, hold_scale_bp=?, small_fish_floor_bp=?,
		    over_capacity_hold_floor_bp=?, updated_ts=?
		WHERE item_id=?`,
		bpPercent(solidScale), bpPercent(holdScale), bpPercent(smallFishFloor),
		bpPercent(overCapacityHoldFloor), float64(time.Now().Unix()), itemID)
	return err
}

func bpPercent(v float64) int64 {
	return int64(v*10000 + 0.5)
}
