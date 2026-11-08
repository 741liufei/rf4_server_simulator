package gameserver

import (
	"database/sql"
	"errors"
	"strings"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// 礼盒（ieopbeejfmc=true 的 cat 55 组合包）4/8 使用拆开。
// 官服行为：礼盒以本体躺在背包，玩家点"使用"客户端发 4/8
// args("524")[容器枚举, 礼盒 guid]，服务端删礼盒、展开发放内容物，
// 回 800：f0=内容物、f1=更新物、f2=移除 GUID，客户端按快照逐项入库/移除。
// 饵料盒（ieopbeejfmc 为空）不走本分支——官服在购买时已直接展开。

// useGiftBox 拦截指向礼盒的 4/8；目标不是礼盒时返回 handled=false，
// 落回 hItemUse 的酒精/技能书/通用分支。
func (s *Server) useGiftBox(ctx *Ctx) ([]byte, bool) {
	if !s.requireDB(ctx) {
		return nil, false
	}
	if ctx.Tree == nil || ctx.Tree.Kind != proto.KindArgs || len(ctx.Tree.Items) != 2 {
		return nil, false
	}
	storage, hasStorage := ctx.ArgInt(0)
	arg := ctx.Tree.ArgItem(1)
	if arg == nil || arg.Kind != proto.KindGuid || !validRigGUID(arg.Hex) {
		return nil, false
	}
	guid := strings.ToLower(arg.Hex)
	cctx, uid := ctx.Sess.pushCtx(), ctx.Sess.Userid
	// 分类用只读查询：不是礼盒就不拦（无副作用，落回通用分支）。
	row, err := s.DB.GetItemByGUID(cctx, guid)
	if err != nil || row == nil || row.UID != uid {
		return nil, false
	}
	def, err := s.DB.GetItemDefByItemID(cctx, row.ItemID)
	if err != nil || def == nil || def.Category != bundleCategory || !bundleOpenable(def) {
		return nil, false
	}
	fail := func(message string) ([]byte, bool) {
		ctx.ReplyFailure("System.InvalidOperationException", message)
		return nil, true
	}
	if ctx.Sess.Closed() {
		return fail("会话已关闭，请重新连接")
	}
	if !hasStorage || storage != 0 || row.Container != "backpack" || row.RigGUID != "" {
		return fail("礼盒只能在背包中打开")
	}
	// prepareBundleItems 只读、必须在库存事务之前调用（见其函数注释）。
	contents, err := s.prepareBundleItems(cctx, uid, row.ItemID, "backpack", 1)
	if err != nil {
		ctx.Log.Warn("礼盒内容物构造失败", "item", row.ItemID, "err", err)
		return fail("礼盒打开未完成，请重试")
	}
	tx, err := beginEconomyTx(cctx, s.DB)
	if err != nil {
		ctx.Log.Warn("礼盒开启事务失败", "err", err)
		return fail("礼盒打开未完成，请重试")
	}
	defer func() { _ = tx.Rollback() }()
	fresh, err := getOwnedItemTx(cctx, tx, uid, guid)
	if errors.Is(err, sql.ErrNoRows) {
		return fail("礼盒不存在或已被打开")
	}
	if err != nil {
		ctx.Log.Warn("礼盒读取失败", "guid", guid, "err", err)
		return fail("礼盒打开未完成，请重试")
	}
	if fresh.Container != "backpack" || fresh.RigGUID != "" {
		return fail("礼盒只能在背包中打开")
	}
	removed, err := removeItemsTx(cctx, tx, uid, []string{guid})
	if err != nil || removed != 1 {
		ctx.Log.Warn("礼盒删除失败", "guid", guid, "removed", removed, "err", err)
		return fail("礼盒打开未完成，请重试")
	}
	granted := make([]*store.Item, 0, len(contents))
	for _, it := range contents {
		out, _, gerr := grantPreparedItemTx(cctx, tx, it, true)
		if gerr != nil {
			ctx.Log.Warn("礼盒内容物入库失败", "item", it.ItemID, "err", gerr)
			return fail("礼盒打开未完成，请重试")
		}
		granted = append(granted, out)
	}
	if err := tx.Commit(); err != nil {
		ctx.Log.Warn("礼盒事务提交失败", "item", row.ItemID, "err", err)
		return fail("礼盒打开未完成，请重试")
	}
	// ItemToTree 含嵌套写，禁止在事务内调用；实例树在提交后构建
	// （与 22/2 购买同一顺序）。单棵失败只降级该物品的即时显示，
	// 不回滚已提交的库存变更。
	added := make([]*proto.Value, 0, len(granted))
	for _, g := range granted {
		tree, terr := s.ItemToTree(cctx, g)
		if terr != nil {
			ctx.Log.Warn("礼盒内容物树构造失败", "item", g.ItemID, "err", terr)
			continue
		}
		added = append(added, tree)
	}
	body, err := proto.EncodeValue(buildRigSnapshot800(added, nil, []string{guid}), ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}
