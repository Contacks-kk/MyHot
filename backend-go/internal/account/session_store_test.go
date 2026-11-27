package account

import (
	"context"
	"errors"
	"testing"

	"aihot/backend-go/internal/config"
)

func TestRedisSessionStoreNilClientReturnsUnavailable(t *testing.T) {
	store := NewRedisSessionStore(nil)
	ctx := context.Background()

	err := store.Create(ctx, SessionRecord{
		UserID:    1,
		Email:     "fixture@example.invalid",
		CSRF:      "csrf",
		TokenHash: "hash",
	}, DefaultSessionTTL)
	if !errors.Is(err, ErrSessionStoreUnavailable) {
		t.Fatalf("expected ErrSessionStoreUnavailable on Create, got %v", err)
	}

	_, _, err = store.Resolve(ctx, "hash", DefaultSessionTTL, DefaultSessionRefreshThreshold, true)
	if !errors.Is(err, ErrSessionStoreUnavailable) || errors.Is(err, ErrNoSession) {
		t.Fatalf("expected ErrSessionStoreUnavailable (and never ErrNoSession) on Resolve, got %v", err)
	}

	if err := store.Delete(ctx, 1, "hash"); !errors.Is(err, ErrSessionStoreUnavailable) {
		t.Fatalf("expected ErrSessionStoreUnavailable on Delete, got %v", err)
	}

	if err := store.RevokeUser(ctx, 1); !errors.Is(err, ErrSessionStoreUnavailable) {
		t.Fatalf("expected ErrSessionStoreUnavailable on RevokeUser, got %v", err)
	}
}

func TestServiceRequiresSessionStoreWithoutMasqueradingAsNoSession(t *testing.T) {
	service := NewService(nil, nil, config.Config{}, nil)
	ctx := context.Background()

	_, renewed, err := service.ResolveSessionWithTouch(ctx, "raw-token", true)
	if !errors.Is(err, ErrSessionStoreUnavailable) || errors.Is(err, ErrNoSession) || renewed {
		t.Fatalf("expected ErrSessionStoreUnavailable without renewal, got renewed=%t err=%v", renewed, err)
	}

	if err := service.Logout(ctx, 1, "raw-token"); !errors.Is(err, ErrSessionStoreUnavailable) {
		t.Fatalf("expected ErrSessionStoreUnavailable on Logout, got %v", err)
	}
}
