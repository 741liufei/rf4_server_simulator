-- 收藏卡与卡册属于专用库存，保留实例和原始钓获资料。
DELETE FROM container_slot
WHERE EXISTS (
    SELECT 1 FROM item
    WHERE item.guid=container_slot.guid AND item.uid=container_slot.uid
      AND item.container='backpack'
      AND item.wire_class IN ('dapkdcghfmg','ckfmndabaei')
);
UPDATE item SET container='native:18', slot=0
WHERE container='backpack' AND wire_class IN ('dapkdcghfmg','ckfmndabaei');
