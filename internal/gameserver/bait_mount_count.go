package gameserver

// 挂饵方式（17/6 rod/set_baitmount 选中的 BaitMountType）决定每份饵
// 拆上钩的数量。官方语义来自客户端反编译 kahmnmfmpgk.kpfhbdgmmif：
// 枚举按位命名，「玉米浮+蛆15」= 一根发丝上 15 份蛆 + 1 颗玉米浮；
// 「沉+沉」「颗+颗」是两个饵槽各一份（玩家分两次挂载），不是一份挂两个。
// 计件规则：带 ×N 后缀的成束挂法才多份，其余一钩一份。

import (
	"context"
	"encoding/json"
	"strings"

	"rf4server/internal/store"
)

// baitMountKind 是挂饵方式的组件族，与客户端 BaitHairMount 的 kind
// 分支对应。判别依据是 item_def 的 fopbhlmkcpe（饵型字段，服务端已在
// baitUseRule / catch_bonuses 使用）；泡沫（hm_foamr*）没有该字段。
type baitMountKind int

const (
	baitKindBoil baitMountKind = iota // 下沉煮饵/天然饵（fopbhlmkcpe=1）
	baitKindInsect                    // 蛆虫族（fopbhlmkcpe=2/7）
	baitKindPopup                     // 浮性 Pop-up 煮饵（fopbhlmkcpe=8）
	baitKindPellets                   // 颗粒（fopbhlmkcpe=9）
	baitKindCorn                      // 玉米浮（fopbhlmkcpe=10）
	baitKindFoam                      // 泡沫（hm_foamr*，无 fopbhlmkcpe）
	baitKindOther
)

// baitMountHookCounts 是成束挂法的 (挂饵方式位值 → 组件族 → 一钩份数)。
// 未列出的挂法（含未选择 f8=0）一律一份；组合挂法只写多份的那一族
// （玉米浮+蛆15 里玉米浮仍是一颗），单份族不进表。
var baitMountHookCounts = map[int64]map[baitMountKind]int64{
	1024:    {baitKindCorn: 2},   // 玉米浮×2
	2048:    {baitKindCorn: 4},   // 玉米浮×4
	8192:    {baitKindCorn: 2},   // Pop-up 煮饵+玉米浮×2
	65536:   {baitKindInsect: 5},  // 玉米浮+蛆5
	131072:  {baitKindInsect: 10}, // 玉米浮+蛆10
	262144:  {baitKindInsect: 15}, // 玉米浮+蛆15
	1048576: {baitKindFoam: 2},   // 泡沫×2
}

// baitMountKindOf 从饵实例的 item_def 判别组件族。
// fopbhlmkcpe 是各品类共用的子类型字段（竿型 rigmeta、钩型 gmhook 等都在用），
// 只在饵品类（27）里才是饵型；泡沫（hm_foamr*，品类 17 非计件）按前缀判别。
func (s *Server) baitMountKindOf(ctx context.Context, row *store.Item) baitMountKind {
	if row == nil {
		return baitKindOther
	}
	itemID := row.ItemID
	if def, err := s.itemDefForRow(ctx, row); err == nil && def != nil {
		if def.ItemID != "" {
			itemID = def.ItemID
		}
		if !strings.HasPrefix(itemID, "hm_foamr") && def.Category != 27 {
			return baitKindOther
		}
		var attrs struct {
			BaitType int64 `json:"fopbhlmkcpe"`
		}
		if json.Unmarshal([]byte(def.AttrsJSON), &attrs) == nil {
			switch attrs.BaitType {
			case 1:
				return baitKindBoil
			case 2, 7:
				return baitKindInsect
			case 8:
				return baitKindPopup
			case 9:
				return baitKindPellets
			case 10:
				return baitKindCorn
			}
		}
	}
	if strings.HasPrefix(itemID, "hm_foamr") {
		return baitKindFoam
	}
	return baitKindOther
}

// baitHookCount 返回该竿当前挂饵方式下这份饵应拆上钩的份数。
// 背包余量不足整束时由 splitMountableChild 按余量钳制。
func (s *Server) baitHookCount(ctx context.Context, rodGUID string, row *store.Item) int64 {
	n := int64(1)
	if s.DB != nil && row != nil && rodGUID != "" {
		if mountType, ok := s.savedBaitMount(ctx, rodGUID); ok && mountType != 0 {
			if perKind := baitMountHookCounts[mountType]; perKind != nil {
				if c := perKind[s.baitMountKindOf(ctx, row)]; c > n {
					n = c
				}
			}
		}
	}
	return n
}
