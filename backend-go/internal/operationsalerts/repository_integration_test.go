package operationsalerts

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openIntegrationRepository(t *testing.T) *gorm.DB {
	t.Helper()
	raw := os.Getenv("MYHOT_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("MYHOT_TEST_DATABASE_URL is not configured")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") || !(strings.HasSuffix(parsed.Path, "_test") || strings.HasSuffix(parsed.Path, "_ci")) {
		t.Fatal("integration database must be localhost and end in _test or _ci")
	}
	orm, err := gorm.Open(postgres.Open(raw), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := orm.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(2)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return orm
}

func TestRepositoryFindingsReadMigratedTestSchema(t *testing.T) {
	orm := openIntegrationRepository(t)
	findings, err := NewRepository(orm).Findings(context.Background(), time.Now().UTC(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range findings {
		if finding.Key == "" || finding.Title == "" || (finding.Level != LevelNow && finding.Level != LevelToday) {
			t.Fatalf("invalid finding from migrated database: %+v", finding)
		}
	}
}

func TestRepositoryAlertStateRoundTripWithinTransaction(t *testing.T) {
	orm := openIntegrationRepository(t).Begin()
	if orm.Error != nil {
		t.Fatal(orm.Error)
	}
	t.Cleanup(func() { _ = orm.Rollback().Error })
	repository := NewRepository(orm)
	want := State{
		"integration.roundtrip": {
			Title:  "integration alert",
			Since:  time.Date(2026, time.October, 8, 1, 2, 3, 0, time.UTC),
			SentAt: time.Date(2026, time.October, 8, 1, 12, 3, 0, time.UTC),
		},
	}
	if err := repository.SaveState(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := repository.LoadState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got["integration.roundtrip"] != want["integration.roundtrip"] {
		t.Fatalf("alert state round trip = %#v, want %#v", got, want)
	}
}
