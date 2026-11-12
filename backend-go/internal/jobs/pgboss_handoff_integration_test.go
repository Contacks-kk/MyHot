package jobs

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

func TestPgBossHandoffAtomicallyMovesOwnedQueueWorkToRiver(t *testing.T) {
	raw := os.Getenv("MYHOT_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("MYHOT_TEST_DATABASE_URL is not configured")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") || !(strings.HasSuffix(parsed.Path, "_test") || strings.HasSuffix(parsed.Path, "_ci")) {
		t.Fatal("integration database must be localhost and end in _test or _ci")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		t.Fatal(err)
	}
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("handoff_%d", time.Now().UnixNano())
	schema := "handoff_test_" + strings.TrimPrefix(prefix, "handoff_")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	sourceID := prefix + "_source"
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DROP SCHEMA "+quotedSchema+" CASCADE")
		_, _ = pool.Exec(ctx, `DELETE FROM sources WHERE id=$1`, sourceID)
		_, _ = pool.Exec(ctx, `DELETE FROM river_job WHERE args->>'sourceId'=$1 OR args->>'articleId'=$1 OR args->>'articleId'=$2`, sourceID, prefix+"_article")
	})
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE TABLE %s.job (
		id uuid PRIMARY KEY DEFAULT gen_random_uuid(), name text NOT NULL, data jsonb,
		state text NOT NULL, start_after timestamptz NOT NULL, created_on timestamptz NOT NULL,
		retry_count integer NOT NULL DEFAULT 0, completed_on timestamptz, output jsonb)`, quotedSchema)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,config) VALUES($1,'Handoff test source','rss','{"feedUrl":"https://example.invalid/feed"}')`, sourceID); err != nil {
		t.Fatal(err)
	}
	insert := fmt.Sprintf(`INSERT INTO %s.job(name,data,state,start_after,created_on,retry_count) VALUES($1,$2::jsonb,'created',now(),now(),$3) RETURNING id::text`, quotedSchema)
	var manualID, scheduledID, notifyID, mediaID string
	for _, record := range []struct {
		name    string
		data    string
		retries int
		id      *string
	}{
		{"sources.fetch", fmt.Sprintf(`{"sourceId":%q,"force":true}`, sourceID), 0, &manualID},
		{"sources.fetch", fmt.Sprintf(`{"sourceId":%q}`, sourceID), 0, &scheduledID},
		{"notify.selected", fmt.Sprintf(`{"articleId":%q,"attempt":2}`, prefix+"_article"), 0, &notifyID},
		{"media.prepare", fmt.Sprintf(`{"articleId":%q}`, prefix+"_article"), 1, &mediaID},
	} {
		if err := pool.QueryRow(ctx, insert, record.name, record.data, record.retries).Scan(record.id); err != nil {
			t.Fatal(err)
		}
	}
	result, err := importPgBossBatch(ctx, pool, client, schema, false, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Imported != 2 || result.Retired != 1 {
		t.Fatalf("handoff result = %#v", result)
	}
	var state string
	if err := pool.QueryRow(ctx, fmt.Sprintf(`SELECT state FROM %s.job WHERE id=$1`, quotedSchema), manualID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "created" {
		t.Fatalf("manual source fetch was consumed while collection is disabled: %s", state)
	}
	resumed, err := importPgBossBatch(ctx, pool, client, schema, true, true, true)
	if err != nil || resumed.Imported != 1 || resumed.Retired != 0 {
		t.Fatalf("manual source fetch resume = %#v, %v", resumed, err)
	}
	var output []byte
	for _, id := range []string{manualID, scheduledID, notifyID, mediaID} {
		if err := pool.QueryRow(ctx, fmt.Sprintf(`SELECT state,output FROM %s.job WHERE id=$1`, quotedSchema), id).Scan(&state, &output); err != nil {
			t.Fatal(err)
		}
		if state != "completed" || !strings.Contains(string(output), `"handoff":"river"`) {
			t.Fatalf("source row %s handoff state=%s output=%s", id, state, output)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind IN ('myhot_collect_source','notify.selected','media.prepare') AND (args->>'sourceId'=$1 OR args->>'articleId'=$2)`, sourceID, prefix+"_article").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("River transferred job count = %d", count)
	}
	second, err := importPgBossBatch(ctx, pool, client, schema, true, true, true)
	if err != nil || second.Imported != 0 || second.Retired != 0 {
		t.Fatalf("second handoff = %#v, %v", second, err)
	}
}
