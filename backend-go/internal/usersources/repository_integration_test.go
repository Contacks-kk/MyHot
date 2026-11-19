package usersources

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/account"
	"aihot/backend-go/internal/config"
	"aihot/backend-go/internal/sourceschedule"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func userSourcesDB(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	raw := os.Getenv("MYHOT_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("MYHOT_TEST_DATABASE_URL is not configured")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") || (!strings.HasSuffix(parsed.Path, "_test") && !strings.HasSuffix(parsed.Path, "_ci")) {
		t.Fatal("database must be loopback and end with _test or _ci")
	}
	db, err := gorm.Open(postgres.Open(raw), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db, fmt.Sprintf("user_source_%d", time.Now().UnixNano())
}

func TestSubscriptionsShareSourceButKeepUserSettingsSeparate(t *testing.T) {
	db, prefix := userSourcesDB(t)
	ctx := context.Background()
	emails := []string{prefix + "_a@example.invalid", prefix + "_b@example.invalid"}
	ids := make([]int64, 2)
	for i, email := range emails {
		if err := db.WithContext(ctx).Raw(`INSERT INTO user_accounts(email,password_hash) VALUES(?,'fixture') RETURNING id`, email).Row().Scan(&ids[i]); err != nil {
			t.Fatal(err)
		}
	}
	var sourceIDs []string
	t.Cleanup(func() {
		_ = db.WithContext(ctx).Exec(`DELETE FROM user_accounts WHERE id IN ?`, ids).Error
		for _, id := range sourceIDs {
			_ = db.WithContext(ctx).Exec(`DELETE FROM sources WHERE id=?`, id).Error
		}
	})
	service := NewService(db, config.SourceSchedule{})
	queued := make(chan string, 4)
	service.SetCollectorEnabled(true)
	service.SetInitialEnqueuer(func(_ context.Context, sourceID string) error {
		queued <- sourceID
		return nil
	})
	sharedURL := "https://example.invalid/" + prefix + ".xml"
	var a, b Subscription
	var firstErr, secondErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		a, firstErr = service.Create(ctx, ids[0], Input{Name: "My feed", URL: sharedURL, IntervalMinutes: 60, ReuseWindowMinutes: 10})
	}()
	b, secondErr = service.Create(ctx, ids[1], Input{Name: "Another label", URL: sharedURL, IntervalMinutes: 30, ReuseWindowMinutes: 5})
	<-done
	sourceIDs = append(sourceIDs, a.SourceID, b.SourceID)
	if firstErr != nil || secondErr != nil {
		t.Fatalf("concurrent creation failed: %v, %v", firstErr, secondErr)
	}
	if a.SourceID != b.SourceID || a.ID == b.ID {
		t.Fatalf("subscriptions did not share the source row independently: a=%#v b=%#v", a, b)
	}
	if len(queued) != 1 || <-queued != a.SourceID {
		t.Fatal("expected one initial fetch for the shared source")
	}
	var coldA, coldB bool
	if err := db.WithContext(ctx).Raw(`SELECT cold_start_pending FROM user_source_subscriptions WHERE id=?`, a.ID).Row().Scan(&coldA); err != nil || !coldA {
		t.Fatalf("new subscription cold_start_pending = %t, %v", coldA, err)
	}
	if err := db.WithContext(ctx).Raw(`SELECT cold_start_pending FROM user_source_subscriptions WHERE id=?`, b.ID).Row().Scan(&coldB); err != nil || !coldB {
		t.Fatalf("second subscriber cold_start_pending = %t, %v", coldB, err)
	}
	if err := db.WithContext(ctx).Exec(`UPDATE user_source_subscriptions SET cold_start_pending=false WHERE id IN (?,?)`, a.ID, b.ID).Error; err != nil {
		t.Fatal(err)
	}
	var activatedAt time.Time
	if err := db.WithContext(ctx).Raw(`SELECT activated_at FROM user_source_subscriptions WHERE id=?`, a.ID).Row().Scan(&activatedAt); err != nil {
		t.Fatal(err)
	}
	repeated, err := service.Create(ctx, ids[0], Input{Name: "My feed", URL: sharedURL})
	if err != nil || repeated.ID != a.ID || !repeated.UpdatedExisting || repeated.NameInherited {
		t.Fatalf("repeat create failed: %#v, %v", repeated, err)
	}
	var repeatedAt time.Time
	if err := db.WithContext(ctx).Raw(`SELECT activated_at,cold_start_pending FROM user_source_subscriptions WHERE id=?`, a.ID).Row().Scan(&repeatedAt, &coldA); err != nil || coldA || !repeatedAt.Equal(activatedAt) {
		t.Fatalf("repeat create reset activation or cold start: %v %t %v", repeatedAt, coldA, err)
	}
	if a.Name != "My feed" || b.Name != "Another label" || b.EffectiveIntervalMinutes != 30 || b.EffectiveReuseWindowMinutes != 5 {
		t.Fatalf("user labels/effective cadence were not isolated and aggregated: a=%#v b=%#v", a, b)
	}
	list, err := service.List(ctx, ids[0])
	if err != nil || len(list) != 1 || list[0].Name != "My feed" || list[0].EffectiveIntervalMinutes != 30 || list[0].EffectiveReuseWindowMinutes != 5 {
		t.Fatalf("first user's list = %#v, %v", list, err)
	}
	changed, err := service.Update(ctx, ids[0], a.ID, Input{Name: "Moved feed", URL: "https://example.invalid/" + prefix + "-other.xml", IntervalMinutes: 60, ReuseWindowMinutes: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).Raw(`SELECT cold_start_pending FROM user_source_subscriptions WHERE id=?`, changed.ID).Row().Scan(&coldA); err != nil || !coldA {
		t.Fatalf("source-changed subscription did not re-enable cold_start_pending: %t, %v", coldA, err)
	}
	sourceIDs = append(sourceIDs, changed.SourceID)
	stillShared, err := service.Get(ctx, ids[1], b.ID)
	if err != nil || stillShared.SourceID != b.SourceID || stillShared.Name != "Another label" {
		t.Fatalf("changing one user's URL changed the other subscription: %#v, %v", stillShared, err)
	}
	duplicateURL := "https://example.invalid/" + prefix + "-duplicate.xml"
	duplicate, err := service.Create(ctx, ids[0], Input{Name: "Duplicate URL", URL: duplicateURL})
	if err != nil {
		t.Fatal(err)
	}
	sourceIDs = append(sourceIDs, duplicate.SourceID)
	if _, err := service.Update(ctx, ids[0], changed.ID, Input{Name: changed.Name, URL: duplicateURL, IntervalMinutes: changed.IntervalMinutes, ReuseWindowMinutes: changed.ReuseWindowMinutes}); !errors.Is(err, ErrAlreadySubscribed) {
		t.Fatalf("editing to another one of the user's subscriptions should return conflict: %v", err)
	}
	paused := false
	if _, err := service.SetEnabled(ctx, ids[1], b.ID, paused); err != nil {
		t.Fatal(err)
	}
	var enabled bool
	if err := db.WithContext(ctx).Raw(`SELECT enabled FROM sources WHERE id=?`, b.SourceID).Row().Scan(&enabled); err != nil || enabled {
		t.Fatalf("source without active subscriptions remains enabled: %v, %v", enabled, err)
	}
	if err := service.Delete(ctx, ids[1], b.ID); err != nil {
		t.Fatal(err)
	}
	var preserved int
	if err := db.WithContext(ctx).Raw(`SELECT count(*) FROM sources WHERE id=?`, b.SourceID).Row().Scan(&preserved); err != nil || preserved != 1 {
		t.Fatalf("deleting subscription removed archived source: count=%d err=%v", preserved, err)
	}
}

func TestPlatformScheduleReconcilesExistingSources(t *testing.T) {
	db, prefix := userSourcesDB(t)
	ctx := context.Background()
	var userID int64
	if err := db.WithContext(ctx).Raw(`INSERT INTO user_accounts(email,password_hash) VALUES(?,'fixture') RETURNING id`,
		prefix+"_schedule@example.invalid").Row().Scan(&userID); err != nil {
		t.Fatal(err)
	}
	var sourceID string
	t.Cleanup(func() {
		_ = db.WithContext(ctx).Exec(`DELETE FROM user_accounts WHERE id=?`, userID).Error
		if sourceID != "" {
			_ = db.WithContext(ctx).Exec(`DELETE FROM sources WHERE id=?`, sourceID).Error
		}
	})
	saved, err := NewService(db, config.SourceSchedule{}).Create(ctx, userID, Input{
		Name:               "Existing feed",
		URL:                "https://example.invalid/" + prefix + "-schedule.xml",
		IntervalMinutes:    5,
		ReuseWindowMinutes: 45,
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceID = saved.SourceID
	if saved.EffectiveIntervalMinutes != 45 {
		t.Fatalf("original interval = %d", saved.EffectiveIntervalMinutes)
	}
	if err := db.WithContext(ctx).Exec(`UPDATE sources SET last_ok_at=now()-interval '1 minute',
		next_fetch_at=now()+interval '44 minutes' WHERE id=?`, sourceID).Error; err != nil {
		t.Fatal(err)
	}
	policy := config.SourceSchedule{
		AutoMinInterval:   15,
		ManualReuseWindow: 10,
		AutoReuseWindow:   10,
	}
	if err := sourceschedule.Reconcile(ctx, db, policy); err != nil {
		t.Fatal(err)
	}
	service := NewService(db, policy)
	got, err := service.Get(ctx, userID, saved.ID)
	if err != nil || got.IntervalMinutes != 5 || got.ReuseWindowMinutes != 45 ||
		got.EffectiveIntervalMinutes != 15 || got.EffectiveReuseWindowMinutes != 10 ||
		got.NextFetchAt == nil || got.LastCheckedAt == nil || got.NextFetchAt.Sub(*got.LastCheckedAt) != 15*time.Minute {
		t.Fatalf("reconciled schedule = %#v, %v", got, err)
	}
	if err := db.WithContext(ctx).Exec(`UPDATE sources SET next_fetch_at=now() WHERE id=?`, sourceID).Error; err != nil {
		t.Fatal(err)
	}
	if err := sourceschedule.Reconcile(ctx, db, policy); err != nil {
		t.Fatal(err)
	}
	var next *time.Time
	if err := db.WithContext(ctx).Raw(`SELECT next_fetch_at FROM sources WHERE id=?`, sourceID).Row().Scan(&next); err != nil || next == nil || !next.Equal(*got.NextFetchAt) {
		t.Fatalf("existing effective cycle did not restore the minimum: next=%v err=%v", next, err)
	}
	if err := account.NewRepository(db).DeleteAccount(ctx, userID, policy); err != nil {
		t.Fatal(err)
	}
	var enabled bool
	if err := db.WithContext(ctx).Raw(`SELECT enabled,next_fetch_at FROM sources WHERE id=?`, sourceID).Row().Scan(&enabled, &next); err != nil || enabled || next != nil {
		t.Fatalf("last account deletion left a scheduled source: enabled=%v next=%v err=%v", enabled, next, err)
	}
}

func TestSubscriptionReusesCanonicalizedLegacyRSSSource(t *testing.T) {
	db, prefix := userSourcesDB(t)
	ctx := context.Background()
	email := prefix + "_legacy@example.invalid"
	var userID int64
	if err := db.WithContext(ctx).Raw(`INSERT INTO user_accounts(email,password_hash) VALUES(?,'fixture') RETURNING id`, email).Row().Scan(&userID); err != nil {
		t.Fatal(err)
	}
	sourceID := prefix + "_legacy_source"
	legacyURL := "https://Example.invalid/feed?b=2&a=1"
	if err := db.WithContext(ctx).Exec(`INSERT INTO sources(id,name,kind,config,enabled,health) VALUES(?, 'Archived RSS', 'rss', jsonb_build_object('feedUrl', ?::text), false, 'paused')`, sourceID, legacyURL).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.WithContext(ctx).Exec(`DELETE FROM user_accounts WHERE id=?`, userID).Error
		_ = db.WithContext(ctx).Exec(`DELETE FROM sources WHERE id=?`, sourceID).Error
	})
	service := NewService(db, config.SourceSchedule{})
	got, err := service.Create(ctx, userID, Input{Name: "Reused archive", URL: "https://example.invalid/feed?a=1&b=2"})
	if err != nil {
		t.Fatal(err)
	}
	if got.SourceID != sourceID || got.IntervalMinutes != 60 || got.ReuseWindowMinutes != 10 {
		t.Fatalf("legacy RSS source was not reused with defaults: %#v", got)
	}
	var storedURL, identity string
	if err := db.WithContext(ctx).Raw(`SELECT config->>'feedUrl',collector_identity FROM sources WHERE id=?`, sourceID).Row().Scan(&storedURL, &identity); err != nil {
		t.Fatal(err)
	}
	if storedURL != legacyURL || identity == "" {
		t.Fatalf("reusing source changed its configured address or did not assign identity: url=%q identity=%q", storedURL, identity)
	}
}
