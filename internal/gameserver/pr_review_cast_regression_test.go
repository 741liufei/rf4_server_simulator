package gameserver

// 抛投校准 PR #188 的复审回归：硬度档枚举范围与前导重量覆盖。
// 与 #185 的 pr_review_gear_regression_test.go 分开命名，两个 PR 合并时
// 不会互相覆盖；testDef / f32Attr 等 helper 来自本分支已有的测试文件。

import (
	"fmt"
	"math"
	"testing"
)

// 硬度档实际是 0..10：8..10（多为船竿）不能静默拿到中性 1.0。
func TestCastRodStiffnessCoversFullEnum(t *testing.T) {
	if got := castRodStiffnessFactor(0); got != 1 {
		t.Fatalf("0=未登记/任意应中性 1.0，实得 %f", got)
	}
	if got := castRodStiffnessFactor(-3); got != 1 {
		t.Fatalf("非法负值应中性 1.0，实得 %f", got)
	}
	if got := castRodStiffnessFactor(1); math.Abs(got-0.990) > 1e-9 {
		t.Fatalf("1 档应为 0.990，实得 %f", got)
	}
	for _, stiffness := range []int64{7, 8, 9, 10, 11} {
		if got := castRodStiffnessFactor(stiffness); math.Abs(got-1.014) > 1e-9 {
			t.Fatalf("硬度 %d 应取上限 1.014，实得 %f", stiffness, got)
		}
	}
}

// 前导线静态定义缺长度时用实例 csQty 补位；定义有长度时实例数量不得覆盖。
func TestLeaderMassUsesInstanceLength(t *testing.T) {
	noLength := testDef(fmt.Sprintf(`{"ocmbfkebgpp":6,"ciichedlnjh":%d}`, f32Attr(0.50)))
	if got := leaderMassG(noLength, 0); got != 0 {
		t.Fatalf("定义与实例都没长度时应返回 0，实得 %f", got)
	}
	short, long := leaderMassG(noLength, 15), leaderMassG(noLength, 30)
	if short <= 0 || math.Abs(long-2*short) > 1e-6 {
		t.Fatalf("实例长度应线性补位: 15m=%f 30m=%f", short, long)
	}
	// 成品前导的 hkgndoipdlc 是厘米，实例 csQty=1 件不能盖掉 200cm。
	withLength := testDef(fmt.Sprintf(
		`{"hkgndoipdlc":%d,"mbnambppbom":{"ciichedlnjh":%d,"ocmbfkebgpp":1}}`,
		f32Attr(200.0), f32Attr(0.35)))
	if math.Abs(leaderMassG(withLength, 1)-leaderMassG(withLength, 0)) > 1e-6 {
		t.Fatal("实例数量覆盖了定义里的前导长度")
	}
	// cat13 有 52/181 条既无内嵌线段也无顶层线径，兜底不能整类返回 0 g。
	noDiameter := testDef(fmt.Sprintf(`{"lgjeahdobch":%d,"ljnnpokapig":%d}`,
		f32Attr(0.02), f32Attr(2.0)))
	if got := leaderMassG(noDiameter, 0); got <= 0 {
		t.Fatalf("无线径时兜底应给出正重量，实得 %f", got)
	}
}
