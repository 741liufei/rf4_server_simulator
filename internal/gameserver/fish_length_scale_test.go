package gameserver

import (
	"context"
	"math"
	"testing"
)

// 体长系数校准：同一重量下，狗鱼（细长）必须比鲤（短厚）显示得更长；
// 已有官方标定曲线和未列入校准表的鱼种必须保持原值。
func TestCalibratedLengthKP(t *testing.T) {
	t.Parallel()
	pikeK, pikeP := calibratedLengthKP("n.pike", defaultSpeciesLengthK, defaultSpeciesLengthP)
	if math.Abs(pikeK-0.065519) > 1e-9 || math.Abs(pikeP-defaultSpeciesLengthP) > 1e-12 {
		t.Fatalf("n.pike 未应用校准: k=%v p=%v", pikeK, pikeP)
	}
	carpK, carpP := calibratedLengthKP("carp", defaultSpeciesLengthK, defaultSpeciesLengthP)
	if math.Abs(carpK-0.044208) > 1e-9 || math.Abs(carpP-defaultSpeciesLengthP) > 1e-12 {
		t.Fatalf("carp 未应用校准: k=%v p=%v", carpK, carpP)
	}
	if pikeK <= carpK {
		t.Fatalf("狗鱼系数应大于鲤: pike=%v carp=%v", pikeK, carpK)
	}
	if got := pikeK * math.Pow(5000, pikeP); got < 0.9 || got > 1.3 {
		t.Fatalf("5kg 狗鱼长度=%.2fm 偏离现实区间", got)
	}
	if got := carpK * math.Pow(5000, carpP); got < 0.55 || got > 0.85 {
		t.Fatalf("5kg 鲤长度=%.2fm 偏离现实区间", got)
	}

	// 静态库里的非默认 k/p 是官方样本标定曲线，不能被现实体型校准覆盖。
	official := []struct {
		key string
		k   float64
		p   float64
	}{
		{"perch", 0.021948, 0.40868},
		{"c.roach", 0.033498, 0.34010},
		{"s.bream", 0.066818, 0.20866},
		{"crucian", 0.035129, 1.0 / 3.0},
	}
	for _, c := range official {
		if gotK, gotP := calibratedLengthKP(c.key, c.k, c.p); gotK != c.k || gotP != c.p {
			t.Fatalf("%s 官方曲线被覆盖: got=(%v,%v) want=(%v,%v)", c.key, gotK, gotP, c.k, c.p)
		}
	}

	// 未收录鱼种和非鱼类保持静态库原值。
	if gotK, gotP := calibratedLengthKP("not_a_fish", defaultSpeciesLengthK, defaultSpeciesLengthP); gotK != defaultSpeciesLengthK || gotP != defaultSpeciesLengthP {
		t.Fatalf("未校准鱼种被改写: k=%v p=%v", gotK, gotP)
	}
	if gotK, gotP := calibratedLengthKP("crawfish", defaultSpeciesLengthK, defaultSpeciesLengthP); gotK != defaultSpeciesLengthK || gotP != defaultSpeciesLengthP {
		t.Fatalf("非鱼类不应参与校准: k=%v p=%v", gotK, gotP)
	}
}

func TestCachedSpeciesLengthKPAppliesCalibration(t *testing.T) {
	t.Parallel()
	s, db := newRigTestServer(t)
	if _, err := db.SQL.Exec(`INSERT OR REPLACE INTO species_weight(fish_key,min_g,max_g,trophy_g,src,length_k,length_p,xp_c,xp_q,trophy_line_g,super_trophy_line_g,avg_g,samples)
		VALUES('carp',50,5000,2000,'test',0.0355,0.3333333333333333,0,0,0,0,0,0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT OR REPLACE INTO species_weight(fish_key,min_g,max_g,trophy_g,src,length_k,length_p,xp_c,xp_q,trophy_line_g,super_trophy_line_g,avg_g,samples)
		VALUES('c.roach',80,900,318,'test',0.033498,0.34010,0,0,0,0,0,0)`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, c := range []fishRollContext{{}, {cache: newFishingRollCache()}} {
		if k, p := s.cachedSpeciesLengthKP(ctx, c, "carp"); math.Abs(k-0.044208) > 1e-9 || math.Abs(p-defaultSpeciesLengthP) > 1e-12 {
			t.Fatalf("cached carp 未应用校准: k=%v p=%v", k, p)
		}
		if k, p := s.cachedSpeciesLengthKP(ctx, c, "c.roach"); k != 0.033498 || p != 0.34010 {
			t.Fatalf("cached 官曲线被覆盖: k=%v p=%v", k, p)
		}
	}
}
