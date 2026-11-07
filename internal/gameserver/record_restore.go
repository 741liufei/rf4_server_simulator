package gameserver

import (
	"context"
	"fmt"
	"math"

	"rf4server/internal/store"
)

// PrepareRestoredRecord uses the same species ruler and length formula as fishing.
// A zero weight selects the smallest weight in the requested grade interval.
func PrepareRestoredRecord(ctx context.Context, db *store.DB, key string, grade, weight int64) (store.CatchRecord, error) {
	r := store.CatchRecord{FishKey: key, Grade: grade}
	if grade < 0 || grade > 4 {
		return r, fmt.Errorf("无效档位")
	}
	if grade == 0 {
		return r, nil
	}
	s := &Server{DB: db}
	lines := s.settleStatics(ctx, key)
	if weight == 0 {
		for _, w := range []int64{1, max(1, lines[0]), max(1, lines[1]), max(1, lines[2])} {
			if s.rollGradeDB(ctx, key, w) == grade && (weight == 0 || w < weight) {
				weight = w
			}
		}
	}
	if weight <= 0 || weight > math.MaxUint32 || s.rollGradeDB(ctx, key, weight) != grade {
		return r, fmt.Errorf("%s：重量不在所选档位内，或该鱼种没有此档位", key)
	}
	k, p := db.SpeciesLengthKP(ctx, key)
	r.WeightG, r.LengthM = weight, k*math.Pow(float64(weight), p)
	return r, nil
}
