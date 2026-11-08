package gameserver

import "time"

// drinkRule 是本地设定的饮酒经验规则。
type drinkRule struct {
	Category string
	Peak     float64
	Duration time.Duration
}

func drinkRuleForCategory(category string) (drinkRule, bool) {
	switch category {
	case "beer":
		return drinkRule{Category: category, Peak: .05, Duration: 5 * time.Minute}, true
	case "wine":
		return drinkRule{Category: category, Peak: .10, Duration: 10 * time.Minute}, true
	case "spirit":
		return drinkRule{Category: category, Peak: .15, Duration: 15 * time.Minute}, true
	case "mead":
		return drinkRule{Category: category, Peak: .20, Duration: 20 * time.Minute}, true
	default:
		return drinkRule{}, false
	}
}

// 白名单按 4.0.25034 饮品定义和名称分类，不包含微量含酒食物或无酒精替代品。
func drinkRuleForItem(itemID string) (drinkRule, bool) {
	var category string
	switch itemID {
	case "kvas_bottle", "kvas_glass",
		"lb_bottle_ch_ale_ny", "lb_bottle_eks", "lb_bottle_eks_ny",
		"lb_bottle_krep", "lb_bottle_krep_ny", "lb_bottle_trad", "lb_bottle_trad_ny",
		"wood_bottle_cask", "wood_bottle_dark", "wood_bottle_lager",
		"wood_can_cask", "wood_can_dark", "wood_can_lager":
		category = "beer"
	case "AV_shamp_1l", "AV_shamp_1l_8", "AV_shamp_1lg", "AV_shamp_p_6l", "AV_shamp_tsr",
		"apple_sidr", "g_vine_bottle",
		"rd_ice_vine", "rd_ice_vine25", "rd_ice_vine26",
		"rd_red_vine", "rd_red_vine_2017", "rd_red_vine_8", "rd_white_vine",
		"rot_bottle_porto", "thermos_gwein", "vine_bottle_em":
		category = "wine"
	case "XXIVA_bottle_vdka", "boyar_bottle_vdka", "gorilka", "hooch_corn", "hooch_potato",
		"kala_vodka", "kr_pennik", "pervach", "tungus_vodka", "vodka_glass",
		"zdn_bottle_cogn3", "zdn_bottle_cogn5", "zdn_bottle_vdka":
		category = "spirit"
	case "barrel_honey", "pm_medovuha", "pm_medovuha_g", "pm_medovuha_g_8", "sbiten":
		category = "mead"
	default:
		return drinkRule{}, false
	}
	return drinkRuleForCategory(category)
}

// consumableRestoreForItem 食品/热饮的恢复分类（4/8 饮用通道）：
// 咖啡→精力、茶→舒适度、可可→饱食+少量精力、其余食品→饱食度。
// 分类映射来自维护者实测经验与官方游戏提示（热茶提高舒适度等），
// 见 specs/changes/player-condition-core Further Notes；恢复量为 tunables
// 标定值，非官服常量。白名单只收可直接食用的成品；烹饪材料
// （面粉/糖/油/盐/调料/生米面/香精等）与酒类（走经验加成规则）不进表。
func consumableRestoreForItem(itemID string) (string, bool) {
	switch itemID {
	// 咖啡：现磨杯/罐装/瓶装/保温瓶。
	case "big_coffee", "big_coffee_can", "big_coffee_can25", "big_coffee_can26",
		"big_coffee_can_g", "fcb_coffee_can", "fcb_coffee_can2", "brand_coffe",
		"small_coffee", "thermos_coffee":
		return "coffee", true
	// 茶：罐装/保温瓶。
	case "ab_tea_can", "ab_tea_can_g", "ab_tea_can2", "thermos_tea":
		return "tea", true
	// 可可：冲饮/保温瓶。
	case "food_cacao", "thermos_cacao":
		return "cocoa", true
	// 可直接食用的食品：蔬果/蛋奶/主食/菜肴/肉鱼/罐头/甜品/软饮。
	case "apples", "carrot", "cucumbers", "cucumbers_mc", "melon", "plum", "tangerin",
		"wmelon_ast", "onion", "potatoes", "grilled_apple", "vinegret",
		"boiled_egg", "food_egg", "farm_milk", "ff_pack_milk", "ff_pack_kefir",
		"food_sweetmilk", "farm_cheese", "ff_proc_cheese",
		"baked_potato", "bowl_potato", "fried_potato", "fish_potato",
		"buckwheat_porridge", "borshch", "fish_soup_nal", "fish_soup_red", "fish_soup_tr",
		"rye_bread_r", "loaf_01", "kalach", "bublik", "r_pie", "pancakes",
		"christmas_pancakes", "pelmeni", "sandwich_cafe", "salmon_steak", "pot_roast",
		"beef", "pork", "lard", "hm_lard", "cutlet_p", "pike_cutlets", "shashlyk_p",
		"fried_bychki", "fried_carp", "fried_karasy", "fried_nalim", "fried_sudak",
		"sm_pikeperch", "sm_trout", "smoked_bream", "smoked_catfish", "smoked_salmon",
		"boiled_crayfish", "dried_chekhon", "dried_plotva", "dried_taran", "dried_vobla",
		"caviar_black", "caviar_pike", "caviar_red",
		"ff_krak_sausage", "grilled_sausage", "ff_pork_canned", "ff_saira_canned",
		"cf_codliver", "cf_crab", "cf_halibut", "cf_oyster", "cf_seabass", "cf_squid",
		"rf_codliver", "rf_crab", "rf_halibut", "rf_oyster", "rf_seabass",
		"cake_6years", "cake_rf4", "candy_murka_1", "candy_murka_2",
		"candystick_1", "candystick_2", "candystick_3",
		"chocofish_1", "chocofish_2", "chocofish_3", "chocolate_santa25",
		"pb_d_choc", "pb_d_choc25", "pb_d_choc26", "pb_d_choc_8", "pb_d_choc_g", "pb_d_chocg",
		"pb_m_choc", "pb_m_choc25", "pb_m_choc26", "pb_m_choc_8", "pb_m_choc_g", "pb_m_chocg",
		"sd_shoc_bar", "pryanik_ny26", "pack_marshm", "grilled_marshm",
		"christmas_dish", "fishcookie",
		"mc_bottle_cola", "mc_can_cola", "brand_softdrink", "mors_bottle_hm", "kisel_jug":
		return "food", true
	}
	return "", false
}

// drinkRate 前 75% 时间为峰值，最后 25% 时间线性降至零。
func drinkRate(category string, remaining time.Duration) float64 {
	rule, ok := drinkRuleForCategory(category)
	if !ok || remaining <= 0 {
		return 0
	}
	tail := rule.Duration / 4
	if remaining >= tail {
		return rule.Peak
	}
	return rule.Peak * float64(remaining) / float64(tail)
}
