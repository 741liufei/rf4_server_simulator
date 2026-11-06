package gameserver

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strings"

	"rf4server/internal/store"
)

const maxBundlePreparedItems = 10000

func bundleAttrs(def *store.ItemDef) (map[string]any, error) {
	if def.AttrsJSON == "" {
		return nil, nil
	}
	if !json.Valid([]byte(def.AttrsJSON)) {
		return nil, fmt.Errorf("组合包相关定义属性损坏: %s", def.ItemID)
	}
	var attrs map[string]any
	decoder := json.NewDecoder(strings.NewReader(def.AttrsJSON))
	decoder.UseNumber()
	if err := decoder.Decode(&attrs); err != nil {
		return nil, fmt.Errorf("组合包相关定义属性损坏: %s: %w", def.ItemID, err)
	}
	return attrs, nil
}

func bundlePositiveQuantity(value any) (int64, error) {
	number, ok := value.(json.Number)
	if ok {
		rational, valid := new(big.Rat).SetString(string(number))
		if valid && rational.IsInt() && rational.Num().IsInt64() && rational.Sign() > 0 {
			return rational.Num().Int64(), nil
		}
	}
	return 0, fmt.Errorf("组合包数量必须是正整数且不能溢出: %v", value)
}

func bundleQuantityLimit(class string) int64 {
	switch fieldOpAt(class, itemQtyFieldIdx(class)) {
	case "BYTE":
		return math.MaxUint8
	case "U16":
		return math.MaxUint16
	case "U32":
		return math.MaxUint32
	default:
		return math.MaxInt64
	}
}

// prepareBundleItems 只展开类别 55 的组合包，数量表示包数；计件内含物沿用定义单位量，
// 非计件内含物逐个创建。嵌套包和超大实例列表直接拒绝，避免循环或无界分配。
// 此函数只构造内存对象，必须在开启库存事务之前调用。
//
// prepareBundleItems expands only category 55 bundles, with packs denoting bundle count.
// Countable contents use definition units, while non-countable contents get separate instances.
// Nested bundles and oversized instance lists are rejected to prevent cycles or unbounded allocation.
// This function only prepares in-memory objects and must run before the inventory transaction.
func (s *Server) prepareBundleItems(ctx context.Context, uid int64, itemID, container string, packs int64) ([]*store.Item, error) {
	if packs <= 0 {
		return nil, fmt.Errorf("组合包包数非法: %s: %d", itemID, packs)
	}
	def, err := s.DB.GetItemDefByItemID(ctx, itemID)
	if err != nil || def == nil {
		return nil, fmt.Errorf("组合包定义不存在: %s: %w", itemID, err)
	}
	if def.Category != bundleCategory {
		return nil, fmt.Errorf("不是组合包定义: %s", itemID)
	}
	attrs, err := bundleAttrs(def)
	if err != nil {
		return nil, err
	}
	contents, ok := attrs["nbkhfhenbag"].([]any)
	if !ok || len(contents) == 0 {
		return nil, fmt.Errorf("组合包没有内含物: %s", itemID)
	}
	type contentPlan struct {
		def   *store.ItemDef
		units int64
	}
	plans := make([]contentPlan, 0, len(contents))
	instances := int64(0)
	for _, raw := range contents {
		entry, _ := raw.(map[string]any)
		inner, _ := entry["cemjmlpafmc"].(string)
		if inner == "" {
			return nil, fmt.Errorf("组合包内含物缺少 item_id: %s", itemID)
		}
		quantity, err := bundlePositiveQuantity(entry[csQty])
		if err != nil {
			return nil, fmt.Errorf("组合包 %s 内含物 %s: %w", itemID, inner, err)
		}
		if quantity > math.MaxInt64/packs {
			return nil, fmt.Errorf("组合包内含物数量溢出: %s", inner)
		}
		child, err := s.DB.GetItemDefByItemID(ctx, inner)
		if err != nil || child == nil {
			return nil, fmt.Errorf("组合包内含物定义不存在: %s: %w", inner, err)
		}
		if child.Category == bundleCategory {
			return nil, fmt.Errorf("不支持嵌套或循环组合包: %s -> %s", itemID, inner)
		}
		if _, ok := resolveWireClass(child); !ok {
			return nil, fmt.Errorf("组合包内含物类别尚不支持: %s: %d", inner, child.Category)
		}
		units := quantity * packs
		count := units
		if countableCategories[child.Category] {
			count = 1
		}
		if count > maxBundlePreparedItems-instances {
			return nil, fmt.Errorf("组合包实例数超过上限 %d: %s", maxBundlePreparedItems, itemID)
		}
		instances += count
		plans = append(plans, contentPlan{def: child, units: units})
	}

	items := make([]*store.Item, 0, int(instances))
	totals := make(map[string]int64)
	for _, plan := range plans {
		count := plan.units
		if countableCategories[plan.def.Category] {
			count = 1
			attrs, err := bundleAttrs(plan.def)
			if err != nil {
				return nil, err
			}
			if value, exists := attrs[csQty]; exists {
				if _, err := bundlePositiveQuantity(value); err != nil {
					return nil, fmt.Errorf("组合包内含物单位量非法: %s: %w", plan.def.ItemID, err)
				}
			}
		}
		for i := int64(0); i < count; i++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			it, err := s.prepareNewItem(ctx, uid, plan.def.ItemID, container, 1, "")
			if err != nil {
				return nil, err
			}
			if countableCategories[plan.def.Category] {
				limit := bundleQuantityLimit(it.WireClass)
				if it.Qty <= 0 || it.Qty > limit/plan.units {
					return nil, fmt.Errorf("组合包内含物单位换算溢出: %s", it.ItemID)
				}
				quantity := it.Qty * plan.units
				if totals[it.ItemID] > limit-quantity {
					return nil, fmt.Errorf("组合包内含物总量溢出: %s", it.ItemID)
				}
				totals[it.ItemID] += quantity
				fields, err := FieldsFromJSON(it.FieldsJSON)
				if err != nil {
					return nil, err
				}
				if !setItemQty(it.WireClass, fields, uint64(quantity)) {
					return nil, fmt.Errorf("组合包计件内含物缺少数量字段: %s", it.ItemID)
				}
				it.FieldsJSON, err = FieldsToJSON(fields)
				if err != nil {
					return nil, err
				}
				it.Qty = quantity
			}
			items = append(items, it)
		}
	}
	return items, nil
}
