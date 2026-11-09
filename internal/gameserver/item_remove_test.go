package gameserver

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
	"time"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
)

func starterRodRemovalSetup(t *testing.T) (*Server, *store.DB, *Session, string, string) {
	t.Helper()
	srv, db := newBundleTestServer(t)
	t.Cleanup(func() { stopFishingRecoveryTimers(srv) })
	sess := newProbeSess(srv)
	ctx := context.Background()
	if err := srv.EnsurePlayer(ctx, sess.Userid); err != nil {
		t.Fatal(err)
	}
	var rod, line string
	if err := db.SQL.QueryRow(`SELECT guid FROM item WHERE uid=? AND item_id=?`, sess.Userid, "tele_10012_tl16").Scan(&rod); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL.QueryRow(`SELECT guid FROM item WHERE uid=? AND item_id=? LIMIT 1`, sess.Userid, "mono_15065").Scan(&line); err != nil {
		t.Fatal(err)
	}
	runRodSet(t, srv, sess, rod, line, 0)
	attached, err := db.GetItemByGUID(ctx, line)
	if err != nil || attached.RigGUID != rod || rigStoredGUID(t, attached, csParent) != rod {
		t.Fatalf("line not attached: row=%+v err=%v", attached, err)
	}
	st := srv.stateFor(sess.Userid)
	st.mu.Lock()
	st.GearGuid = rod
	st.RigType = 1
	st.DepthCM = 120
	st.mu.Unlock()
	if _, err := db.SQL.Exec(`INSERT INTO rod_setting(guid,key,value) VALUES(?,'test','120')`, rod); err != nil {
		t.Fatal(err)
	}
	return srv, db, sess, rod, line
}

func assertRemovedStarterLogin(t *testing.T, srv *Server, db *store.DB, uid int64, rod, line string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.GetItemByGUID(ctx, rod); err != sql.ErrNoRows {
		t.Fatalf("discarded rod must stay deleted: %v", err)
	}
	assertBackpackRow(t, db, line, rod)
	for _, query := range []string{
		`SELECT COUNT(*) FROM rig WHERE guid=?`,
		`SELECT COUNT(*) FROM rig_component WHERE rig_guid=?`,
		`SELECT COUNT(*) FROM container_slot WHERE guid=?`,
		`SELECT COUNT(*) FROM rod_setting WHERE guid=?`,
	} {
		var n int
		if err := db.SQL.QueryRow(query, rod).Scan(&n); err != nil || n != 0 {
			t.Errorf("stale reference: %s count=%d err=%v", query, n, err)
		}
	}
	st := srv.stateFor(uid)
	st.mu.Lock()
	gear := st.GearGuid
	st.mu.Unlock()
	if gear != "" {
		t.Errorf("held rod still references discarded item: %s", gear)
	}
	saved, err := db.LoadFishingState(ctx, uid)
	if err != nil || saved == nil || saved.GearGUID != "" || saved.RigType != 0 || saved.DepthCM != 0 ||
		saved.CastActive || saved.Hooked || !saved.Resolved || saved.Pending != nil || !saved.BiteAt.IsZero() {
		t.Errorf("invalid persisted fishing state: %+v err=%v", saved, err)
	}
	sess := newProbeSess(srv)
	sess.Userid = uid
	for _, tc := range []struct {
		name    string
		handler Handler
		args    []*proto.Value
	}{
		{"profile", hProfile, nil},
		{"shortcuts", hShortcutSnapshot, nil},
		{"inventory", hRigSyncBackpack, nil},
	} {
		body, ok := tc.handler(&Ctx{Sess: sess, Tree: rigRequestTree(t, tc.args...), DecodeOK: true, Log: logging.Discard()})
		if !ok {
			t.Fatalf("%s not handled", tc.name)
		}
		tree, _, err := proto.DecodeValue(body, 0, srv.Table())
		if err != nil {
			t.Fatalf("%s decode: %v", tc.name, err)
		}
		if itemRemovalTreeHasGUID(tree, rod) {
			t.Errorf("%s still sends discarded rod GUID", tc.name)
		}
	}
}

func itemRemovalTreeHasGUID(tree *proto.Value, guid string) bool {
	if tree == nil {
		return false
	}
	if tree.Hex == guid {
		return true
	}
	for _, item := range tree.Items {
		if itemRemovalTreeHasGUID(item, guid) {
			return true
		}
	}
	for _, field := range tree.Fields {
		if field.Hex == guid || itemRemovalTreeHasGUID(field.Val, guid) {
			return true
		}
	}
	return false
}

func TestItemRemoveStarterRodCanReloadProfile(t *testing.T) {
	t.Parallel()
	srv, db, sess, rod, line := starterRodRemovalSetup(t)
	st := srv.stateFor(sess.Userid)
	st.mu.Lock()
	st.CastActive, st.Hooked = true, true
	st.InWater, st.NeedBite = true, true
	st.Pending = &RolledFish{FishGUID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	st.pushing = st.Pending
	st.persist.recoverPending, st.persist.recoverCompleted = true, true
	srv.armBiteTimerLocked(sess.Userid, st, time.Now().Add(time.Hour))
	srv.armPendingTimerLocked(sess.Userid, st, time.Hour)
	generation, biteTicket, pendingTicket := st.castGeneration, st.biteTicket, st.pendingTicket
	st.mu.Unlock()
	capture := &ctxReplyCapture{}
	body, ok := hItemRemove(&Ctx{Sess: sess, Main: 4, Sub: 7, Tree: rigRequestTree(t, proto.Guid(rod)),
		DecodeOK: true, ExpectResponse: true, replyCapture: capture, Log: logging.Discard()})
	if _, _, _, failure := capture.snapshot(); !ok || len(body) == 0 || failure {
		t.Fatal("discard failed")
	}
	st.mu.Lock()
	stale := st.InWater || st.NeedBite || st.biteTimer != nil || st.pendingTimer != nil || st.pushing != nil ||
		st.persist.recoverPending || st.persist.recoverCompleted || st.castGeneration <= generation ||
		st.biteTicket <= biteTicket || st.pendingTicket <= pendingTicket
	st.mu.Unlock()
	if stale {
		t.Fatal("discard left active cast callbacks or recovery flags")
	}
	assertRemovedStarterLogin(t, srv, db, sess.Userid, rod, line)
	restarted := newFishingRecoveryServer(t, db)
	if err := restarted.EnsurePlayer(context.Background(), sess.Userid); err != nil {
		t.Fatal(err)
	}
	assertRemovedStarterLogin(t, restarted, db, sess.Userid, rod, line)
}

func TestEnsurePlayerRepairsPreviouslyDiscardedStarterRod(t *testing.T) {
	t.Parallel()
	_, db, sess, rod, line := starterRodRemovalSetup(t)
	if err := db.DeleteItems(context.Background(), []string{rod}); err != nil {
		t.Fatal(err)
	}
	restarted := newFishingRecoveryServer(t, db)
	var repaired *store.Item
	for i := 0; i < 2; i++ {
		if err := restarted.EnsurePlayer(context.Background(), sess.Userid); err != nil {
			t.Fatal(err)
		}
		assertRemovedStarterLogin(t, restarted, db, sess.Userid, rod, line)
		row, err := db.GetItemByGUID(context.Background(), line)
		if err != nil {
			t.Fatal(err)
		}
		if repaired != nil && *row != *repaired {
			t.Fatal("repeated login changed repaired attachment")
		}
		repaired = row
		var rods int
		if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM item WHERE uid=? AND item_id='tele_10012_tl16'`, sess.Userid).Scan(&rods); err != nil || rods != 0 {
			t.Fatalf("login granted a replacement starter rod: count=%d err=%v", rods, err)
		}
	}
}

func TestItemRemoveRodFailureRollsBackInventoryAndFishingState(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"attachment", "fishing_state"} {
		t.Run(stage, func(t *testing.T) {
			assertItemRemoveRodFailureRollsBack(t, stage)
		})
	}
}

func assertItemRemoveRodFailureRollsBack(t *testing.T, stage string) {
	t.Helper()
	srv, db, sess, rod, line := starterRodRemovalSetup(t)
	fish, _ := seedImportedKeepnetFish(t, srv, sess.Userid, 7101, true)
	st := srv.fishingState(sess.Userid)
	st.mu.Lock()
	st.CastActive, st.Hooked, st.InWater, st.NeedBite = true, true, true, true
	st.Pending = &RolledFish{FishGUID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	st.Keepnet = []*RolledFish{{FishGUID: fish.EchoGUID}}
	st.pushing = st.Pending
	st.persist.recoverPending, st.persist.recoverCompleted = true, true
	srv.armBiteTimerLocked(sess.Userid, st, time.Now().Add(time.Hour))
	srv.armPendingTimerLocked(sess.Userid, st, time.Hour)
	biteTimer, pendingTimer := st.biteTimer, st.pendingTimer
	generation, biteTicket, pendingTicket := st.castGeneration, st.biteTicket, st.pendingTicket
	st.mu.Unlock()
	before, err := db.ListItemsByContainers(context.Background(), sess.Userid, []string{"backpack", "rig", "keepnet"})
	if err != nil {
		t.Fatal(err)
	}
	savedBefore, err := db.LoadFishingState(context.Background(), sess.Userid)
	if err != nil {
		t.Fatal(err)
	}
	referenceCounts := make(map[string]int)
	for _, query := range []string{`SELECT COUNT(*) FROM rig WHERE guid=?`, `SELECT COUNT(*) FROM rig_component WHERE rig_guid=?`, `SELECT COUNT(*) FROM rod_setting WHERE guid=?`} {
		var count int
		if err := db.SQL.QueryRow(query, rod).Scan(&count); err != nil {
			t.Fatal(err)
		}
		referenceCounts[query] = count
	}
	query := `CREATE TRIGGER reject_attachment_return BEFORE UPDATE OF container ON item
		WHEN OLD.container='rig' AND NEW.container='backpack'
		BEGIN SELECT RAISE(ABORT, 'test attachment failure'); END`
	if stage == "fishing_state" {
		query = `CREATE TRIGGER reject_fishing_reset BEFORE UPDATE ON fishing_state
			BEGIN SELECT RAISE(ABORT, 'test fishing state failure'); END`
	}
	if _, err := db.SQL.Exec(query); err != nil {
		t.Fatal(err)
	}
	capture := &ctxReplyCapture{}
	body, ok := hItemRemove(&Ctx{Sess: sess, Tree: rigRequestTree(t, proto.Guid(fish.EchoGUID), proto.Guid(rod)),
		DecodeOK: true, ExpectResponse: true, replyCapture: capture, Log: logging.Discard()})
	if _, _, _, failure := capture.snapshot(); !ok || len(body) != 0 || !failure {
		t.Fatal("failed transaction must return failure, not a successful snapshot")
	}
	if _, err := db.GetItemByGUID(context.Background(), rod); err != nil {
		t.Fatalf("rod deletion was not rolled back: %v", err)
	}
	row, err := db.GetItemByGUID(context.Background(), line)
	if err != nil || row.RigGUID != rod || row.Container != "rig" || rigStoredGUID(t, row, csParent) != rod {
		t.Fatalf("attachment changed on rollback: row=%+v err=%v", row, err)
	}
	shortcuts, err := db.ListShortcuts(context.Background(), sess.Userid)
	if err != nil || len(shortcuts) != 4 {
		t.Fatalf("shortcuts changed on rollback: %+v err=%v", shortcuts, err)
	}
	after, err := db.ListItemsByContainers(context.Background(), sess.Userid, []string{"backpack", "rig", "keepnet"})
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("failed removal changed inventory: err=%v", err)
	}
	savedAfter, err := db.LoadFishingState(context.Background(), sess.Userid)
	if err != nil || !reflect.DeepEqual(savedBefore, savedAfter) {
		t.Fatalf("failed removal changed saved fishing state: before=%+v after=%+v err=%v", savedBefore, savedAfter, err)
	}
	if _, err := db.KeepnetGet(context.Background(), sess.Userid, fish.EchoGUID); err != nil {
		t.Fatalf("failed rod repair committed fish release: %v", err)
	}
	for query, want := range referenceCounts {
		var count int
		if err := db.SQL.QueryRow(query, rod).Scan(&count); err != nil || count != want {
			t.Fatalf("failed removal changed rig references: %s count=%d want=%d err=%v", query, count, want, err)
		}
	}
	st.mu.Lock()
	unchanged := st.GearGuid == rod && st.CastActive && st.Hooked && st.InWater && st.NeedBite &&
		st.Pending != nil && st.pushing == st.Pending && len(st.Keepnet) == 1 && st.Keepnet[0].FishGUID == fish.EchoGUID &&
		st.biteTimer == biteTimer && st.pendingTimer == pendingTimer && st.castGeneration == generation &&
		st.biteTicket == biteTicket && st.pendingTicket == pendingTicket && st.persist.recoverPending && st.persist.recoverCompleted
	st.mu.Unlock()
	if !unchanged {
		t.Fatal("failed discard changed online fishing state or timers")
	}
}

func TestItemRemoveRepairPreservesValidRigAndOtherPlayer(t *testing.T) {
	t.Parallel()
	srv, db, sess, rod, line := starterRodRemovalSetup(t)
	ctx := context.Background()
	var healthyRod, healthyLine string
	if err := db.SQL.QueryRow(`SELECT guid FROM item WHERE uid=? AND item_id='spin_10001_S60L'`, sess.Userid).Scan(&healthyRod); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL.QueryRow(`SELECT guid FROM item WHERE uid=? AND item_id='mono_15065' AND guid<>?`, sess.Userid, line).Scan(&healthyLine); err != nil {
		t.Fatal(err)
	}
	runRodSet(t, srv, sess, healthyRod, healthyLine, 0)
	before, err := db.GetItemByGUID(ctx, healthyLine)
	if err != nil || before.RigGUID != healthyRod {
		t.Fatalf("healthy rig setup: %v %v", before, err)
	}
	healthyRodBefore, err := db.GetItemByGUID(ctx, healthyRod)
	if err != nil {
		t.Fatal(err)
	}
	other, err := srv.CreateItem(ctx, sess.Userid+1, "mono_15065", "rig", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateItemFields(ctx, other.GUID, other.FieldsJSON, other.Qty, other.Durability, other.Workmanship, rod); err != nil {
		t.Fatal(err)
	}
	const foreignMissingRod = "cccccccccccccccccccccccccccccccc"
	if err := db.UpsertRig(ctx, foreignMissingRod, sess.Userid+1, 1, "", "{}"); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertRigComponent(ctx, foreignMissingRod, 11, other.GUID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO rod_setting(guid,key,value) VALUES(?,'test','42')`, foreignMissingRod); err != nil {
		t.Fatal(err)
	}
	foreignFishing := &store.FishingState{UID: sess.Userid + 1, RodGUID: foreignMissingRod, GearGUID: foreignMissingRod, CastActive: true,
		Pending: &store.FishingFish{FishGUID: "dddddddddddddddddddddddddddddddd"}}
	if err := db.SaveFishingState(ctx, foreignFishing); err != nil {
		t.Fatal(err)
	}
	foreignBefore, err := db.GetItemByGUID(ctx, other.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := hItemRemove(&Ctx{Sess: sess, Tree: rigRequestTree(t, proto.Guid(rod), proto.Guid(rod), proto.Guid(other.GUID)),
		DecodeOK: true, Log: logging.Discard()}); !ok {
		t.Fatal("discard not handled")
	}
	after, err := db.GetItemByGUID(ctx, healthyLine)
	if err != nil || *before != *after {
		t.Fatalf("valid attachment changed: before=%+v after=%+v err=%v", before, after, err)
	}
	healthyRodAfter, err := db.GetItemByGUID(ctx, healthyRod)
	if err != nil || *healthyRodBefore != *healthyRodAfter {
		t.Fatalf("valid rod changed: err=%v", err)
	}
	untouched, err := db.GetItemByGUID(ctx, other.GUID)
	if err != nil || *untouched != *foreignBefore {
		t.Fatalf("another player's attachment changed: row=%+v err=%v", untouched, err)
	}
	foreignAfter, err := db.LoadFishingState(ctx, sess.Userid+1)
	if err != nil || !reflect.DeepEqual(foreignFishing, foreignAfter) {
		t.Fatalf("another player's fishing state changed: got=%+v err=%v", foreignAfter, err)
	}
	for _, query := range []string{`SELECT COUNT(*) FROM rig WHERE guid=?`, `SELECT COUNT(*) FROM rig_component WHERE rig_guid=?`, `SELECT COUNT(*) FROM rod_setting WHERE guid=?`} {
		var count int
		if err := db.SQL.QueryRow(query, foreignMissingRod).Scan(&count); err != nil || count != 1 {
			t.Fatalf("another player's orphan rig changed: %s count=%d err=%v", query, count, err)
		}
	}
	assertRemovedStarterLogin(t, srv, db, sess.Userid, rod, line)
}

func TestItemRemoveRepairKeepsFishReleaseOwnershipAndCache(t *testing.T) {
	t.Parallel()
	srv, db := newBundleTestServer(t)
	ctx := context.Background()
	const uid int64 = 7357
	imported, _ := seedImportedKeepnetFish(t, srv, uid, 7201, true)
	fresh, _ := seedImportedKeepnetFish(t, srv, uid, 7202, false)
	remaining, _ := seedImportedKeepnetFish(t, srv, uid, 7203, true)
	foreign, _ := seedImportedKeepnetFish(t, srv, uid+1, 7204, true)
	st := srv.fishingState(uid)
	st.mu.Lock()
	st.Keepnet = []*RolledFish{{FishGUID: imported.EchoGUID}, {FishGUID: fresh.EchoGUID}, {FishGUID: remaining.EchoGUID}}
	st.mu.Unlock()
	for range 2 {
		body, handled := hItemRemove(keepnetTestCtx(srv, uid, proto.U32(5),
			proto.GuidArr([]string{imported.EchoGUID, fresh.EchoGUID, imported.EchoGUID, foreign.EchoGUID})))
		if !handled || len(body) == 0 {
			t.Fatal("fish release did not return a snapshot")
		}
		kept, err := db.KeepnetList(ctx, uid)
		if err != nil || len(kept) != 1 || kept[0].EchoGUID != remaining.EchoGUID {
			t.Fatalf("released fish remained or wrong fish removed: %+v err=%v", kept, err)
		}
		if _, err := db.GetItemByGUID(ctx, imported.EchoGUID); err != sql.ErrNoRows {
			t.Fatalf("released imported snapshot remained: %v", err)
		}
		if _, err := db.KeepnetGet(ctx, uid+1, foreign.EchoGUID); err != nil {
			t.Fatalf("another player's fish changed: %v", err)
		}
		if _, err := db.GetItemByGUID(ctx, foreign.EchoGUID); err != nil {
			t.Fatalf("another player's snapshot changed: %v", err)
		}
		st.mu.Lock()
		valid := len(st.Keepnet) == 1 && st.Keepnet[0].FishGUID == remaining.EchoGUID
		st.mu.Unlock()
		if !valid {
			t.Fatal("release did not update only removed fish in online keepnet")
		}
	}
}
