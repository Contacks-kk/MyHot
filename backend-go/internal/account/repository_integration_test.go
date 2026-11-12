package account

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRegisterCreatesVerifiedAccountAndRejectsDuplicates(t *testing.T) {
	raw := os.Getenv("MYHOT_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("MYHOT_TEST_DATABASE_URL is not configured")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" && parsed.Hostname() != "::1") || !(strings.HasSuffix(parsed.Path, "_test") || strings.HasSuffix(parsed.Path, "_ci")) {
		t.Fatal("integration database must be localhost and end in _test or _ci")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	orm, err := db.OpenGORM(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.CloseGORM(orm) })

	prefix := fmt.Sprintf("account_rollback_%d", time.Now().UnixNano())
	email := prefix + "@example.invalid"
	repository := NewRepository(orm)
	id, err := repository.Register(ctx, email, "fixture-password-hash", prefix+"_token", "fixture-csrf", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM user_accounts WHERE id=$1`, id) })
	session, err := repository.FindSession(ctx, prefix+"_token")
	if err != nil || session.ID != id || session.CSRF != "fixture-csrf" {
		t.Fatalf("registration session: %+v %v", session, err)
	}
	failedEmail := prefix + "_rollback@example.invalid"
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM user_accounts WHERE email=$1`, failedEmail) })
	if _, err := repository.Register(ctx, failedEmail, "fixture-password-hash", prefix+"_token", "fixture-csrf", time.Now().Add(time.Hour)); err == nil {
		t.Fatal("duplicate session token should roll back registration")
	}
	var surviving int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_accounts WHERE email=$1`, failedEmail).Scan(&surviving); err != nil || surviving != 0 {
		t.Fatalf("failed session left an account: %d %v", surviving, err)
	}
	var verified bool
	if err := pool.QueryRow(ctx, `SELECT verified_at IS NOT NULL FROM user_accounts WHERE id=$1`, id).Scan(&verified); err != nil || !verified {
		t.Fatalf("verified account: %v %v", verified, err)
	}
	if _, err := repository.Register(ctx, email, "other-hash", prefix+"_other_token", "other-csrf", time.Now().Add(time.Hour)); err != ErrDuplicateAccount {
		t.Fatalf("duplicate: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_email_tokens WHERE user_id=$1`, id).Scan(&count); err != nil || count != 0 {
		t.Fatalf("registration must not persist email tokens: %d %v", count, err)
	}
}
