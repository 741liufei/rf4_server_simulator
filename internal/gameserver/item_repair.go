package gameserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"rf4server/internal/proto"
)

// itemPhysicsRepairKey 记录最近一次物理字段修复的版本与统计。
// 每次启动仍会重新扫描，以修复后续导入或回档产生的异常数据。
const itemPhysicsRepairKey = "repair:item_physics:v5"
const itemPhysicsRepairVersion = "11"

const craftedLeaderQuarantineContainer = "craft_quarantine"

// reelFrictionZeroMigrationKey 标记"存量零值制动力"迁移只执行一次。
const reelFrictionZeroMigrationKey = "migration:reel_friction_zero:v1"

// migrateLegacyZeroDragForce 处理升级前的存量零值摩擦片。
//
// 旧实现把 csDragForce=0 当成"未初始化"，每次打滑都从零件定义重算，因此
// 升级前存档里留下来的 0 并不代表磨穿；若不处理，新语义会让这些轮子一上线
// 就泄力失效。这里统一补回额定值。迁移完成后 0 是权威的"已磨穿"状态，
// 必须靠 KV 标记保证不再重复执行，否则磨穿的轮子会被复活。
func (s *Server) migrateLegacyZeroDragForce(ctx context.Context) (int, error) {
	_, done, err := s.DB.KVGet(ctx, reelFrictionZeroMigrationKey)
	if err == nil && done {
		return 0, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("读取摩擦片迁移标记: %w", err)
	}
	rows, err := s.DB.SQL.QueryContext(ctx, `SELECT guid FROM item WHERE wire_class=?`, "dgjinoebdop")
	if err != nil {
		return 0, err
	}
	var guids []string
	for rows.Next() {
		var guid string
		if err := rows.Scan(&guid); err != nil {
			_ = rows.Close()
			return 0, err
		}
		guids = append(guids, guid)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	fixed := 0
	for _, guid := range guids {
		row, rerr := s.DB.GetItemByGUID(ctx, guid)
		if rerr != nil || row == nil {
			continue
		}
		fields, ferr := FieldsFromJSON(row.FieldsJSON)
		if ferr != nil {
			continue
		}
		child, def, _, ok := s.selectedReelPart(ctx, fields, 9)
		if !ok || child == nil || def == nil {
			continue
		}
		cur, present, idx := frictionWasherDrag(child)
		if !present || cur != 0 {
			continue
		}
		want := itemDefF32(def, gearAttrDragMax) * row.Workmanship
		if want <= 0 || idx < 0 || idx >= len(child.Fields) {
			continue
		}
		if !setWireFloat(child.Fields, child.Class, idx, want) {
			continue
		}
		bumpCreatedTicks(fields, row.WireClass)
		fj, jerr := FieldsToJSON(fields)
		if jerr != nil {
			continue
		}
		if uerr := s.DB.UpdateItemFields(ctx, row.GUID, fj, row.Qty, row.Durability, row.Workmanship, row.RigGUID); uerr != nil {
			return fixed, uerr
		}
		row.FieldsJSON = fj
		if _, perr := s.syncReelPerformance(ctx, row); perr != nil {
			return fixed, perr
		}
		fixed++
	}
	if err := s.DB.KVSet(ctx, reelFrictionZeroMigrationKey, "done"); err != nil {
		return fixed, fmt.Errorf("写入摩擦片迁移标记: %w", err)
	}
	return fixed, nil
}

// RepairItemPhysics 修复已落库物品的物理字段快照：
//   - 把误落成钓线类的工具、材料和铃铛改写成自己的实例类；
//   - 重新物化缺失的渔轮子件；
//   - 补全线、钩、竿、轮的顶层强度；
//   - 回填零值状态位和容器枚举；
//   - 修正渔轮刹车片成色，只补齐缺失的曳力字段（已有磨损值不再覆盖）；
//   - 将竿的 f5 类型限制在竿型允许范围内；
//   - 修复旧自制引线的内嵌线材槽位，并把旧减震引线迁到 f3。
//
// 兼容性只用于装配推荐，不覆盖玩家显式选择。修复写回 fields_json，
// 并通过 ensureRig 同步 rig/rig_component。成功后写版本和统计标记；
// 失败时保留上一次成功标记。
func (s *Server) RepairItemPhysics(ctx context.Context) error {
	if s.DB == nil {
		return fmt.Errorf("物品物理状态修复：数据库未连接")
	}
	// 必须先于常规扫描：常规扫描只会补齐缺失字段，不会改写已存在的 0。
	if _, err := s.migrateLegacyZeroDragForce(ctx); err != nil {
		return fmt.Errorf("物品物理状态修复：摩擦片存量零值迁移: %w", err)
	}

	rows, err := s.DB.SQL.QueryContext(ctx, `SELECT guid FROM item ORDER BY guid`)
	if err != nil {
		return fmt.Errorf("物品物理状态修复：列出物品: %w", err)
	}
	var guids []string
	for rows.Next() {
		var guid string
		if err := rows.Scan(&guid); err != nil {
			_ = rows.Close()
			return fmt.Errorf("物品物理状态修复：读取 guid: %w", err)
		}
		guids = append(guids, guid)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("物品物理状态修复：关闭查询: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("物品物理状态修复：遍历物品: %w", err)
	}

	changed := 0
	rigsRepaired := 0
	quarantined := 0
	restored := 0
	for _, guid := range guids {
		it, err := s.DB.GetItemByGUID(ctx, guid)
		if err != nil {
			return fmt.Errorf("物品物理状态修复：读取 %s: %w", guid, err)
		}
		if it == nil {
			continue
		}
		if def, derr := s.DB.GetItemDef(ctx, it.NumericID); derr == nil && def != nil {
			rewritten, rerr := s.reclassifyStoredItem(ctx, it, def)
			if rerr != nil {
				return fmt.Errorf("物品物理状态修复：改写类别 %s: %w", guid, rerr)
			}
			if rewritten {
				changed++
				continue
			}
		}
		fields, err := FieldsFromJSON(it.FieldsJSON)
		if err != nil {
			return fmt.Errorf("物品物理状态修复：解析 %s: %w", guid, err)
		}
		fields = alignFieldsToOps(it, fields)
		leaderChanged, invalidLeader, lerr := s.repairCraftedLeaderWire(ctx, it, fields)
		if lerr != nil {
			return fmt.Errorf("物品物理状态修复：修复引线 %s: %w", guid, lerr)
		}
		if invalidLeader {
			if err := s.DB.UpdateItemContainer(ctx, it.GUID, craftedLeaderQuarantineContainer); err != nil {
				return fmt.Errorf("物品物理状态修复：隔离无效引线 %s: %w", guid, err)
			}
			quarantined++
			s.log.Warn("无效自制引线已隔离", "guid", guid, "item", it.ItemID)
			continue
		}
		if it.Container == craftedLeaderQuarantineContainer {
			// 旧版本把"找不到参数完全一致的前导线定义"的引线关进隔离区，
			// 玩家从此看不见自己做出来的引线。映射放宽后必须把物品放回原容器。
			restore := "backpack"
			if it.RigGUID != "" {
				restore = "rig"
			}
			if err := s.DB.UpdateItemContainer(ctx, it.GUID, restore); err != nil {
				return fmt.Errorf("物品物理状态修复：解除引线隔离 %s: %w", guid, err)
			}
			if restore == "backpack" {
				if slot, serr := s.DB.NextFreeSlot(ctx, it.UID, "backpack"); serr == nil && slot >= 0 {
					if uerr := s.DB.SetItemSlot(ctx, it.GUID, slot); uerr != nil {
						return fmt.Errorf("物品物理状态修复：分配引线背包槽位 %s: %w", guid, uerr)
					}
				}
			}
			restored++
			it.Container = restore
			s.log.Info("自制引线已解除隔离", "guid", guid, "item", it.ItemID, "container", restore)
		}
		before, _ := FieldsToJSON(fields)
		obj := proto.Obj(it.WireClass, uint32(it.TypeID), fields...)
		rowChanged := leaderChanged
		var rodCreated proto.Field
		hasRodCreated := false
		// 与 ItemToTree 的字段修复保持一致：状态零值和缺失子件必须写回数据库。
		if def, derr := s.DB.GetItemDef(ctx, it.NumericID); derr == nil && def != nil {
			stateFlagsZeroFill(it.WireClass, obj, def)
			s.materializeInto(ctx, it.WireClass, def, obj, it.GUID, it.Workmanship, it.Durability)
		}
		// 竿实例的厂件和 f5 也必须持久化，不能只在渲染时重建。
		if it.WireClass == "cnolibojkao" {
			if idx := fieldIdxByCS(it.WireClass, csCreated); idx >= 0 && idx < len(fields) {
				rodCreated = fields[idx]
				hasRodCreated = true
			}
			if err := s.rebuildRod(ctx, it, obj); err != nil {
				return fmt.Errorf("物品物理状态修复：重建鱼竿 %s: %w", guid, err)
			}
			fields = obj.Fields
			// f16 是每次下发都会刷新的动态时间戳，不参与持久化变化判断；
			// f0、f5 等字段仍通过后续 JSON 比较检测。
			if hasRodCreated {
				if idx := fieldIdxByCS(it.WireClass, csCreated); idx >= 0 && idx < len(fields) {
					fields[idx] = rodCreated
				}
			}
			// 挂饵方式可选项缺失时按钓组类型补掩码：f9=0 会让「选择安装方式」
			// 弹窗全空、鱼饵被判定为不适配（2026-11-04 实测）。
			if idx := fieldIdxByCS(it.WireClass, csRigObject); idx >= 0 && idx < len(fields) && fields[idx].Val != nil {
				if _, avail := rigBaitMountsOf(fields[idx].Val); avail == 0 {
					setRigBaitMountAvail(fields[idx].Val, baitMountAvailForRig(s.rigTypeOfItem(ctx, it), 0))
				}
			}
		}
		// 品类 46（成品窝料）存量补单件克重：老实例是照抄定义生成的，
		// 重量恒为 0，卡片永远显示「质量 0 克」。
		if def46, derr := s.DB.GetItemDef(ctx, it.NumericID); derr == nil && def46 != nil && def46.Category == 46 {
			if fillFeedUnitWeight(it.WireClass, fields, def46.ItemID) {
				rowChanged = true
			}
		}
		if s.canonicalizeItemPhysicalFields(ctx, it, fields) {
			rowChanged = true
		}
		if after, aerr := FieldsToJSON(fields); aerr == nil && after != before {
			rowChanged = true
		}
		// 计件物品以 fields_json 的 ljnnpokapig 为准，修正 item.qty。
		if def, derr := s.DB.GetItemDef(ctx, it.NumericID); derr == nil && def != nil && countableCategories[def.Category] {
			if qi := fieldIdxByCS(it.WireClass, csQty); qi >= 0 && qi < len(fields) {
				if q := int64(u64Of(fields[qi].Num)); q > 0 && q != it.Qty {
					it.Qty = q
					rowChanged = true
				}
			}
		}
		if rowChanged {
			bumpCreatedTicks(fields, it.WireClass)
			fj, err := FieldsToJSON(fields)
			if err != nil {
				return fmt.Errorf("物品物理状态修复：编码 %s: %w", guid, err)
			}
			if err := s.DB.UpdateItemFields(ctx, it.GUID, fj, it.Qty, it.Durability, it.Workmanship, it.RigGUID); err != nil {
				return fmt.Errorf("物品物理状态修复：写回 %s: %w", guid, err)
			}
			changed++
		}
		if it.WireClass == "cnolibojkao" {
			if s.ensureRig(ctx, it.UID, it.GUID) {
				rigsRepaired++
			}
		}
	}

	marker := fmt.Sprintf("done;version=%s;scanned=%d;changed=%d;rods=%d;quarantined=%d;leaders_restored=%d",
		itemPhysicsRepairVersion, len(guids), changed, rigsRepaired, quarantined, restored)
	if err := s.DB.KVSet(ctx, itemPhysicsRepairKey, marker); err != nil {
		return fmt.Errorf("物品物理状态修复：写完成标记: %w", err)
	}
	s.log.Info("物品物理状态修复完成", "version", itemPhysicsRepairVersion,
		"items_changed", changed, "rods_repaired", rigsRepaired,
		"quarantined", quarantined, "leaders_restored", restored, "total", len(guids))
	return nil
}
