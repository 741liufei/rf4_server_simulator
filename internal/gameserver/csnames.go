package gameserver

// 物品字段名常量。创建和读取物品实例时应统一使用这些常量，避免字段名拼写错误。
const (
	csGUID        = "cahhdnicmbn" // CS_GUID       实例 guid
	csParent      = "odflendafcg" // CS_PARENT     所属父件（竿）
	csSlotEnum    = "mfllbndghhe" // CS_SLOT_ENUM
	csNumericID   = "iofbjlaadfg" // CS_NUMERIC_ID 物品数字 id（客户端渲染身份）
	csWorkmanship = "hjilekjlgbk" // CS_WORKMANSHIP 工艺
	csDurability  = "nmidlgejlcp" // CS_DURABILITY  耐久
	csCreated     = "adhajflcpcd" // CS_CREATED     创建 Ticks
	csUpdated     = "iingicaldbk" // CS_UPDATED     更新 FILETIME
	csStateEnum   = "hgaijieklek" // CS_STATE_ENUM  状态位
	csSubtype     = "dbgaheblmei" // CS_SUBTYPE_ENUM
	csQty         = "ljnnpokapig" // 数量（可堆叠件）
	csCustomName  = "phoglfnffah" // 自定义名称（客户端制作时填写的名字）
	csStrength    = "nlmnpekipjc" // 强度（受力部件）
	csDragForce   = "ppibbfgiffa" // 渔轮刹车片子实例的曳力物理值（loedbndnill.f0）
	csOrigin      = "effnpjlfjai" // 购买收据 166 pcfflcdpjgk 内嵌位
	csRigObject   = "pgcabefancm" // 竿实例的 153 钓组对象（f5）
	csLine        = "bghbekmllgp" // 竿实例的钓线 guid 字段（f3；客户端按钓线模型解析）
	csReel        = "neangpifoka" // 竿实例的渔轮 guid 字段（f4；客户端按渔轮模型解析）
	csRigTip      = "kjphlnnlflh" // 竿实例的当前尖梢（f8，U32N 字符串）
	csSplitSource = "hmfmpbemjkg" // 计件挂载子实例的来源堆 guid（f2；卸下时 +1 退回）
	csHandLoaded  = "nmoceodgdgb" // 手持工具（弹弓/打窝器）f1 的当前装填饵堆 guid
)
