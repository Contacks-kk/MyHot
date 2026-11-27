package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/config"
	"aihot/backend-go/internal/interests"
)

func TestMCPAuthenticatedOversizeRequestKeepsJSONRPCOriginAndPrivateContract(t *testing.T) {
	pool, orm := authTestPools(t)
	ctx := context.Background()
	email := fmt.Sprintf("mcp-body-%d@example.invalid", time.Now().UnixNano())
	var userID int64
	if err := pool.QueryRow(ctx, `INSERT INTO user_accounts(email,password_hash,verified_at) VALUES($1,'fixture',now()) RETURNING id`, email).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM user_accounts WHERE id=$1`, userID) })

	server := New(config.Config{SiteURL: "https://myhot.example"}, pool, orm, map[string]interests.Entity{})
	redisStore := attachTestRedis(t, server)
	token := seedTestUserSession(t, redisStore, userID, "fixture-csrf", time.Hour)
	handler := server.Handler()
	request := httptest.NewRequest(http.MethodPost, "https://myhot.example/api/mcp", strings.NewReader(strings.Repeat(" ", mcpMaxBody+1)))
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	request.Header.Set("Origin", "https://myhot.example")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized request status = %d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("Surrogate-Control") != "no-store" {
		t.Fatalf("oversized request must remain private: %v", response.Header())
	}
	if response.Header().Get("Access-Control-Allow-Origin") != "https://myhot.example" {
		t.Fatalf("allowed Origin was not preserved: %v", response.Header())
	}
	if got, want := strings.TrimSpace(response.Body.String()), `{"jsonrpc":"2.0","error":{"code":-32000,"message":"MCP request body is too large."},"id":null}`; got != want {
		t.Fatalf("oversized request error = %s, want %s", got, want)
	}
}
