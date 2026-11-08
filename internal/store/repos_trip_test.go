package store

import (
	"context"
	"testing"
)

func TestExpireTripAccessTxRejectsConcurrentRepurchase(t *testing.T) {
	db := openIntegrityTestDB(t)
	ctx := context.Background()
	const uid int64 = 7711
	const place = "level_018_moskitnoe"
	expected := TripState{UID: uid, Place: place, RemainingSec: 10, LastConsumeTS: 100, Purchases: 1}
	if err := db.UpsertTripState(ctx, expected); err != nil {
		t.Fatal(err)
	}
	if err := db.PlaceAccessInsert(ctx, uid, place, "trip"); err != nil {
		t.Fatal(err)
	}

	// Simulate a purchase committing after consumeTripTimes loaded the old state.
	repurchased := expected
	repurchased.RemainingSec = 18000
	repurchased.LastConsumeTS = 200
	repurchased.Purchases = 2
	if err := db.UpsertTripState(ctx, repurchased); err != nil {
		t.Fatal(err)
	}
	expired, err := db.ExpireTripAccessTx(ctx, expected, 300)
	if err != nil {
		t.Fatal(err)
	}
	if expired {
		t.Fatal("stale expiry must not overwrite a concurrent repurchase")
	}
	got, err := db.GetTripState(ctx, uid, place)
	if err != nil || got == nil || got.RemainingSec != repurchased.RemainingSec || got.Purchases != repurchased.Purchases {
		t.Fatalf("repurchase state was overwritten: got=%+v err=%v", got, err)
	}
	var access int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM place_access WHERE uid=? AND place=? AND kind='trip'`, uid, place).Scan(&access); err != nil || access != 1 {
		t.Fatalf("repurchase access changed: count=%d err=%v", access, err)
	}

	// A fresh state snapshot may expire normally.
	expired, err = db.ExpireTripAccessTx(ctx, *got, 300)
	if err != nil || !expired {
		t.Fatalf("fresh expiry failed: expired=%v err=%v", expired, err)
	}
	got, err = db.GetTripState(ctx, uid, place)
	if err != nil || got == nil || got.RemainingSec != 0 {
		t.Fatalf("expired state not cleared: got=%+v err=%v", got, err)
	}
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM place_access WHERE uid=? AND place=? AND kind='trip'`, uid, place).Scan(&access); err != nil || access != 0 {
		t.Fatalf("expired access not removed: count=%d err=%v", access, err)
	}
}
