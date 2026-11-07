package gameserver

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"rf4server/internal/store"
)

// removeItemsAndRepairRigs 原子删除本人物品，并将失去父物品的配件退回背包。
// 空列表用于登录时修复已有孤立配件；不补发物品，也不修改其他玩家的数据。
//
// removeItemsAndRepairRigs atomically deletes owned items and returns orphaned
// attachments to the backpack. An empty list repairs existing login data without
// granting replacement items or changing another player's inventory.
func (s *Server) removeItemsAndRepairRigs(ctx context.Context, uid int64, guids []string) (removed, fish []string, err error) {
	s.fishMu.Lock()
	st := s.fishStates[uid]
	s.fishMu.Unlock()
	if st != nil {
		st.mu.Lock()
		defer st.mu.Unlock()
	}
	tx, err := beginEconomyTx(ctx, s.DB)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	var removedRods []string
	for _, guid := range guids {
		row, err := getOwnedItemTx(ctx, tx, uid, guid)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if row.WireClass == "cnolibojkao" {
			removedRods = append(removedRods, row.GUID)
		}
	}
	removed, fish, err = s.DB.RemoveOwnedItemsTx(ctx, tx, uid, guids)
	if err != nil {
		return nil, nil, err
	}
	for _, guid := range removed {
		if _, err := tx.ExecContext(ctx, `DELETE FROM rig_component WHERE guid=? AND EXISTS
			(SELECT 1 FROM rig WHERE rig.guid=rig_component.rig_guid AND rig.uid=?)`, guid, uid); err != nil {
			return nil, nil, err
		}
	}
	for _, guid := range removedRods {
		if err := deleteMissingRodIndexTx(ctx, tx, uid, guid); err != nil {
			return nil, nil, err
		}
	}

	rows, err := tx.QueryContext(ctx, `SELECT `+txItemCols+` FROM item
		WHERE uid=? AND COALESCE(rig_guid,'')<>'' AND NOT EXISTS
		(SELECT 1 FROM item parent WHERE parent.guid=item.rig_guid AND parent.uid=item.uid)`, uid)
	if err != nil {
		return nil, nil, err
	}
	var orphans []*store.Item
	for rows.Next() {
		row, err := scanTxItem(rows)
		if err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		orphans = append(orphans, row)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, nil, err
	}
	for _, row := range orphans {
		fields, err := FieldsFromJSON(row.FieldsJSON)
		if err != nil {
			return nil, nil, err
		}
		clearRigFieldsForBackpack(fields, row.WireClass)
		row.RigGUID = ""
		row.Container = tradingItemContainer(row.WireClass, "backpack")
		if err := updateItemFieldsTx(ctx, s.DB, tx, row, fields); err != nil {
			return nil, nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE item SET container=?, slot=0 WHERE guid=? AND uid=?`, row.Container, row.GUID, uid); err != nil {
			return nil, nil, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM rig_component WHERE guid=? AND EXISTS
			(SELECT 1 FROM rig WHERE rig.guid=rig_component.rig_guid AND rig.uid=?)`, row.GUID, uid); err != nil {
			return nil, nil, err
		}
	}

	rows, err = tx.QueryContext(ctx, `SELECT guid FROM rig WHERE uid=?
		AND NOT EXISTS (SELECT 1 FROM item WHERE item.guid=rig.guid AND item.uid=rig.uid)`, uid)
	if err != nil {
		return nil, nil, err
	}
	var missingRods []string
	for rows.Next() {
		var guid string
		if err := rows.Scan(&guid); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		missingRods = append(missingRods, guid)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, nil, err
	}
	for _, guid := range missingRods {
		if err := deleteMissingRodIndexTx(ctx, tx, uid, guid); err != nil {
			return nil, nil, err
		}
	}

	clearHeld := false
	if st != nil && st.GearGuid != "" {
		_, err := getOwnedItemTx(ctx, tx, uid, st.GearGuid)
		clearHeld = errors.Is(err, sql.ErrNoRows)
		if err != nil && !clearHeld {
			return nil, nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE fishing_state SET
		gear_guid='', rig_type=0, depth_cm=0, cast_active=0, cast_ts_ns=0, fall_ts_ns=0,
		fall_seen=0, rig_settled=0, tick_seen=0, tick_seq=0, last_anim=0,
		push_sent=0, hooked=0, resolved=1, pending_json=NULL, bite_at_ns=0
		WHERE uid=? AND gear_guid<>'' AND NOT EXISTS
		(SELECT 1 FROM item WHERE item.guid=fishing_state.gear_guid AND item.uid=fishing_state.uid)`, uid); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	if clearHeld {
		st.cancelBiteTimerLocked()
		st.cancelPendingTimerLocked()
		st.castGeneration++
		st.pushing = nil
		st.persist.recoverPending, st.persist.recoverCompleted = false, false
		st.GearGuid, st.RigType, st.DepthCM = "", 0, 0
		st.CastActive, st.InWater, st.NeedBite = false, false, false
		st.CastTs, st.FallTs, st.BiteAt = time.Time{}, time.Time{}, time.Time{}
		st.FallSeen, st.RigSettled, st.TickSeen = false, false, false
		st.TickSeq, st.LastAnim = 0, 0
		st.PushSent, st.Hooked, st.Resolved, st.Pending = false, false, true, nil
	}
	return removed, fish, nil
}

func deleteMissingRodIndexTx(ctx context.Context, tx *sql.Tx, uid int64, guid string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM rig_component WHERE rig_guid=? AND EXISTS
		(SELECT 1 FROM rig WHERE rig.guid=rig_component.rig_guid AND rig.uid=?)`, guid, uid); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM rod_setting WHERE guid=? AND NOT EXISTS
		(SELECT 1 FROM rig WHERE rig.guid=rod_setting.guid AND rig.uid<>?)`, guid, uid); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM rig WHERE guid=? AND uid=?`, guid, uid)
	return err
}
