package gameserver

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"rf4server/internal/proto"
)

// The assistant is a virtual contact, not a player account or an administrator.
const fishQueryContactUID int64 = 2147483647
const fishQueryContactName = "查鱼助手"

func hSocialMembers(ctx *Ctx) ([]byte, bool) {
	s := ctx.Sess.Srv
	ids := []uint32{uint32(fishQueryContactUID), uint32(gearAssistantUID)}
	seen := map[int64]bool{fishQueryContactUID: true, gearAssistantUID: true}
	s.mu.Lock()
	for _, peer := range s.sessions {
		if peer != nil && !peer.Closed() && peer.Userid > 0 && !seen[peer.Userid] {
			ids = append(ids, uint32(peer.Userid))
			seen[peer.Userid] = true
		}
	}
	s.mu.Unlock()
	sort.Slice(ids[2:], func(i, j int) bool { return ids[i+2] < ids[j+2] })
	return encodeSocialTree(ctx, proto.U32Arr(ids))
}

const fishQueryHelp = "发送：鱼名 [地图]\n例如：河鲈；河鲈 下通古斯卡河。\n鱼名支持拼音前缀，如“圆腹”或“yuanfu”；多种匹配直接取第一种。\n默认当前地图，最多推荐三组区域、鱼饵和钓法。仅支持已核对坐标的地图；按当前条件估算理论条/小时，不含客户端等待、非实际鱼获。\n查询一般很快；复杂地图请预留约3秒，结果通过个人消息返回。\n\n" +
	"计算说明：单竿、单饵无窝料，先粗筛区域再配饵，不保证全图最优。区域估算不逐点计算水深、底质和水流。\n" +
	"水陆说明：有本图数据时做中心与四角五点粗筛，任一点有水即保留；混合格可能含陆地，不保证可达。缺少本图数据时未排除陆地。\n" +
	"理论速率不含客户端等待、占竿、搏鱼、收鱼、重抛及落水就位保护，不是实际鱼获或未来一小时预测。"
const fishQueryBudget = 3 * time.Second
const fishQueryTimeoutReply = "查询超时（计算预算3秒），请稍后重试。"

func (s *Server) fishQueryReply(ctx context.Context, sess *Session, text string) (reply string) {
	defer func() {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			reply = fishQueryTimeoutReply
		}
	}()
	args := strings.Fields(text)
	if len(args) == 0 || text == "帮助" || text == "菜单" || strings.EqualFold(text, "help") {
		return fishQueryHelp
	}
	if args[0] == "查鱼" || args[0] == "查询" {
		args = args[1:]
	}
	if len(args) == 0 || len(args) > 2 {
		return fishQueryHelp
	}
	// The contact queries the whole species, without a trophy-grade filter.
	req := FishQueryRequest{UID: sess.Userid, Map: s.currentLevel(sess.Userid), Grade: 1}
	var err error
	req.Fish, err = s.resolveFishQuerySpecies(ctx, args[0])
	if err != nil {
		return err.Error()
	}
	if len(args) == 2 {
		req.Map, err = s.resolveFishQueryMap(ctx, args[1])
		if err != nil {
			return err.Error()
		}
	}
	if s.tunableF("fish_weight_cap", 0) > 0 || s.tunableF("hook_entry_cap", 0) > 0 {
		st := s.stateFor(req.UID)
		st.mu.Lock()
		req.Gear = st.GearGuid
		st.mu.Unlock()
	}
	result, err := s.QueryFish(ctx, req)
	if err != nil {
		if errors.Is(err, errFishQueryRegionMapUnsupported) {
			return err.Error()
		}
		if errors.Is(err, errFishQueryWaterInvalid) {
			s.log.Warn("查鱼水陆数据无效", "err", err)
			return errFishQueryWaterInvalid.Error()
		}
		s.log.Warn("查鱼失败", "uid", req.UID, "err", err)
		if ctx.Err() != nil {
			return fishQueryTimeoutReply
		}
		if req.Gear == "" && (s.tunableF("fish_weight_cap", 0) > 0 || s.tunableF("hook_entry_cap", 0) > 0) {
			return "装备限重已开启，请先拿起要使用的鱼竿再查询。"
		}
		return "暂时无法查询，请确认地图和鱼种后重试。"
	}
	return formatFishQueryReply(s.speciesZhName(ctx, req.Fish), result)
}

func formatFishQueryReply(fishName string, result *FishQueryResponse) string {
	fishName = fishQueryName(fishName)
	if len(result.Plans) == 0 {
		return fishName + "\n当前地图、当前条件没有可行方案，可指定其他地图。"
	}
	lines := []string{result.Plans[0].MapName + " · " + fishName}
	reference := ""
	uniform := false
	for i, plan := range result.Plans[:min(3, len(result.Plans))] {
		method := fishQueryMethodLabel(plan)
		if plan.Action == "drift" {
			method += "·顺水漂流"
		} else if plan.Action == "jig" {
			method += "·阶梯"
		}
		lines = append(lines, fmt.Sprintf("%d. %s · %s · 理论 %.2f条/小时", i+1, plan.BaitName, method, plan.TheoreticalPerHour))
		if plan.Region != nil {
			lines = append(lines, "   "+formatFishQueryRegion(*plan.Region))
		} else if plan.X != nil && plan.Z != nil {
			point := fmt.Sprintf("   世界落点 X %.1f / Z %.1f", *plan.X, *plan.Z)
			if plan.Zone > 0 {
				point += fmt.Sprintf("（海图区%d）", plan.Zone)
			}
			lines = append(lines, point)
		} else if reference == "" && len(plan.References) > 0 {
			reference = plan.References[0].Coordinate
		}
		uniform = uniform || plan.CoordinateKind == "region_uniform"
	}
	if reference != "" {
		lines = append(lines, "攻略参考点："+reference+"（未参与概率计算）")
	}
	if uniform {
		lines = append(lines, "鱼群分布未开启，不推荐区域。")
	}
	lines = append(lines, "理论速率不计搏鱼、收鱼时间。")
	return strings.Join(lines, "\n")
}

func formatFishQueryRegion(r FishQueryRegion) string {
	var labels [4]string
	approx := ""
	for i, value := range [4]float64{r.MinX, r.MinZ, r.MaxX, r.MaxZ} {
		rounded := math.Round(value*100) / 100
		if math.Abs(rounded-value) > 1e-9 {
			approx = "约"
		}
		labels[i] = strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", rounded), "0"), ".")
	}
	return fmt.Sprintf("区域%s %s:%s～%s:%s（上限不含）", approx, labels[0], labels[1], labels[2], labels[3])
}

func fishQueryMethodLabel(plan FishQueryPlan) string {
	switch plan.RigType {
	case 1:
		return "浮钓"
	case 12:
		return "底钓"
	case 5:
		return "路亚"
	case 50:
		return "海钓铁板"
	case 51:
		return "海钓软饵"
	case 52:
		return "海钓鱼肉"
	case 55:
		return "海钓底钓"
	}
	return plan.RigName
}

func (s *Server) receiveFishQuery(ctx *Ctx, text string) ([]byte, bool) {
	if !ctx.ExpectResponse {
		return nil, true
	}
	queryCtx, cancel := context.WithTimeout(ctx.Sess.pushCtx(), fishQueryBudget)
	defer cancel()
	text = strings.TrimSpace(text)
	reply := fishQueryHelp
	if len([]rune(text)) <= 100 {
		reply = s.fishQueryReply(queryCtx, ctx.Sess, text)
	}
	c := ctx.Sess.pushCtx()
	tx, err := beginEconomyTx(c, s.DB)
	if err != nil {
		ctx.ReplyFailure("System.IO.IOException", "查询回复保存失败")
		return nil, true
	}
	defer func() { _ = tx.Rollback() }()
	id, err := insertContactReply(c, tx, ctx.Sess.Userid, fishQueryContactUID, reply)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		ctx.ReplyFailure("System.IO.IOException", "查询回复保存失败")
		return nil, true
	}
	ctx.AfterResponse(func() { s.notifyMail(ctx.Sess.pushCtx(), ctx.Sess.Userid, id) })
	return []byte{0}, true
}
