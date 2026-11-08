package gameserver

import (
	"context"
	"fmt"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

func nativeInventoryContainers(kind uint64) []string {
	switch kind {
	case containerBackpack:
		return []string{"backpack", "rig"}
	case 17:
		return []string{fmt.Sprintf("native:%d", kind)}
	case containerTradingCards:
		// Album cards remain in the collection inventory; their parent GUID
		// determines which album slot the client displays them in.
		return []string{tradingCardContainer, "card_deck"}
	case 10, 11:
		// 房屋仓库（容器枚举 10/11 统一落 item.container='house'，与
		// containerEnumName 一致）。缺这条映射时 4/1 计数与 4/4 列表
		// 对房屋容器一律回空，客户端房屋仓库 UI 永远无数据。
		return []string{"house"}
	}
	return nil
}

// 导入快照保留渔获资料，容器和版本以当前护网为准。
// 物种稀有度（f4）始终以静态表为准——旧快照可能缺失或为 0。
func (s *Server) keepnetItemTree(ctx context.Context, entry *store.KeepnetEntry, level string) *proto.Value {
	rarity := s.DB.SpeciesRarity(ctx, entry.Species)
	row, err := s.DB.GetItemByGUID(ctx, entry.EchoGUID)
	if err == nil && row.UID == entry.UID && row.Container == "keepnet" && row.TypeID == 25 && row.WireClass == "ecknbkpeeji" {
		if fields, err := FieldsFromJSON(row.FieldsJSON); err == nil && len(fields) == 29 {
			fields[4].Num = ptrU64(uint64(rarity))
			fields[16].Hex = entry.EchoGUID
			fields[18].Num = ptrU64(containerKeepnet)
			fields[22].Num = ptrU64(uint64(netTicksNow()))
			return proto.Obj(row.WireClass, uint32(row.TypeID), fields...)
		}
	}
	return buildKeepnet25(entry, level, rarity)
}

func inventoryContainerMatches(name string, allowed []string) bool {
	for _, value := range allowed {
		if name == value {
			return true
		}
	}
	return false
}
