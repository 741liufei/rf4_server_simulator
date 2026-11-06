package store

import (
	"context"
	"database/sql"
)

// DeleteKeepnetTx 同时移除护网条目及同一玩家的导入快照。
func (d *DB) DeleteKeepnetTx(ctx context.Context, tx *sql.Tx, uid int64, guid string) (bool, error) {
	result, err := tx.ExecContext(ctx, `DELETE FROM keepnet_items WHERE uid=? AND echo_guid=?`, uid, guid)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM item WHERE uid=? AND guid=? AND container='keepnet' AND type_id=25`, uid, guid)
	return err == nil, err
}

// RemoveOwnedItems 让放生和普通物品删除在同一事务中保留玩家边界。
//
// RemoveOwnedItems releases fish and removes ordinary items in one transaction,
// preserving the player's ownership boundary.
func (d *DB) RemoveOwnedItems(ctx context.Context, uid int64, guids []string) (removed, fish []string, err error) {
	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	removed, fish, err = d.RemoveOwnedItemsTx(ctx, tx, uid, guids)
	if err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return removed, fish, nil
}

// RemoveOwnedItemsTx 在调用方事务中删除本人物品并返回实际删除和放生的 GUID。
// 重复或非本人 GUID 不产生副作用；调用方负责提交或回滚及提交后的在线状态同步。
//
// RemoveOwnedItemsTx removes owned items in the caller's transaction and returns
// the removed and released GUIDs. Duplicate or foreign GUIDs have no effect. The
// caller owns commit/rollback and synchronizes online state only after commit.
func (d *DB) RemoveOwnedItemsTx(ctx context.Context, tx *sql.Tx, uid int64, guids []string) (removed, fish []string, err error) {
	seen := make(map[string]bool, len(guids))
	for _, guid := range guids {
		if seen[guid] {
			continue
		}
		seen[guid] = true
		kept, err := d.DeleteKeepnetTx(ctx, tx, uid, guid)
		if err != nil {
			return nil, nil, err
		}
		if kept {
			removed = append(removed, guid)
			fish = append(fish, guid)
			continue
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM item WHERE uid=? AND guid=?`, uid, guid)
		if err != nil {
			return nil, nil, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return nil, nil, err
		}
		if count != 0 {
			removed = append(removed, guid)
		}
	}
	return removed, fish, nil
}
