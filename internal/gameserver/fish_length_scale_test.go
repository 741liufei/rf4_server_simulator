package gameserver

import (
	"math"
	"testing"
)

// 体长系数校准：同一重量下，狗鱼（细长）必须比鲤（短厚）显示得更长；
// 未列入校准表的鱼种保持静态库原值。
func TestCalibratedLengthK(t *testing.T) {
	const defaultK = 0.0355
	if got := calibratedLengthK("carp", defaultK); math.Abs(got-0.044208) > 1e-9 {
		t.Fatalf("carp length_k=%v 未应用校准", got)
	}
	if got := calibratedLengthK("n.pike", defaultK); math.Abs(got-0.065519) > 1e-9 {
		t.Fatalf("n.pike length_k=%v 未应用校准", got)
	}
	pike := calibratedLengthK("n.pike", defaultK)
	carp := calibratedLengthK("carp", defaultK)
	if pike <= carp {
		t.Fatalf("狗鱼系数应大于鲤: pike=%v carp=%v", pike, carp)
	}
	// 5kg 时的显示长度应落在现实体长区间。
	if got := pike * math.Pow(5000, 1.0/3.0); got < 0.9 || got > 1.3 {
		t.Fatalf("5kg 狗鱼长度=%.2fm 偏离现实区间", got)
	}
	if got := carp * math.Pow(5000, 1.0/3.0); got < 0.55 || got > 0.85 {
		t.Fatalf("5kg 鲤长度=%.2fm 偏离现实区间", got)
	}
	// 未列入校准的鱼种原样返回。
	if got := calibratedLengthK("not_a_fish", defaultK); got != defaultK {
		t.Fatalf("未校准鱼种被改写: %v", got)
	}
}
