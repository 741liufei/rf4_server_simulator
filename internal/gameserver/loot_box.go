package gameserver

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"rf4server/internal/store"
)

// ---- 钓获礼包（开箱） ----
//
// 玩家钓上鱼并入护后，按概率触发"礼包"，服务端当场掷骰开箱：
// 选出奖励 → 直接进背包 → 推送物品更新 → 聊天播报"开到了什么"。
//
// 设计取舍：客户端没有可用的"开箱弹窗"协议（10304 奖励包只有字段表、
// 服务端零实现），因此不走新增 opcode，而是复用已有的
// AddToStack + pushItemUpdate + 聊天播报，做到"钓上来直接开到东西"的体感。
//
// 配置写在 kv.loot_box_defs（JSON），改完 30 秒内生效，不用重启：
//
//	[{"id":"event_box","name":"活动礼包","chance":0.05,
//	  "rewards":[{"item_id":"worm_01","qty":2,"weight":10}]}]
//
// rewards 留空时，从商店货架随机抽一件有库存的商品（用户要求的"随机商店物品"）。

const lootBoxDefsKVKey = "loot_box_defs"

// lootBoxReward 礼包里的单个奖励。
type lootBoxReward struct {
	ItemID string  `json:"item_id"`
	Qty    int64   `json:"qty"`
	Weight float64 `json:"weight"`
}

// lootBoxDef 一种礼包。
type lootBoxDef struct {
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Chance  float64         `json:"chance"` // 每次钓获触发的概率 0..1
	Rewards []lootBoxReward `json:"rewards"`
	// CarrierItem 可选：开袋时额外发放的"可见载体"物品。
	// category 73 的 gift_bag 没有 wire class 映射，直接发会让客户端物化出错，
	// 因此默认不发；需要"背包里看得见一个袋子"时，把这里填成可正常显示的道具
	// （例如 category 55 的套盒）。
	CarrierItem string `json:"carrier_item"`
	CarrierQty  int64  `json:"carrier_qty"`
}

// defaultLootBoxDefs 内置兜底：5% 概率钓到"活动礼包"，随机开出一件商店商品。
var defaultLootBoxDefs = []lootBoxDef{
	{ID: "event_box", Name: "福袋", Chance: 0.05},
}

// lootBoxDefs 读当前生效的礼包表：优先 kv.loot_box_defs，解析失败用内置兜底。
func (s *Server) lootBoxDefs(ctx context.Context) []lootBoxDef {
	if s.DB != nil {
		var raw string
		if err := s.DB.SQL.QueryRowContext(ctx, `SELECT v FROM kv WHERE k=?`, lootBoxDefsKVKey).Scan(&raw); err == nil && raw != "" {
			var defs []lootBoxDef
			if json.Unmarshal([]byte(raw), &defs) == nil {
				return defs
			}
			s.log.Warn("礼包配置解析失败，使用内置兜底", "kv", lootBoxDefsKVKey)
		}
	}
	return defaultLootBoxDefs
}

// rollLootBox 在钓获入护后尝试触发礼包；命中则发奖并播报。
// 返回开出的物品 ID（未触发为空）。
func (s *Server) rollLootBox(ctx context.Context, sess *Session, catch *RolledFish) string {
	if s == nil || s.DB == nil || sess == nil {
		return ""
	}
	// 总开关：默认关闭，需要管理员在后台显式开启后才触发。
	if s.tunableF("loot_box_enabled", 0) != 1 {
		return ""
	}
	defs := s.lootBoxDefs(ctx)
	if len(defs) == 0 {
		return ""
	}
	// 保底：钓到奖杯(3)/蓝冠(4)鱼时必定开一包，不受概率影响。
	// 由 tunables.loot_box_trophy_guarantee 控制（默认开启）。
	if guarantee := s.tunableF("loot_box_trophy_guarantee", 1) == 1; guarantee {
		if g := catchGrade(catch); g >= 3 {
			if itemID := s.grantLootBox(ctx, sess, defs[0], catch, "保底"); itemID != "" {
				return itemID
			}
		}
	}
	// 一次钓获最多开一个礼包：按顺序判定，命中即停。
	for _, def := range defs {
		chance := s.lootBoxChance(def)
		if chance <= 0 || rand.Float64() >= chance {
			continue
		}
		if itemID := s.grantLootBox(ctx, sess, def, catch, "随机"); itemID != "" {
			return itemID
		}
	}
	return ""
}

// lootBoxChance 取该礼包的触发概率：tunables.loot_box_chance 优先（后台可调），
// 否则用配置里的 chance。负数视为关闭。
func (s *Server) lootBoxChance(def lootBoxDef) float64 {
	p := s.tunableF("loot_box_chance", -1)
	if p < 0 {
		return def.Chance
	}
	if p > 1 {
		return 1
	}
	return p
}

// catchGrade 安全取鱼档位（1 未达标 / 2 合格 / 3 奖杯 / 4 蓝冠）。
func catchGrade(f *RolledFish) int64 {
	if f == nil {
		return 0
	}
	return f.Grade
}

// grantLootBox 抽奖并发放，失败返回空串。
func (s *Server) grantLootBox(ctx context.Context, sess *Session, def lootBoxDef, catch *RolledFish, reason string) string {
	itemID, qty, name := s.pickLootReward(ctx, def)
	if itemID == "" {
		return ""
	}
	row, _, err := s.AddToStack(ctx, sess.Userid, itemID, "backpack", qty)
	if err != nil {
		s.log.Warn("礼包发奖失败", "uid", sess.Userid, "box", def.ID, "item", itemID, "err", err)
		return ""
	}
	s.pushItemUpdate(sess.Userid, row)
	// 可选载体：让背包里出现一个看得见的"袋子"。
	if def.CarrierItem != "" && def.CarrierItem != itemID {
		qty := def.CarrierQty
		if qty <= 0 {
			qty = 1
		}
		if crow, _, cerr := s.AddToStack(ctx, sess.Userid, def.CarrierItem, "backpack", qty); cerr == nil {
			s.pushItemUpdate(sess.Userid, crow)
		} else {
			s.log.Warn("福袋载体发放失败", "uid", sess.Userid, "carrier", def.CarrierItem, "err", cerr)
		}
	}
	display := name
	if display == "" {
		display = itemID
	}
	prefix := "钓到了" + def.Name
	if reason == "保底" {
		prefix = "上星保底：" + def.Name
	}
	sess.pushSystemChat(fmt.Sprintf("%s，开出：%s ×%d", prefix, display, qty))
	s.log.Info("礼包开出", "uid", sess.Userid, "box", def.ID, "item", itemID, "qty", qty,
		"reason", reason, "fish", catchSpecies(catch), "grade", catchGrade(catch))
	return itemID
}

// catchSpecies 安全取鱼种名（catch 可能为 nil）。
func catchSpecies(f *RolledFish) string {
	if f == nil {
		return ""
	}
	return f.Species
}

// pickLootReward 选出奖励：配置了 rewards 就按权重抽，否则从商店货架随机一件。
func (s *Server) pickLootReward(ctx context.Context, def lootBoxDef) (itemID string, qty int64, name string) {
	if len(def.Rewards) > 0 {
		var total float64
		for _, r := range def.Rewards {
			if r.ItemID != "" && r.Weight > 0 {
				total += r.Weight
			}
		}
		if total > 0 {
			x := rand.Float64() * total
			for _, r := range def.Rewards {
				if r.ItemID == "" || r.Weight <= 0 {
					continue
				}
				if x < r.Weight {
					q := r.Qty
					if q <= 0 {
						q = 1
					}
					return r.ItemID, q, s.lootItemName(ctx, r.ItemID)
				}
				x -= r.Weight
			}
		}
	}
	return s.pickRandomShopItem(ctx)
}

// pickRandomShopItem 从生效货架随机抽一件可购买商品。
// 排除套盒/礼包类（55/73），避免开出"套娃"以及 category 73 被错误物化的物品。
func (s *Server) pickRandomShopItem(ctx context.Context) (itemID string, qty int64, name string) {
	row := s.DB.SQL.QueryRowContext(ctx, `
		SELECT s.item_id
		FROM shop_item s
		JOIN item_def d ON d.item_id = s.item_id
		WHERE s.stock > 0
		  AND d.category NOT IN (55, 73)
		ORDER BY RANDOM() LIMIT 1`)
	if err := row.Scan(&itemID); err != nil || itemID == "" {
		s.log.Warn("礼包抽取商店物品失败", "err", err)
		return "", 0, ""
	}
	return itemID, 1, s.lootItemName(ctx, itemID)
}

// lootItemName 取物品中文名（缺省回退 item_id）。
func (s *Server) lootItemName(ctx context.Context, itemID string) string {
	var name string
	if err := s.DB.SQL.QueryRowContext(ctx,
		`SELECT COALESCE(NULLIF(name_zh,''), NULLIF(name,''), ?) FROM item_catalog WHERE item_id=? LIMIT 1`,
		itemID, itemID).Scan(&name); err != nil || strings.TrimSpace(name) == "" {
		return itemID
	}
	return name
}

var _ = store.Item{}

// scheduleLootBox 异步开箱：不阻塞 14/5 入护回包，也不受请求 ctx 取消影响。
func (s *Server) scheduleLootBox(sess *Session, catch *RolledFish) {
	if s == nil || sess == nil || s.DB == nil {
		return
	}
	fish := catch
	if fish != nil {
		cp := *fish
		fish = &cp
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.log.Warn("礼包开箱 panic", "uid", sess.Userid, "panic", r)
			}
		}()
		ctx, cancel := context.WithTimeout(s.dbCtx(), 30*time.Second)
		defer cancel()
		s.rollLootBox(ctx, sess, fish)
	}()
}
