package gameserver

import (
	"context"
	"math"
	"math/rand"
	"strings"
	"testing"
	"time"

	"rf4server/internal/proto"
)

func TestFishQueryMatchesProduction(t *testing.T) {
	t.Setenv("GODEBUG", "randseednop=0")
	for _, fallback := range []bool{false, true} {
		for _, rules := range []string{"0", "1"} {
			name := "database/"
			if fallback {
				name = "pool/"
			}
			t.Run(name+rules, func(t *testing.T) {
				rand.Seed(271828)
				s, db := seedBaitWeightTest(t)
				for key, value := range map[string]string{"fish_bait_rules_enabled": rules, "fish_weather_enabled": rules, "fish_weight_scale": "1.25", "bite_probability": "0.6"} {
					if err := db.TunableSet(t.Context(), key, value, "test"); err != nil {
						t.Fatal(err)
					}
				}
				s.execAdminCmd(&AdminCommand{Op: "reload_tunables"})
				loc := "level_900_rules"
				if fallback {
					loc = "query-fallback"
				}
				bonus := neutralFishingSkillBonus()
				bonus.TrophyLuck = .35
				c := fishRollContext{loc: loc, now: time.Unix(1794096000, 0), cache: newFishingRollCache(), forRig: true, rigType: 1, depthCM: 150, happyBite: .05,
					weather: fishingWeather{known: true, kind: 1, airTemp: 18, previousAirTemp: 18, wind: 2}}
				c.skill = bonus
				q := &fishQueryCapture{fish: "c.roach", grade: 2}
				inspect := c
				inspect.query = q
				if fish := s.rollFishWithRules(loc, "rule_worm", .8, bonus, inspect); fish != nil {
					t.Fatal("inspection generated a fish")
				}
				const attempts = 4000
				matched := 0
				for range attempts {
					fish := s.rollFishWithRules(loc, "rule_worm", .8, bonus, c)
					if fish != nil && fish.Species == q.fish && fish.Grade >= q.grade {
						matched++
					}
				}
				observed := .6 * float64(matched) / attempts
				if q.chance <= 0 || math.Abs(observed-q.chance) > .025 {
					t.Fatalf("estimated=%g observed=%g selection=%g acceptance=%g grade=%g", q.chance, observed, q.selection, q.acceptance, q.gradeShare)
				}
				t.Logf("estimated=%.4f sampled=%.4f", q.chance, observed)
			})
		}
	}
}

func TestFishQueryCaptureUsesWeightsAndBiteGate(t *testing.T) {
	s, db := seedBaitWeightTest(t)
	if err := db.TunableSet(t.Context(), "bite_probability", "0.5", "test"); err != nil {
		t.Fatal(err)
	}
	q := &fishQueryCapture{fish: "c.roach", grade: 1}
	c := fishRollContext{query: q, cache: newFishingRollCache(), forRig: true}
	law := fishWeightLaw{lower: 1, upper: 10000, scale: 1, mass: func(lo, hi float64) float64 { return hi - lo }}
	s.captureFishQuery([]string{"c.roach", "frog"}, []float64{3, 1}, map[string]float64{"c.roach": .4, "frog": .8}, true, neutralFishingSkillBonus(), c, true, func(string) fishWeightLaw { return law })
	if math.Abs(q.selection-.75) > 1e-12 || math.Abs(q.acceptance-.4) > 1e-12 || math.Abs(q.chance-.15) > 1e-12 {
		t.Fatalf("wrong weighted bite probability: %+v", q)
	}
	if math.Abs(q.fishShare-.6) > 1e-12 {
		t.Fatalf("wrong share among successful bites: %g", q.fishShare)
	}
}

func TestFishQueryReadOnlyAndCatalog(t *testing.T) {
	t.Setenv("GODEBUG", "randseednop=0")
	s, db := seedBaitWeightTest(t)
	const uid int64 = 1000001
	const mapKey = "level_015_tunguska"
	if _, err := db.SQL.Exec(`UPDATE loc_alias SET alias=? WHERE alias='level_900_rules'`, mapKey); err != nil {
		t.Fatal(err)
	}
	if _, err := db.EnsureProfile(t.Context(), uid); err != nil {
		t.Fatal(err)
	}
	if err := db.SetPlayerLocation(t.Context(), uid, mapKey, mapKey, 11); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO spot_fish(loc,spot,fish_key,catches) VALUES('rules-test','67:57','c.roach',5)`); err != nil {
		t.Fatal(err)
	}
	// query_only rejects writes even when they would be hidden inside a helper.
	db.SQL.SetMaxOpenConns(1)
	if _, err := db.SQL.Exec(`PRAGMA query_only=ON`); err != nil {
		t.Fatal(err)
	}
	rand.Seed(314159)
	wantNext := rand.Int63()
	rand.Seed(314159)
	req := FishQueryRequest{UID: uid, Fish: "c.roach", Map: mapKey, Grade: 3}
	result, err := s.QueryFish(t.Context(), req)
	if err != nil || len(result.Plans) == 0 {
		t.Fatalf("query=%+v err=%v", result, err)
	}
	if rand.Int63() != wantNext {
		t.Fatal("query consumed production random numbers")
	}
	for _, plan := range result.Plans {
		if plan.Map != req.Map || plan.Chance <= 0 || plan.Chance > 1 || plan.CoordinateKind != "region_uniform" || plan.X != nil || plan.Region != nil || len(plan.References) != 0 {
			t.Fatalf("invalid plan or invented world coordinates: %+v", plan)
		}
	}
	if result.Maps != 1 || result.BaitGroups > 48 || result.Evaluated > 96 || result.CandidateRegions != 0 {
		t.Fatalf("query exceeded candidate bounds: %+v", result)
	}
	if key, err := s.resolveFishQuerySpecies(t.Context(), "常见拟鲤"); err != nil || key != "c.roach" {
		t.Fatalf("species=%s err=%v", key, err)
	}
	if key, err := s.resolveFishQueryMap(t.Context(), "rules-test"); err != nil || key != req.Map {
		t.Fatalf("map=%s err=%v", key, err)
	}
	if _, err := s.resolveFishQuerySpecies(t.Context(), "%"); err == nil {
		t.Fatal("punctuation matched a fish")
	}
	if reply := s.fishQueryReply(t.Context(), &Session{Srv: s, Userid: uid}, "常见拟鲤 "+mapKey); !strings.Contains(reply, "rules-test") || !strings.Contains(reply, "鱼群分布未开启") || strings.Contains(reply, "67:57") {
		t.Fatalf("reply=%s", reply)
	}
	req.Map = ""
	if _, err := s.QueryFish(t.Context(), req); err == nil {
		t.Fatal("query accepted an unbounded map search")
	}
	req.Map, req.Fish = mapKey, "absent-fish"
	if empty, err := s.QueryFish(t.Context(), req); err != nil || len(empty.Plans) != 0 {
		t.Fatalf("absent fish=%+v err=%v", empty, err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.QueryFish(cancelled, req); err == nil {
		t.Fatal("cancelled query did not stop")
	}
}

func TestFishQueryContactAndMailReplay(t *testing.T) {
	s, db := newProgressSrv(t)
	const uid int64 = 1000001
	if _, err := db.EnsureProfile(t.Context(), uid); err != nil {
		t.Fatal(err)
	}
	first := newMailTestSession(t, s, 1, uid)
	second := newMailTestSession(t, s, 2, uid+1)
	first.sess.Token = "fish-query-test"
	env := &dlineTestEnv{srv: s, db: db, sess: first.sess}
	members := env.invoke(t, 24, 4, proto.Args524(proto.U32(0), proto.StrVal("")))
	if members.Kind != proto.KindU32Arr || len(members.Items) != 4 || members.Items[0].Num != uint64(fishQueryContactUID) || members.Items[1].Num != uint64(gearAssistantUID) {
		t.Fatalf("contact missing from members: %+v", members)
	}
	card := decodeWrappedBytes(t, env.invoke(t, 24, 19, proto.Args524(proto.U32(uint32(fishQueryContactUID)))))
	if card.TypeID != 2002 || card.Fields[1].StrOr() != fishQueryContactName || card.Fields[3].U32() != 0 {
		t.Fatalf("invalid contact card: %+v", card)
	}
	body, err := proto.EncodeValue(proto.Args524(proto.U32(uint32(fishQueryContactUID)), proto.StrVal("帮助")), s.Table())
	if err != nil {
		t.Fatal(err)
	}
	send := func() {
		s.handleEnvelopePayload(first.sess, proto.BuildRequestEnvelope(601, 19, 1, append(append([]byte(nil), body...), 0)))
		select {
		case response := <-first.envelopes:
			if response.Marker != -2 || response.CallID != 601 || len(response.Body) != 1 || response.Body[0] != 0 {
				t.Fatalf("message acknowledgement must precede reply: %+v", response)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no message acknowledgement")
		}
	}
	send()
	mails, err := db.MailList(t.Context(), uid, false)
	if err != nil || len(mails) != 1 || mails[0].Sender != fishQueryContactUID || mails[0].Text != fishQueryHelp {
		t.Fatalf("query reply not persisted: mails=%+v err=%v", mails, err)
	}
	assertMailTestNotification(t, first, mails[0])
	assertNoMailTestPush(t, second)
	send()
	assertNoMailTestPush(t, first)
	mails, err = db.MailList(t.Context(), uid, false)
	if err != nil || len(mails) != 1 {
		t.Fatalf("replay created another reply: mails=%+v err=%v", mails, err)
	}
	var profiles int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM profile WHERE uid=?`, fishQueryContactUID).Scan(&profiles); err != nil || profiles != 0 {
		t.Fatalf("virtual contact created a player account: count=%d err=%v", profiles, err)
	}
}

func TestFishQueryContactDoesNotTakeItems(t *testing.T) {
	env := newDLineTestEnv(t, 1000001)
	// A valid owned item would be transferred by the regular player-mail path.
	seedEconomyItemDef(t, env.db, 9451, "query_mail_item", 27)
	item := economyTestItem(t, env.srv, env.sess.Userid, "query_mail_item", "backpack", 3)
	env.invoke(t, 19, 2, proto.Args524(proto.U32(uint32(fishQueryContactUID)), proto.U32(1), proto.Guid(item.GUID)))
	var qty int
	if err := env.db.SQL.QueryRow(`SELECT qty FROM item WHERE guid=? AND uid=?`, item.GUID, env.sess.Userid).Scan(&qty); err != nil || qty != 3 {
		t.Fatalf("assistant took player items: qty=%d err=%v", qty, err)
	}
}
