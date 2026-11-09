package gameserver

import (
	"context"
	"fmt"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// RepairReelEnginePower 补齐电轮电机对象的功率与最高速度。
//
// 电机 wire 类 kbpmmclfipg/1470 的 f0/f1 字段名（npnlhobgfen/mbbdpnidheg）
// 与定义里的 afmboidbbaj/fgkfkgimfgo 不同，按名字回填会漏掉，早期物化出来
// 的电轮这两个值是 0，客户端据此算不出收线速度。
func (s *Server) RepairReelEnginePower(ctx context.Context) (int, error) {
	if s.DB == nil {
		return 0, nil
	}
	rows, err := s.DB.SQL.QueryContext(ctx, `SELECT guid FROM item WHERE wire_class=?`, "dgjinoebdop")
	if err != nil {
		return 0, err
	}
	var guids []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			_ = rows.Close()
			return 0, err
		}
		guids = append(guids, g)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	fixed := 0
	for _, g := range guids {
		it, gerr := s.DB.GetItemByGUID(ctx, g)
		if gerr != nil || it == nil {
			continue
		}
		fields, ferr := FieldsFromJSON(it.FieldsJSON)
		if ferr != nil {
			continue
		}
		idx := fieldIdxByCS(it.WireClass, "dfjpnambjcj")
		if idx < 0 || idx >= len(fields) || fields[idx].Val == nil || fields[idx].Val.Kind != proto.KindObj {
			continue
		}
		motor := fields[idx].Val
		if motor.Class != "kbpmmclfipg" {
			continue
		}
		var nid uint64
		if ni := fieldIdxByCS(motor.Class, csNumericID); ni >= 0 && ni < motor.FieldCount() {
			nid = u64Of(motor.Fields[ni].Num)
		}
		if nid == 0 {
			continue
		}
		def, derr := s.DB.GetItemDef(ctx, int64(nid))
		if derr != nil || def == nil {
			continue
		}
		attrs := defAttrs(def)
		if attrs == nil {
			continue
		}
		changed := false
		setBits := func(cs, attr string) {
			fi := fieldIdxByCS(motor.Class, cs)
			if fi < 0 || fi >= motor.FieldCount() {
				return
			}
			raw, ok := attrs[attr].(float64)
			if !ok || raw == 0 {
				return
			}
			want := uint64(raw)
			if u64Of(motor.Fields[fi].Num) == want {
				return
			}
			motor.Fields[fi].Num = ptrU64(want)
			changed = true
		}
		setBits("npnlhobgfen", "afmboidbbaj")
		setBits("mbbdpnidheg", "fgkfkgimfgo")
		if !changed {
			continue
		}
		fj, jerr := FieldsToJSON(fields)
		if jerr != nil {
			continue
		}
		if err := s.DB.UpdateItemFields(ctx, it.GUID, fj, it.Qty, it.Durability, it.Workmanship, it.RigGUID); err != nil {
			continue
		}
		fixed++
	}
	return fixed, nil
}

// RepairReelComponents 修复渔线轮零件槽，但保留工坊已选择的同品类改装件。
//
// 历史数据里的子零件可能是旧型号/别的轮子留下的（定义与实际零件对不上），
// 而 materializeInto 只补空槽不会覆盖，导致客户端按零件定义算兼容型号时全部算错。
// 这里以 item_def.attrs_json 为准，逐槽用 buildPartInstance 重建并回写。
func (s *Server) RepairReelComponents(ctx context.Context) (int, error) {
	if s.DB == nil {
		return 0, fmt.Errorf("数据库未连接")
	}
	rows, err := s.DB.SQL.QueryContext(ctx,
		`SELECT guid FROM item WHERE wire_class=?`, "dgjinoebdop")
	if err != nil {
		return 0, err
	}
	var guids []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			rows.Close()
			return 0, err
		}
		guids = append(guids, g)
	}
	rows.Close()

	fixed := 0
	for _, g := range guids {
		it, gerr := s.DB.GetItemByGUID(ctx, g)
		if gerr != nil || it == nil {
			continue
		}
		def, derr := s.DB.GetItemDef(ctx, it.NumericID)
		if derr != nil || def == nil {
			continue
		}
		attrs := defAttrs(def)
		fallbackParts := reelPartsNeedFallback(def)
		if fallbackParts {
			// 旧轮型没有嵌套六件时，借用同子类/同系列完整轮型的零件模板。
			// 只补空槽，已有的合法改装件仍由下面的 selectedReelPart 分支保留。
			attrs = s.reelMaterializationAttrs(ctx, def)
		}
		if attrs == nil {
			continue
		}
		fields, ferr := FieldsFromJSON(it.FieldsJSON)
		if ferr != nil {
			continue
		}
		changed := false
		for key, raw := range attrs {
			cfg, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			subID, _ := cfg["nhjofmmmngl"].(string)
			if subID == "" {
				continue
			}
			idx := fieldIdxByCS(it.WireClass, key)
			if idx < 0 || idx >= len(fields) || fields[idx].Op != "VAL" {
				continue
			}
			expectedCat := int64(0)
			switch n := cfg["mgkfaekpnoj"].(type) {
			case float64:
				expectedCat = int64(n)
			case int64:
				expectedCat = n
			}
			// 普通完整轮型只重建已有零件槽；缺槽新增一旦用错 wire 类，客户端解码
			// 会抛 InvalidCastException。占位轮型没有核心六件，已由上面的完整
			// 轮型模板确认 wire 类，允许补建核心零件槽；电机槽也继续允许补建。
			if fields[idx].Val == nil || fields[idx].Val.Kind != proto.KindObj {
				if expectedCat != 81 && !fallbackParts {
					continue
				}
			}
			old := fields[idx].Val
			// 同品类即视为合法工坊改装件，绝不能按母轮出厂配置覆盖。
			// 同时补齐历史版本漏写的摩擦力、挂载位。
			if selected := s.reelPartDef(ctx, old); selected != nil && selected.Category == expectedCat {
				if s.normalizeReelPart(it, old, selected) {
					changed = true
				}
				continue
			}
			child, cerr := s.buildPartInstance(ctx, it, subID, key)
			if cerr != nil || child == nil {
				continue
			}
			// 保留原零件的 guid（若已有），避免客户端物品集合里出现重复实例。
			if old := fields[idx].Val; old != nil && old.Kind == proto.KindObj {
				if gidx := fieldIdxByCS(old.Class, csGUID); gidx >= 0 && gidx < old.FieldCount() {
					if oldGUID := old.Fields[gidx].Hex; oldGUID != "" {
						if nidx := fieldIdxByCS(child.Class, csGUID); nidx >= 0 && nidx < child.FieldCount() {
							child.Fields[nidx].Hex = oldGUID
						}
					}
				}
				// 保留零件原有磨损，否则每次启动都会把耐久重置为 1.0。
				if didx := fieldIdxByCS(old.Class, csDurability); didx >= 0 && didx < old.FieldCount() && old.Fields[didx].Num != nil {
					if cidx := fieldIdxByCS(child.Class, csDurability); cidx >= 0 && cidx < child.FieldCount() {
						child.Fields[cidx].Num = old.Fields[didx].Num
					}
				}
				// 同一个零件（沿用原 GUID）重建时，摩擦片制动力也要带过去；
				// 0 表示已磨穿，同样不能丢。
				if oldDrag, present, _ := frictionWasherDrag(old); present {
					if cidx := fieldIdxByCS(child.Class, csDragForce); cidx >= 0 && cidx < child.FieldCount() {
						setWireFloat(child.Fields, child.Class, cidx, oldDrag)
					}
				}
			}
			fields[idx].Val = child
			changed = true
		}
		// Normalize against the selected parts, including any rebuilt slots.
		snapshotJSON, snapshotErr := FieldsToJSON(fields)
		if snapshotErr != nil {
			continue
		}
		snapshot := *it
		snapshot.FieldsJSON = snapshotJSON
		if s.canonicalizeReelStrength(ctx, &snapshot, fields) {
			changed = true
		}
		if !changed {
			continue
		}
		fj, jerr := FieldsToJSON(fields)
		if jerr != nil {
			continue
		}
		if err := s.DB.UpdateItemFields(ctx, it.GUID, fj, it.Qty, it.Durability, it.Workmanship, it.RigGUID); err != nil {
			continue
		}
		fixed++
	}
	return fixed, nil
}

// normalizeReelPart 回填改装零件会影响的运行时物理字段。
func (s *Server) normalizeReelPart(host *store.Item, child *proto.Value, def *store.ItemDef) bool {
	if host == nil || child == nil || def == nil {
		return false
	}
	changed := false
	if idx := fieldIdxByCS(child.Class, csStateEnum); idx >= 0 && idx < child.FieldCount() {
		old := u64Of(child.Fields[idx].Num)
		want := old | subMountFlag
		if want != old {
			child.Fields[idx].Num = ptrU64(want)
			changed = true
		}
	}
	if def.Category == 9 {
		// 制动力是磨损状态，不是可从定义重算的派生值：只在字段缺失时补齐，
		// 字段存在（含 0=已磨穿）一律保留，否则启动修复会把磨损刷回满值。
		if _, present, idx := frictionWasherDrag(child); !present && idx >= 0 && idx < child.FieldCount() {
			if setWireFloat(child.Fields, child.Class, idx, itemDefF32(def, gearAttrDragMax)*host.Workmanship) {
				changed = true
			}
		}
	}
	return changed
}
