package gameserver

// 钓鱼游装备包：进图发放、离图/下线/到期回收（生命周期见 syncTripEquipment）。
//
// 官方模型（用户 2026-11-05 口头修正）：钓鱼游临时装备只能在对应水域使用——
// 进入地图发放，离开地图（切图/下线）即回收，重新进图再发放。客户端对其他
// 地图的物品另行锁定，服务端生命周期是权威约束。
//
// 官方钓鱼游标志（2026-11-06 六轮实验+反编译定案）：物品 wire f12 dbgaheblmei
// （pfapfdlnfbj）打位 16|256：
//   - 16（fhblahbelfd）：解锁钥匙。lcdihnabclk.cbhjcnkojgc（竿/轮/misc 格子族
//     共同基类，继承链 gmkhlogpalc→hkfhagknkil→lcdihnabclk→hggmlkgbpme）在
//     f12==16 时把格子 state 归零，基类的等级锁位 1（playerLevel < def.req，
//     如 26 级 Azimut 对 25 级号）随之清除，随后 |=32 重建为可用。反编译字面
//     条件（!=16 归零）被控制流打平颠倒，以实测为准。
//   - 256（mhobmilijbm，"TemporaryTourItem"）：详情面板角标 + "该物品在钓鱼
//     游期间可以使用" tooltip（kTooltip_TemporaryTourItem，knmjphiefdm.
//     kkkohnkelfd 唯一消费点；f12==256 显示，极性亦经实测校正）。
//   - 16 另有旁证：官方抓包出现 113 次（唯一高频非零值），出售/上架过滤器
//     要求 16 清零——钓鱼游装备不可出售，语义吻合。
//   - 租用对象（f9 bkmnbppciao，134/135 两类 × type × 窗口 × 字符串四轮组合）
//     均使物品显示"锁定"，与官方机制无关，全部证伪弃用。
// 官方 18 级账号可借钓鱼游使用 26 级 Azimut 10000，即由 16 位解锁（用户实测
// 复现确认）。
//
// 发放用 CreateItem 独立成行（不走 AddToStack 合并），避免把玩家自有的
// 同类可堆叠物品并进回收范围；发放记录进 trip_grant（迁移 00039）供回收
// 与"重复进图不重复发放"判断。回收 = 删除物品 + 4/21 移除推送（与船票
// 到期清理 boat.go 同型）。

import (
	"context"
	"strings"
	"time"

	"rf4server/internal/store"
)

// tripEquipItem 装备包条目（Units 为发放单位数，可堆叠件按定义单位量换算）。
type tripEquipItem struct {
	ItemID string
	Units  int64
}

// tripEquipmentBundles 钓鱼游装备包（用户 2026-11-05 提供清单，item_id 经
// item_catalog 品牌字段逐一核对）。
var tripEquipmentBundles = map[string][]tripEquipItem{
	"level_018_moskitnoe": {
		{"feeder_10050_FD360M", 3}, // Syberia Model Two FD360M（brand=syb_modeltwofeeder）
		{"spin_5126", 3},           // Beluga Natora 6000（brand=beluga_natora）
		{"fluoro_15822", 3},        // Snake Power Carbon 61LB 300m（brand=snake_power_carbon_300）
		{"feeder_bell", 3},         // Express Fishing 双铃铛 - FA
		{"sqpsinker_30546", 3},     // Simmons C-Lead SW M-CB 70
		{"rod_pod", 1},             // 竿架（清单未指定型号，暂以 SFR-123 占位）
		{"papermap_copper", 1},     // 铜湖地图
	},
	"level_012_north_sea": {
		{"papermap_north_sea", 1},        // 挪威海地图
		{"pilkr_10208_os88", 1},          // WTA Saltmaster - Offshore 88（brand=wta_Saltmaster）
		{"spin_5319", 1},                 // SAT Azimut 10000（brand=sat_Azimut）
		{"braid_40888", 1},               // Solid Materials HiPower Braid 80LB MC 600m
		{"sh_leader_40815", 1},           // Solid Materials HiPower Shock Leader 80LB 2m
		{"pilkerlures_2513", 1},          // Jax Bond 125-01
		{"pilkerlures_2514", 1},          // Jax Bond 125-02
		{"pilkerlures_2515", 1},          // Jax Bond 125-03
		{"pilkerlures_2516", 1},          // Jax Bond 125-04
		{"pilkerlures_2517", 1},          // Jax Bond 125-05
		{"pilkerlures_2518", 1},          // Jax Bond 125-06
		{"pilkerlures_2519", 1},          // Jax Bond 125-07
		{"pilkerlures_2520", 1},          // Jax Bond 125-08
		{"pilkerlures_2521", 1},          // Jax Bond 125-09
		{"pilkerlures_2522", 1},          // Jax Bond 125-10
		{"pilkerlures_2523", 1},          // Jax Bond 125-11
		{"pilkerlures_2692", 1},          // Zeiman Dapper Pilk 150-01
		{"pilkerlures_2693", 1},          // Zeiman Dapper Pilk 150-02
		{"pilkerlures_2694", 1},          // Zeiman Dapper Pilk 150-03
		{"pilkerlures_2695", 1},          // Zeiman Dapper Pilk 150-04
		{"pilkerlures_2696", 1},          // Zeiman Dapper Pilk 150-05
		{"pilkerlures_2697", 1},          // Zeiman Dapper Pilk 150-06
		{"pilkerlures_2698", 1},          // Zeiman Dapper Pilk 150-07
		{"pilkerlures_2699", 1},          // Zeiman Dapper Pilk 150-08
		{"pilkerlures_2700", 1},          // Zeiman Dapper Pilk 150-09
		{"ticket_boat_norw_sea_a_24", 1}, // 五小时船票（目录无 5h 票种，暂以挪威海 A 船票代替）
	},
}

// grantTripEquipment 购票成功后发放装备包，返回发放件数。
//
// 已有未回收发放时跳过（同一张图重复进出不重复建行）。按件建行落库、
// 登记 trip_grant 并推 4/22 给在线会话。durSec 保留参数位（历史租用标记用）。
func (s *Server) grantTripEquipment(ctx context.Context, uid int64, place string, durSec int64) int {
	if s == nil || s.DB == nil {
		return 0
	}
	bundle := tripEquipmentBundles[place]
	if len(bundle) == 0 {
		return 0
	}
	has, err := s.DB.HasUnrecalledTripGrant(ctx, uid, place)
	if err != nil {
		s.log.Warn("钓鱼游装备包发放检查失败", "place", place, "err", err)
		return 0
	}
	if has {
		return 0
	}
	// f12 标志（2026-11-06 第五轮实测确认）：dbgaheblmei |= 256（pfapfdlnfbj
	// .mhobmilijbm，"TemporaryTourItem"）= 官方钓鱼游装备标志，客户端据此显示
	// 角标 + 详情"该物品在钓鱼游期间可以使用"tooltip（反编译唯一消费点
	// knmjphiefdm.kkkohnkelfd，实测极性 f12==256 显示）。等级锁不受 256 影响
	// （状态机无该位消费点），见下方等级锁注释。船票不打此标志（票有自己的
	// 租用语义，且官方样本未知）。
	// 等级锁（未解，客户端静态数据驱动）：竿/轮族状态位 1 = playerLevel <
	// def.dlmaaeecbci（如 Azimut 10000 req=26），decode=5 实证 state=1/req=26；
	// 官方是否存在 25 级可用 26 级轮子的豁免待官方对照确认。
	var guids, itemIDs []string
	// 按件发放：竿/轮/铃铛/地图等非堆叠类别忽略 units，必须逐件建行
	// （清单"各三个"= 3 个独立物品，各有独立 guid/耐久）。
	for _, e := range bundle {
		for i := int64(0); i < e.Units; i++ {
			row, err := s.CreateItem(ctx, uid, e.ItemID, "backpack", 1)
			if err != nil || row == nil {
				s.log.Warn("钓鱼游装备发放失败", "place", place, "item", e.ItemID, "err", err)
				continue
			}
			// 注：apaibhmcopp（铃铛类型枚举 {1 基础型, 2 BiteAlarmBell, 3 电子报警器}）
			// 是 item_def.attrs_json 的 fopbhlmkcpe 客户端静态配置（dadgbbiijki getter
			// 从配置对象链读取），不占 wire 实例字段；feeder_bell 目录值=1 即官方值，
			// 服务端无需改写实例字段。
			if !strings.HasPrefix(e.ItemID, "ticket_boat") {
				s.markTripTourFlag(ctx, row)
			}
			guids = append(guids, row.GUID)
			itemIDs = append(itemIDs, row.ItemID)
			s.pushItemUpdate(uid, row)
		}
	}
	if len(guids) > 0 {
		if gerr := s.DB.AddTripGrants(ctx, uid, place, guids, itemIDs); gerr != nil {
			// 登记失败必须回滚已发放物品：物品已在背包而 trip_grant 无记录，
			// 回收永远扫不到，等于永久白送（审查 P2）。发放与登记并非单事务，
			// 这里用补偿删除兜底；补偿失败时行虽残留但告警可见。
			s.log.Warn("钓鱼游装备发放登记失败，回滚已发放物品", "place", place, "err", gerr)
			for _, g := range guids {
				if derr := s.DB.DeleteItemCascade(ctx, g); derr != nil {
					s.log.Warn("钓鱼游装备回滚删除失败", "place", place, "guid", guidShort(g), "err", derr)
				}
			}
			for _, sess := range s.sessionsForUID(uid) {
				sess.Push4_21(guids)
			}
			return 0
		}
		s.log.Info("钓鱼游装备包发放", "place", place, "count", len(guids))
	}
	return len(guids)
}

// markTripTourFlag 置 wire f12 dbgaheblmei 的钓鱼游标志位：
//   - 256（mhobmilijbm，"TemporaryTourItem"）：角标 + 详情 tooltip
//     （第五轮实测确认显示）。
//   - 16（fhblahbelfd）：lcdihnabclk.cbhjcnkojgc 状态归零分支的钥匙——
//     f12==16 时格子 state 清零（等级锁位随之清除），随后 |=32 重建为可用。
//     反编译字面条件（!=16 归零）与观测矛盾（那样一切物品永不显示等级锁），
//     打平反编译丢失 else 结构，极性以实测为准；官方抓包 f12=16 出现 113 次
//     且多个过滤器要求 16 清零（钓鱼游装备不可出售，语义吻合）。
//
// 落库使 4/5 全量刷新与 4/22 推送一致；失败仅告警。
func (s *Server) markTripTourFlag(ctx context.Context, row *store.Item) {
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return
	}
	idx := fieldIdxByCS(row.WireClass, "dbgaheblmei")
	if idx < 0 || idx >= len(fields) {
		return
	}
	cur := uint64(0)
	if fields[idx].Num != nil {
		cur = *fields[idx].Num
	}
	fields[idx].Num = ptrU64(cur | 16 | 256)
	fj, err := FieldsToJSON(fields)
	if err != nil {
		return
	}
	if uerr := s.DB.UpdateItemFields(ctx, row.GUID, fj, row.Qty,
		row.Durability, row.Workmanship, row.RigGUID); uerr != nil {
		s.log.Warn("钓鱼游装备 f12 标志落库失败", "guid", guidShort(row.GUID), "err", uerr)
		return
	}
	row.FieldsJSON = fj
}

// recallTripEquipment 到期回收：删除该地点未回收的发放物品并推 4/21+2/1。
// 已装上钓组的组件先从钓组摘除（同管理端 delete_item 语义）再级联删除；
// 库里已不存在的发放行直接标记回收。返回实际移除件数。
func (s *Server) recallTripEquipment(ctx context.Context, uid int64, place string) int {
	if s == nil || s.DB == nil {
		return 0
	}
	grants, err := s.DB.UnrecalledTripGrants(ctx, uid, place)
	if err != nil || len(grants) == 0 {
		return 0
	}
	removed := make([]string, 0, len(grants))
	for _, g := range grants {
		row, err := s.DB.GetItemByGUID(ctx, g.GUID)
		if err != nil || row == nil || row.UID != uid {
			removed = append(removed, g.GUID) // 物品已不存在（消耗/手动删除）
			continue
		}
		if row.RigGUID != "" || row.Container == "rig" {
			s.rigRemoveItemComponent(ctx, uid, row.RigGUID, row.GUID)
			if row, err = s.DB.GetItemByGUID(ctx, row.GUID); err != nil || row == nil {
				removed = append(removed, g.GUID)
				continue
			}
		}
		if err := s.DB.DeleteItemCascade(ctx, row.GUID); err != nil {
			s.log.Warn("钓鱼游装备回收删除失败", "place", place, "guid", guidShort(row.GUID), "err", err)
			continue
		}
		removed = append(removed, row.GUID)
	}
	if len(removed) == 0 {
		return 0
	}
	if merr := s.DB.MarkTripGrantsRecalled(ctx, uid, place, removed); merr != nil {
		s.log.Warn("钓鱼游装备回收标记失败", "place", place, "err", merr)
	}
	s.clearFishingStateRefs(ctx, uid, removed)
	for _, sess := range s.sessionsForUID(uid) {
		sess.Push4_21(removed)
		sess.Push2_1()
	}
	s.log.Info("钓鱼游装备包回收", "place", place, "count", len(removed))
	return len(removed)
}

// clearFishingStateRefs 回收后清理钓位对已删除装备的悬空引用。钓鱼游竿/饵
// （如北海包的 pilkr 海竿）被抛竿后记入 fishing_state.gear_guid/bait，回收
// 删除物品后引用悬空：下次登录 effectiveFishingSkills → GetItemByGUID 命中
// sql.ErrNoRows → 5/1 技能树处理器静默失败回兜底空包 → 客户端卡"载入个人
// 资料"（2026-11-06 实测事故）。gear/bait 任一被回收即整段收竿复位（与收线
// 同型：撤 timer、ticket 自增作废旧回调），DB 与内存 FishState 同步清理
// （到期回收可发生在在线抛竿中）。
func (s *Server) clearFishingStateRefs(ctx context.Context, uid int64, removed []string) {
	if len(removed) == 0 {
		return
	}
	// 内存态：在线回收（到期/切图）时立即撤抛竿。
	s.fishMu.Lock()
	st := s.fishStates[uid]
	s.fishMu.Unlock()
	if st != nil {
		st.mu.Lock()
		gearGone, baitGone := false, false
		for _, g := range removed {
			if g != "" && g == st.GearGuid {
				gearGone = true
			}
			if g != "" && g == st.Bait {
				baitGone = true
			}
		}
		if gearGone || baitGone {
			st.cancelBiteTimerLocked()
			st.cancelPendingTimerLocked()
			st.GearGuid = ""
			st.Bait = ""
			st.CastActive = false
			st.InWater = false
			st.NeedBite = false
			st.Hooked = false
			st.PushSent = false
			st.Pending = nil
			st.BiteAt = time.Time{}
		}
		st.mu.Unlock()
	}
	// DB 态：下次登录的钓位恢复不再引用已删除物品。
	saved, err := s.DB.LoadFishingState(ctx, uid)
	if err != nil || saved == nil {
		return
	}
	gearGone, baitGone := false, false
	for _, g := range removed {
		if g != "" && g == saved.GearGUID {
			gearGone = true
		}
		if g != "" && g == saved.Bait {
			baitGone = true
		}
	}
	if !gearGone && !baitGone {
		return
	}
	if _, err := s.DB.SQL.ExecContext(ctx, `UPDATE fishing_state SET
		gear_guid=CASE WHEN ? THEN '' ELSE gear_guid END,
		bait=CASE WHEN ? THEN '' ELSE bait END,
		cast_active=0, hooked=0, resolved=0, pending_json=NULL, armed_json=NULL,
		cast_ts_ns=0, fall_ts_ns=0, fall_seen=0, bite_at_ns=0, updated_ts=0
		WHERE uid=?`, gearGone, baitGone, uid); err != nil {
		s.log.Warn("钓鱼游回收清理钓位引用失败", "uid", uid, "err", err)
		return
	}
	s.log.Info("钓鱼游回收清理钓位悬空引用", "uid", uid, "gear_gone", gearGone, "bait_gone", baitGone)
}

// activeTripRemainingSec 当前地点有效钓鱼游的剩余秒数（<=0 或无票返回 false）。
func (s *Server) activeTripRemainingSec(ctx context.Context, uid int64, place string) (int64, bool) {
	if s == nil || s.DB == nil {
		return 0, false
	}
	if _, ok := placeTripPrices[place]; !ok {
		return 0, false
	}
	var remaining float64
	if err := s.DB.SQL.QueryRowContext(ctx,
		`SELECT remaining_sec FROM trip_state WHERE uid=? AND place=?`,
		uid, place).Scan(&remaining); err != nil || remaining <= 0 {
		return 0, false
	}
	return int64(remaining), true
}

// syncTripEquipment 装备包与所在地图同步（官方生命周期，用户 2026-11-05 口头
// 修正）：进入有效钓鱼游地图（登录落点/切图）发放，离开（切图/下线）回收——
// 装备只能在该钓鱼游水域使用，客户端对图外租用物品直接渲染锁定。
// current=落点地图；发放时长取该地图钓鱼游当前剩余。
func (s *Server) syncTripEquipment(ctx context.Context, uid int64, current string) {
	if s == nil || s.DB == nil {
		return
	}
	if durSec, ok := s.activeTripRemainingSec(ctx, uid, current); ok {
		s.grantTripEquipment(ctx, uid, current, durSec)
	}
	places, err := s.DB.UnrecalledTripGrantPlaces(ctx, uid)
	if err != nil {
		s.log.Warn("钓鱼游装备同步读发放地点失败", "err", err)
		return
	}
	for _, p := range places {
		if p != current {
			s.recallTripEquipment(ctx, uid, p)
		}
	}
}

// recallAllTripEquipment 下线回收全部未回收发放（离线即离开所有地图）。
func (s *Server) recallAllTripEquipment(ctx context.Context, uid int64) {
	if s == nil || s.DB == nil {
		return
	}
	places, err := s.DB.UnrecalledTripGrantPlaces(ctx, uid)
	if err != nil {
		return
	}
	for _, p := range places {
		s.recallTripEquipment(ctx, uid, p)
	}
}
