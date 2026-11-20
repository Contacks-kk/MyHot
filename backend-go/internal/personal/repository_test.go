package personal

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

/** TestHotWindowsUseBeijingCalendarDays checks midnight boundaries, pagination, and invalid selections. */
func TestHotWindowsUseBeijingCalendarDays(t *testing.T) {
	now := time.Date(2026, 11, 20, 17, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		query  HotQuery
		start  string
		end    string
		limit  int
		offset int
	}{
		{"default", HotQuery{Page: 1}, "2026-11-18T17:00:00Z", "2026-11-20T17:00:00Z", 10, 0},
		{"today after Beijing midnight", HotQuery{Range: "today", Page: 1}, "2026-11-20T16:00:00Z", "2026-11-20T17:00:00Z", 50, 0},
		{"historical full day", HotQuery{Range: "date", Date: "2026-11-19", Page: 2}, "2026-11-18T16:00:00Z", "2026-11-19T16:00:00Z", 50, 50},
		{"selected current day", HotQuery{Range: "date", Date: "2026-11-21", Page: 1}, "2026-11-20T16:00:00Z", "2026-11-20T17:00:00Z", 50, 0},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			window, err := hotWindowFor(test.query, now)
			if err != nil {
				t.Fatal(err)
			}
			if window.Start.UTC().Format(time.RFC3339) != test.start || window.End.UTC().Format(time.RFC3339) != test.end || window.Limit != test.limit || window.Offset != test.offset {
				t.Fatalf("unexpected hotspot window: %+v", window)
			}
		})
	}
	for _, query := range []HotQuery{
		{Range: "unknown", Page: 1},
		{Range: "date", Page: 1},
		{Range: "date", Date: "2026-02-30", Page: 1},
		{Range: "date", Date: "2026-11-22", Page: 1},
		{Range: "today", Page: 0},
		{Range: "today", Page: 100001},
		{Range: "48h", Page: 2},
	} {
		_, err := hotWindowFor(query, now)
		var inputError *InputError
		if !errors.As(err, &inputError) {
			t.Fatalf("expected input error for %+v, got %v", query, err)
		}
	}
}

func TestSaveInterestsBindsArraysAsSingleParameters(t *testing.T) {
	orm, err := gorm.Open(postgres.Open("postgres://localhost:1/unused_test?sslmode=disable"), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := orm.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	var statement string
	var parameters int
	if err := orm.Callback().Raw().After("gorm:raw").Register("capture_interests_parameters", func(tx *gorm.DB) {
		statement, parameters = tx.Statement.SQL.String(), len(tx.Statement.Vars)
	}); err != nil {
		t.Fatal(err)
	}
	for _, values := range [][]string{nil, {}, {"OpenAI"}, {"具身智能", `引号"`, `反\斜杠`}} {
		if err := NewRepository(orm).SaveInterests(context.Background(), 1, values, values); err != nil {
			t.Fatal(err)
		}
		if parameters != 3 || !strings.Contains(statement, "VALUES($1,$2,$3)") {
			t.Errorf("array length %d generated %d parameters: %s", len(values), parameters, statement)
		}
	}
}
