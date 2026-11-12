package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aihot/backend-go/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRegistrationStatusAndNoPolicyAcceptance(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://127.0.0.1:1/myhot_test?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	server := New(config.Config{
		PublicURL: "http://localhost",
		SMTPHost:  "mail.example.invalid",
		SMTPPort:  587,
		SMTPFrom:  "noreply@example.invalid",
	}, pool, closedGORM(t, "postgres://127.0.0.1:1/myhot_test?connect_timeout=1"), nil)
	checkStatus := func(want bool) {
		t.Helper()
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://localhost/api/auth/registration-status", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("registration status returned %d", response.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["enabled"] != want || len(body) != 1 {
			t.Fatalf("registration status = %v, want only enabled=%t", body, want)
		}
	}

	checkStatus(false)
	server.Config.OpenRegistration = true
	checkStatus(true)

	for _, body := range []string{
		`{"email":"reader@example.invalid","password":"fixture-password-123","unexpected":true}`,
		`{"email":"not-an-email","password":"short"}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "http://localhost/api/auth/register", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid registration body returned %d: %s", response.Code, response.Body.String())
		}
	}

	request := httptest.NewRequest(http.MethodPost, "http://localhost/api/auth/register", strings.NewReader(`{"email":"reader@example.invalid","password":"fixture-password-123"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("registration without a code must be rejected before database access, got %d: %s", response.Code, response.Body.String())
	}
}
