package account

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"aihot/backend-go/internal/auth"
	"aihot/backend-go/internal/db"
)

func testRedisStore(t *testing.T) *RedisSessionStore {
	t.Helper()
	raw := os.Getenv("MYHOT_TEST_REDIS_URL")
	if raw == "" {
		t.Skip("MYHOT_TEST_REDIS_URL is not configured")
	}

	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" && parsed.Hostname() != "::1") {
		t.Fatalf("integration Redis must run on localhost, got %q (%v)", raw, err)
	}

	client, err := db.OpenRedis(context.Background(), raw, os.Getenv("REDIS_CA_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return NewRedisSessionStore(client)
}

func TestRedisSessionStoreSlidingLifecycleAndRevocation(t *testing.T) {
	store := testRedisStore(t)
	ctx := context.Background()
	userID := time.Now().UnixNano()
	t.Cleanup(func() { _ = store.RevokeUser(ctx, userID) })

	tokenA, hashA, err := auth.NewToken()
	if err != nil || tokenA == "" {
		t.Fatalf("NewToken failed: %v", err)
	}
	tokenB, hashB, err := auth.NewToken()
	if err != nil || tokenB == "" {
		t.Fatalf("NewToken B failed: %v", err)
	}

	// Create session with 10s TTL and 5s refresh threshold: initial reads must not renew
	ttl := 10 * time.Second
	threshold := 5 * time.Second
	if err := store.Create(ctx, SessionRecord{
		UserID:    userID,
		Email:     "redis-a@example.invalid",
		CSRF:      "csrf-a",
		TokenHash: hashA,
	}, ttl); err != nil {
		t.Fatal(err)
	}

	resolved, renewed, err := store.Resolve(ctx, hashA, ttl, threshold, true)
	if err != nil || renewed || resolved.ID != userID || resolved.CSRF != "csrf-a" {
		t.Fatalf("fresh session should resolve without renewal: session=%+v renewed=%t err=%v", resolved, renewed, err)
	}

	// Shorten TTL below threshold and verify touch=false reads without extending TTL
	if err := store.client.Expire(ctx, sessionKey(hashA), 3*time.Second).Err(); err != nil {
		t.Fatal(err)
	}
	_, renewed, err = store.Resolve(ctx, hashA, ttl, threshold, false)
	if err != nil || renewed {
		t.Fatalf("touch=false must not renew TTL: renewed=%t err=%v", renewed, err)
	}

	// Now resolve with touch=true; TTL is below threshold so Redis must extend back to ttl and report renewed=true
	_, renewed, err = store.Resolve(ctx, hashA, ttl, threshold, true)
	if err != nil || !renewed {
		t.Fatalf("touch=true below threshold must renew TTL: renewed=%t err=%v", renewed, err)
	}
	remaining, err := store.client.TTL(ctx, sessionKey(hashA)).Result()
	if err != nil || remaining < 8*time.Second {
		t.Fatalf("renewed TTL expected near %v, got %v (%v)", ttl, remaining, err)
	}

	// Second session for same user: Delete removes only hashB, RevokeUser clears remaining sessions
	if err := store.Create(ctx, SessionRecord{
		UserID:    userID,
		Email:     "redis-a@example.invalid",
		CSRF:      "csrf-b",
		TokenHash: hashB,
	}, ttl); err != nil {
		t.Fatal(err)
	}
	active, err := store.CountActiveUserSessions(ctx, userID)
	if err != nil || active != 2 {
		t.Fatalf("expected 2 active sessions, got %d (%v)", active, err)
	}
	if err := store.Delete(ctx, userID, hashB); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Resolve(ctx, hashB, ttl, threshold, true); !errors.Is(err, ErrNoSession) {
		t.Fatalf("deleted session must return ErrNoSession, got %v", err)
	}
	if err := store.RevokeUser(ctx, userID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Resolve(ctx, hashA, ttl, threshold, true); !errors.Is(err, ErrNoSession) {
		t.Fatalf("revoked user session must return ErrNoSession, got %v", err)
	}
	active, err = store.CountActiveUserSessions(ctx, userID)
	if err != nil || active != 0 {
		t.Fatalf("expected 0 active sessions after revoke, got %d (%v)", active, err)
	}

	// Verify pruning keeps user set bounded after expired sessions
	for i := 0; i < userSessionPruneThreshold+2; i++ {
		expiredHash := fmt.Sprintf("expired_%d_%d", userID, i)
		if err := store.client.SAdd(ctx, userSessionsKey(userID), expiredHash).Err(); err != nil {
			t.Fatal(err)
		}
	}
	_, hashFresh, _ := auth.NewToken()
	if err := store.Create(ctx, SessionRecord{
		UserID:    userID,
		Email:     "redis-a@example.invalid",
		CSRF:      "csrf-fresh",
		TokenHash: hashFresh,
	}, ttl); err != nil {
		t.Fatal(err)
	}
	countAfterPrune, err := store.client.SCard(ctx, userSessionsKey(userID)).Result()
	if err != nil || countAfterPrune != 1 {
		t.Fatalf("expected expired handles pruned down to 1, got %d (%v)", countAfterPrune, err)
	}
}
