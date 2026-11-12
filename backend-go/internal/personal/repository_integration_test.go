package personal

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/db"
)

func TestInterestsRepositoryRoundTrip(t *testing.T) {
	raw := os.Getenv("MYHOT_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("MYHOT_TEST_DATABASE_URL is not configured")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") || !strings.HasSuffix(parsed.Path, "_test") {
		t.Fatal("integration database must be localhost and end in _test")
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	orm, err := db.OpenGORM(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.CloseGORM(orm) })
	var userID int64
	if err := pool.QueryRow(ctx, `INSERT INTO user_accounts(email,password_hash,verified_at) VALUES($1,'unused',now()) RETURNING id`, fmt.Sprintf("interests_%d@example.invalid", time.Now().UnixNano())).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM user_accounts WHERE id=$1`, userID) })
	repository := NewRepository(orm)
	cases := []struct {
		name               string
		keywords, entities []string
	}{
		{"nil", nil, nil},
		{"empty", []string{}, []string{}},
		{"single", []string{"OpenAI"}, []string{"openai"}},
		{"multiple", []string{"具身智能", "Go"}, []string{"openai", "google"}},
		{"escaped", []string{`引号"`, `逗号,`, `反\斜杠`, `{花括号}`, "", "NULL"}, []string{`quote"`, `comma,`, `back\slash`}},
	}
	for _, test := range cases {
		t.Run("save_"+test.name, func(t *testing.T) {
			if err := repository.SaveInterests(ctx, userID, test.keywords, test.entities); err != nil {
				t.Fatal(err)
			}
			var keywords, entities []string
			if err := pool.QueryRow(ctx, `SELECT keywords,entity_ids FROM user_interests WHERE user_id=$1`, userID).Scan(&keywords, &entities); err != nil {
				t.Fatal(err)
			}
			wantKeywords, wantEntities := test.keywords, test.entities
			if wantKeywords == nil {
				wantKeywords = []string{}
			}
			if wantEntities == nil {
				wantEntities = []string{}
			}
			if !reflect.DeepEqual(keywords, wantKeywords) || !reflect.DeepEqual(entities, wantEntities) {
				t.Fatalf("stored interests = %q / %q", keywords, entities)
			}
		})
	}
	for _, test := range cases[1:] {
		t.Run("read_"+test.name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, `INSERT INTO user_interests(user_id,keywords,entity_ids) VALUES($1,$2,$3) ON CONFLICT(user_id) DO UPDATE SET keywords=excluded.keywords,entity_ids=excluded.entity_ids`, userID, test.keywords, test.entities); err != nil {
				t.Fatal(err)
			}
			value, err := repository.Interests(ctx, userID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(value.Keywords, test.keywords) || !reflect.DeepEqual(value.EntityIDs, test.entities) {
				t.Fatalf("read interests = %q / %q", value.Keywords, value.EntityIDs)
			}
		})
	}
}
