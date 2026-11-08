package gameserver

import (
	"context"
	"strconv"

	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

// 基础实现：轮管卡默认关闭，按竿持久化卡米数。
const (
	reelClipSettingKey  = "reel_clip_m"
	reelDragSettingKey  = "reel_drag_setting"
	reelSpeedSettingKey = "reel_speed_setting"
	maxReelClipM        = 10000
	// defaultReelSpeed 新竿 f5.f7 的出厂默认，也是玩家从未调过 17/15 时的收线速度。
	defaultReelSpeed int64 = 15
)

func clampReelClipM(v int64) int64 {
	if v < 0 {
		return 0
	}
	if v > maxReelClipM {
		return maxReelClipM
	}
	return v
}

func rigClipOf(v *proto.Value) int64 {
	if v == nil || v.Kind != proto.KindObj || v.FieldCount() <= 5 {
		return 0
	}
	f := v.Field(5)
	if f.Num == nil {
		return 0
	}
	return clampReelClipM(int64(u32Of(f.Num)))
}

// buildRig153WithClip 构造 153 钓组对象（不带选中挂饵方式，f8=0）。
// f9 按钓组类型给可选集，否则新建竿的安装弹窗是全空。
func buildRig153WithClip(name string, rigType int64, comps map[int64]rigComp, depth, clipM int64) *proto.Value {
	return buildRig153Full(name, rigType, comps, depth, clipM, 0, baitMountAvailForRig(rigType, 0))
}

// buildRig153Full 构造 153 钓组对象，写入挂饵方式的两个字段。
//
// 客户端 BaitMountType 在 153 里占两个字段，同为 cacifnemgdl 枚举：
//   - f8（fihgclhkkci）= 当前选中的安装方式，0 = 未选择，安装栏显示「没选择」；
//   - f9（cfdddmphebg）= 可选安装方式位掩码，0 = 一个选项都没有，弹窗全空。
//
// 2026-11-04 实测：f9=126 时「选择安装方式」弹窗列出 6 项
// （下沉香煮成饵 / 颗粒 / 沉+沉 / 颗+颗 / 颗+沉 / 沉+Pop-up），
// 玩家点选后客户端回 17/6，携带的是单个位（4/8/16/32/64）。
// 只把该值写进 f9 时弹窗能开、但安装栏始终显示「没选择」——选中的是 f8。
func buildRig153Full(name string, rigType int64, comps map[int64]rigComp, depth, clipM, baitMountSel, baitMountAvail int64) *proto.Value {
	if name == "" {
		name = "rig_FloatSimple"
	}
	return wiregen.MustMakeObj("gbbhdkefnkn", map[int]*proto.Value{
		0: proto.StrVal(name),
		1: proto.U32(uint32(rigType)),
		2: proto.Args(proto.S("81"), sortedRigCompItems(comps, rigType)...),
		3: proto.U32(0),
		4: proto.U32(uint32(depth)),
		5: proto.U32(uint32(clampReelClipM(clipM))),
		6: proto.U32(15),
		7: proto.U32(uint32(defaultReelSpeed)),
		8: proto.U32(uint32(clampBaitMount(baitMountSel))),
		9: proto.U32(uint32(clampBaitMount(baitMountAvail))),
	})
}

// buildRig153Like 以已有的 153 对象为模板重建。f9（可选项）按当前钓组类型
// 重算——反编译证明客户端对「钓组 ↔ 挂饵方式」零过滤（bbhjnacijjf 打开及
// 每次 153 推送都用 f1/f9/f8 三字段重建列表），联动只能由服务端在这里给出。
// f8 不在新可选集内时清零，客户端安装栏自动回到「没选择」。
func buildRig153Like(src *proto.Value, name string, rigType int64, comps map[int64]rigComp, depth, clipM int64) *proto.Value {
	sel, avail := rigBaitMountsOf(src)
	avail = baitMountAvailForRig(rigType, avail)
	if sel&avail != sel {
		sel = 0
	}
	out := buildRig153Full(name, rigType, comps, depth, clipM, sel, avail)
	copyRigReelSettings(src, out)
	return out
}

// copyRigReelSettings 保留实例 f5 已保存的摩擦档位 f6 和收线速度 f7。
// 15 只是新建竿的出厂默认；14/2、4/22 重建钓组时如果照抄默认值，
// 17/14、17/15 保存的玩家设定会在下一次下发时被冲掉。
func copyRigReelSettings(src, dst *proto.Value) {
	if src == nil || src.Kind != proto.KindObj || dst == nil || dst.Kind != proto.KindObj {
		return
	}
	for _, idx := range []int{6, 7} {
		if idx >= len(src.Fields) || idx >= len(dst.Fields) {
			continue
		}
		if f := src.Fields[idx]; f.Num != nil {
			dst.Fields[idx] = proto.Field{Op: "U32", Num: ptrU64(u64Of(f.Num))}
		}
	}
}

// applyRigBaitMountRender 渲染期按 153.f1 的钓组类型重算 f9 可选掩码，
// f8 不在新可选集内时清零。存量竿的快照可能还存着联动上线前的全开 f9，
// 回放存档的路径（rebuildRod）会原样下发，必须在这里兜住。
// 只改本次下发的 wire 对象，不落库；未知钓组沿用实例旧值，行为不变。
func applyRigBaitMountRender(rig *proto.Value) {
	if rig == nil || rig.Kind != proto.KindObj || rig.FieldCount() <= 9 {
		return
	}
	var rt int64
	if f := rig.Field(1); f.Num != nil {
		rt = int64(u32Of(f.Num))
	}
	sel, stored := rigBaitMountsOf(rig)
	avail := baitMountAvailForRig(rt, stored)
	if sel&avail != sel {
		sel = 0
	}
	setRigBaitMountSel(rig, sel)
	setRigBaitMountAvail(rig, avail)
}

// clampBaitMount 把挂饵方式收进 u32；位宽不设上限，
// 客户端 BaitMountType 是两个独立枚举值（选中 f8 / 可选掩码 f9），
// 枚举项数量未知，窄掩码会悄悄截掉高位选项。
func clampBaitMount(v int64) int64 {
	if v < 0 {
		return 0
	}
	if v > 0xffffffff {
		return 0xffffffff
	}
	return v
}

// rigBaitMountsOf 读 153 对象的 (选中值 f8, 可选项掩码 f9)。
func rigBaitMountsOf(v *proto.Value) (sel, avail int64) {
	if v == nil || v.Kind != proto.KindObj || v.FieldCount() <= 9 {
		return 0, 0
	}
	if f := v.Field(8); f.Num != nil {
		sel = clampBaitMount(int64(u32Of(f.Num)))
	}
	if f := v.Field(9); f.Num != nil {
		avail = clampBaitMount(int64(u32Of(f.Num)))
	}
	return sel, avail
}

// rigBaitMountSelOf 读 153 对象 f8（当前选中的挂饵方式）。
func rigBaitMountSelOf(v *proto.Value) int64 {
	sel, _ := rigBaitMountsOf(v)
	return sel
}

// setRigBaitMountSel 就地改写 153 对象 f8。
func setRigBaitMountSel(v *proto.Value, sel int64) {
	if v == nil || v.Kind != proto.KindObj || v.FieldCount() <= 8 {
		return
	}
	v.Fields[8].Num = ptrU64(uint64(clampBaitMount(sel)))
}

// setRigBaitMountAvail 就地改写 153 对象 f9。
func setRigBaitMountAvail(v *proto.Value, avail int64) {
	if v == nil || v.Kind != proto.KindObj || v.FieldCount() <= 9 {
		return
	}
	v.Fields[9].Num = ptrU64(uint64(clampBaitMount(avail)))
}

const baitMountSettingKey = "baitmount"

// defaultBaitMountAvail 是未知/非沉底钓组的可选掩码（沿用旧行为，不限制）。
//
// 2026-11-05 反编译修正：客户端对「钓组类型 ↔ 挂饵方式」不做任何过滤——
// 安装方式弹窗（bbhjnacijjf）打开及每次收到 153 推送时都用
// bkcpbemimgl(153.f1 钓组类型, 153.f9 可选掩码, 153.f8 选中值) 重建模型，
// gdgnidafbmp 按 f9 的每个位各生成一行，钓组类型只用于预览图命名。
// 旧注释「客户端自己按竿型+已挂鱼饵筛掉不适用的项」是误判，联动是服务端职责。
var defaultBaitMountAvail int64 = 0xffffffff

// 可选挂饵方式集合：
//   - baitMountSetBottom：官方抓包 f9=126 锚点（下沉香煮成饵/颗粒/沉+沉/
//     颗+颗/颗+沉/沉+Pop-up 六种基础沉底挂法）；
//   - baitMountPopupFamily：pop-up/人造玉米族六位（27/28 由客户端专属
//     _chod 预览图实证）；
//   - baitMountMaggotFamily：玉米浮+蛆5/10/15；
//   - baitMountFoamFamily：ZIG 泡沫族。
//   - 128/256/512/1024/2048 等组合位按编排表直接写数值。
const (
	baitMountSetBottom    = 126                                       // 2|4|8|16|32|64
	baitMountPopupFamily  = 1024 | 2048 | 4096 | 8192 | 16384 | 32768 // Pop-up/人造玉米族
	baitMountMaggotFamily = 65536 | 131072 | 262144                   // 玉米浮+蛆5/10/15
	baitMountFoamFamily   = 524288 | 1048576 | 2097152                // 泡沫族（Zig 专用）
)

// rigBaitMountAvail 钓组类型 → 可选挂饵方式掩码（153.f9）。
// 编排表由用户按官方游戏知识逐钓组给定（2026-11-05），锚点 = 官方抓包
// f9=126 与 27/28 的 _chod 预览图。30/31（浮钓 carp 发丝/浮钓 carp
// Pop-up）暂未编排，维持 126+pop-up 族；未列出的钓组类型
// （浮钓/路亚/海钓）不做限制，维持旧行为。
var rigBaitMountAvail = map[int64]int64{
	// 基础沉底族：普通沉底/帕特诺斯特/内联/环圈/方法/活鱼/火箭/标记。
	12: baitMountSetBottom, 13: baitMountSetBottom, 14: baitMountSetBottom,
	15: baitMountSetBottom, 16: baitMountSetBottom, 17: baitMountSetBottom,
	18: baitMountSetBottom, 23: baitMountSetBottom,
	// 发丝钓组：兼容基础底钓饵、颗粒及单/双饵组合（含玉米浮）。
	24: baitMountSetBottom | 128 | 256 | 512 | 1024 | 2048,
	// 雪人钓组：核心为沉底饵+Pop-up / 浮水组合。
	25: 64 | 128 | 256 | 512 | 4096 | 8192 | 16384 | 32768,
	// Pop-up/铰链/Chod/方法饵 Pop-up：纯浮水体系，pop-up/玉米族六位。
	26: baitMountPopupFamily, 27: baitMountPopupFamily, 28: baitMountPopupFamily,
	32: baitMountPopupFamily,
	// 底蛆钓组：玉米浮及玉米浮+蛆混合体系。
	29: 1024 | 2048 | baitMountMaggotFamily,
	// 固定 Zig：ZIG 泡沫体系及浮水组合。
	34: 16384 | 32768 | baitMountFoamFamily,
	// 暂未编排，维持过渡值。
	30: baitMountSetBottom | baitMountPopupFamily,
	31: baitMountSetBottom | baitMountPopupFamily,
}

// baitMountAvailForRig 返回该钓组类型应下发的 f9。
// 飞德竿默认 BottomSimple(12)：上游 126 的窄掩码会让客户端自动选中活饵
// 安装方式，切竿后表现为自动跳到活饵钓组；这里仅对 12 恢复旧版全开，
// 其他钓组继续按上游编排表收窄，等上游修复后再去掉此特例。
func baitMountAvailForRig(rigType, stored int64) int64 {
	if rigType == 12 {
		return defaultBaitMountAvail
	}
	if m, ok := rigBaitMountAvail[rigType]; ok {
		return m
	}
	if stored != 0 {
		return stored
	}
	return defaultBaitMountAvail
}

// baitMountFor 返回渲染钓组时应写入 f8 的选中值：玩家选过的优先，否则 0（未选择）。
func (s *Server) baitMountFor(ctx context.Context, rodGUID string) int64 {
	if v, ok := s.savedBaitMount(ctx, rodGUID); ok {
		return v
	}
	return 0
}

// applyRigBaitMount 把玩家选中的安装方式写进竿实例 153.f8，并刷新版本戳。
// f9（可选项）保持不变，否则弹窗会只剩玩家刚点的那一项；缺失时按钓组类型补。
func (s *Server) applyRigBaitMount(ctx context.Context, rodGUID string, sel int64) error {
	row, err := s.DB.GetItemByGUID(ctx, rodGUID)
	if err != nil || row == nil {
		return err
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return err
	}
	idx := fieldIdxByCS(row.WireClass, csRigObject)
	if idx < 0 || idx >= len(fields) || fields[idx].Val == nil {
		return nil
	}
	setRigBaitMountSel(fields[idx].Val, sel)
	if _, avail := rigBaitMountsOf(fields[idx].Val); avail == 0 {
		setRigBaitMountAvail(fields[idx].Val, baitMountAvailForRig(s.rigTypeOfItem(ctx, row), 0))
	}
	s.saveItemFields(ctx, row, fields)
	return nil
}

// savedBaitMount 读取玩家在某根竿上选过的挂饵方式。
// 旧选择不再属于该竿当前钓组类型的可选集时（换钓组后的残留值，含更早版本
// 按全开掩码落库的存量），判为未设置——挂饵份数（baitHookCount）走同一读口，
// 自动回退一份，不需要各读方重复校验。
func (s *Server) savedBaitMount(ctx context.Context, rodGUID string) (int64, bool) {
	if s.DB == nil || rodGUID == "" {
		return 0, false
	}
	raw, err := s.DB.GetRodSetting(ctx, rodGUID, baitMountSettingKey)
	if err != nil || raw == "" {
		return 0, false
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	sel := clampBaitMount(v)
	if sel == 0 {
		return 0, false
	}
	row, err := s.DB.GetItemByGUID(ctx, rodGUID)
	if err != nil || row == nil {
		return sel, true
	}
	if m := baitMountAvailForRig(s.rigTypeOfItem(ctx, row), 0); sel&m != sel {
		return 0, false
	}
	return sel, true
}

func (s *Server) savedReelClipM(ctx context.Context, rodGUID string) (int64, bool) {
	if s.DB == nil || rodGUID == "" {
		return 0, false
	}
	raw, err := s.DB.GetRodSetting(ctx, rodGUID, reelClipSettingKey)
	if err != nil || raw == "" {
		return 0, false
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return clampReelClipM(v), true
}
