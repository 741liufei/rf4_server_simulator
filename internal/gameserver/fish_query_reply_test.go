package gameserver

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestFishQueryReplyCompactReference(t *testing.T) {
	result := &FishQueryResponse{AsOf: "2026-11-08T05:38:00Z", Plans: []FishQueryPlan{
		{MapName: "旧奥斯特罗格湖", BaitName: "蚯蚓", RigName: "浮钓", Chance: .183, TheoreticalPerHour: 65.88,
			References: []FishQueryReference{{Coordinate: "45:12", Catches: 100}}},
		{MapName: "旧奥斯特罗格湖", BaitName: "蛆", RigName: "底钓", Chance: .125, TheoreticalPerHour: 45,
			References: []FishQueryReference{{Coordinate: "45:12", Catches: 100}}},
	}}
	want := "旧奥斯特罗格湖 · 河鲈\n" +
		"1. 蚯蚓 · 浮钓 · 理论 65.88条/小时\n" +
		"2. 蛆 · 底钓 · 理论 45.00条/小时\n" +
		"攻略参考点：45:12（未参与概率计算）\n" +
		"理论速率不计搏鱼、收鱼时间。"
	if got := formatFishQueryReply("河鲈", result); got != want {
		t.Fatalf("reply = %q; want %q", got, want)
	}
}

func TestFishQueryReplyKeepsCalculatedLocationsSeparate(t *testing.T) {
	x, z := 425.0, 495.0
	result := &FishQueryResponse{Plans: []FishQueryPlan{
		{MapName: "测试湖", BaitName: "饵一", RigName: "路亚", Action: "jig", Chance: .1, X: &x, Z: &z,
			CoordinateKind: "manual", References: []FishQueryReference{{Coordinate: "99:99"}}},
	}}
	reply := formatFishQueryReply("河鲈", result)
	if !strings.Contains(reply, "路亚·阶梯") || !strings.Contains(reply, "世界落点 X 425.0 / Z 495.0") ||
		strings.Contains(reply, "海图区") || strings.Contains(reply, "99:99") {
		t.Fatalf("manual position mixed with a chart zone or reference: %s", reply)
	}
	result.Plans[0].MapName, result.Plans[0].Zone, result.Plans[0].CoordinateKind = "挪威海", 7, "sea_grid"
	if reply := formatFishQueryReply("鳕鱼", result); !strings.Contains(reply, "（海图区7）") {
		t.Fatalf("missing chart zone: %s", reply)
	}
}

func TestFishQueryReplyLimitedAndNoInventedCoordinate(t *testing.T) {
	result := &FishQueryResponse{}
	if got := formatFishQueryReply("河鲈", result); got != "河鲈\n当前地图、当前条件没有可行方案，可指定其他地图。" {
		t.Fatalf("empty reply = %s", got)
	}
	for range 4 {
		result.Plans = append(result.Plans, FishQueryPlan{MapName: "测试湖", BaitName: "饵", RigName: "浮钓", Chance: .001, TheoreticalPerHour: .36, Action: "drift"})
	}
	got := formatFishQueryReply("河鲈", result)
	if strings.Contains(got, "4.") || !strings.Contains(got, "3. 饵 · 浮钓·顺水漂流 · 理论 0.36条/小时") ||
		strings.Contains(got, "参考点") || strings.Contains(got, "落点") || strings.Contains(got, "饵深") {
		t.Fatalf("too many plans, unnecessary fields or invented position: %s", got)
	}
}

func TestFishQueryHelpWaitNotice(t *testing.T) {
	if !strings.Contains(fishQueryHelp, "约3秒") || !strings.Contains(fishQueryHelp, "个人消息返回") {
		t.Fatalf("help does not explain query wait: %s", fishQueryHelp)
	}
}

func TestFishQueryReplyExplanationsMovedToHelp(t *testing.T) {
	result := &FishQueryResponse{WaterFiltered: true, Plans: []FishQueryPlan{{MapName: "铜湖", BaitName: "饵", RigType: 5, CoordinateKind: "inland_region", Region: &FishQueryRegion{MinX: 40, MinZ: 48, MaxX: 48, MaxZ: 56}}}}
	got := formatFishQueryReply("绯写锦鲤", result)
	for _, detail := range []string{"粗筛", "不保证", "五点", "混合格", "预测", "单竿"} {
		if strings.Contains(got, detail) || !strings.Contains(fishQueryHelp, detail) {
			t.Fatalf("detail %q should be help-only: reply=%s help=%s", detail, got, fishQueryHelp)
		}
	}
	if !strings.HasSuffix(got, "理论速率不计搏鱼、收鱼时间。") {
		t.Fatalf("missing short rate explanation: %s", got)
	}
}

func TestFishQuerySpeciesOnlyHelp(t *testing.T) {
	if !strings.HasPrefix(fishQueryHelp, "发送：鱼名 [地图]\n") {
		t.Fatalf("help does not describe species-only syntax: %s", fishQueryHelp)
	}
	for _, removed := range []string{"普通", "达标", "星", "奖杯", "蓝", "档位"} {
		if strings.Contains(fishQueryHelp, removed) {
			t.Fatalf("help still advertises grade %q: %s", removed, fishQueryHelp)
		}
	}
}

func TestFishQuerySpeciesOnlyCommands(t *testing.T) {
	s, db := seedBaitWeightTest(t)
	const uid int64 = 1000001
	const mapKey = "level_015_tunguska"
	if _, err := db.SQL.Exec(`UPDATE loc_alias SET alias=? WHERE alias='level_900_rules'`, mapKey); err != nil {
		t.Fatal(err)
	}
	if _, err := db.EnsureProfile(t.Context(), uid); err != nil {
		t.Fatal(err)
	}
	s.setLevel(uid, mapKey)
	sess := &Session{Srv: s, Userid: uid}
	for _, text := range []string{"常见拟鲤", "常见拟鲤 rules-test", "查鱼 常见拟鲤", "查询 常见拟鲤 " + mapKey} {
		t.Run(text, func(t *testing.T) {
			got := s.fishQueryReply(t.Context(), sess, text)
			if !strings.HasPrefix(got, "rules-test · 常见拟鲤\n1. ") || strings.Contains(got, "及以上") {
				t.Fatalf("species-only query returned %q", got)
			}
		})
	}
	for _, grade := range []string{"普通", "达标", "星", "奖杯", "蓝", "蓝冠"} {
		t.Run("removed/"+grade, func(t *testing.T) {
			if got := s.fishQueryReply(t.Context(), sess, "常见拟鲤 "+grade); got != "地图未识别，请用地图全名或level编号" {
				t.Fatalf("grade suffix is still interpreted as a filter: %q", got)
			}
			if got := s.fishQueryReply(t.Context(), sess, "常见拟鲤 "+mapKey+" "+grade); got != fishQueryHelp {
				t.Fatalf("extra grade argument is still accepted: %q", got)
			}
		})
	}
}

func TestFishQueryReplyExpiredDeadline(t *testing.T) {
	s, _ := seedBaitWeightTest(t)
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	// A timeout during name resolution must not be reported as unavailable data.
	got := s.fishQueryReply(ctx, &Session{Srv: s, Userid: 1000001}, "常见拟鲤")
	if want := "查询超时（计算预算3秒），请稍后重试。"; got != want {
		t.Fatalf("timeout reply=%q, want %q", got, want)
	}
}
