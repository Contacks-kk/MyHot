package httpapi

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aihot/backend-go/internal/config"
)

func TestMCPPreflightIsAnonymousAndRestrictsOrigin(t *testing.T) {
	server := (&Server{Config: config.Config{SiteURL: "https://myhot.example"}}).Handler()
	request := httptest.NewRequest(http.MethodOptions, "/api/mcp", nil)
	request.Header.Set("Origin", "https://myhot.example")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || response.Header().Get("Access-Control-Allow-Origin") != "https://myhot.example" {
		t.Fatalf("allowed preflight failed: %d %v %s", response.Code, response.Header(), response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Surrogate-Control") != "" {
		t.Fatalf("preflight cache policy changed: %v", response.Header())
	}
	if got := response.Header().Get("Vary"); got != "Origin" {
		t.Fatalf("unexpected preflight Vary header %q", got)
	}

	request = httptest.NewRequest(http.MethodOptions, "/api/mcp", nil)
	request.Header.Set("Origin", "https://attacker.example")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || strings.Contains(response.Body.String(), "origin_not_allowed") == false {
		t.Fatalf("disallowed origin response changed: %d %s", response.Code, response.Body.String())
	}
}

func TestMCPContentRequiresVerifiedUserSessionBeforeHostValidation(t *testing.T) {
	server := (&Server{Config: config.Config{SiteURL: "https://myhot.example"}}).Handler()
	request := httptest.NewRequest(http.MethodPost, "/api/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	request.Host = "attacker.example"
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), `"code":"unauthorized"`) {
		t.Fatalf("anonymous MCP call must be rejected by the session boundary first: %d %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("Surrogate-Control") != "no-store" {
		t.Fatalf("MCP auth response must stay private: %v", response.Header())
	}
}

func TestMCPHostAndOriginAllowLists(t *testing.T) {
	server := &Server{Config: config.Config{SiteURL: "https://myhot.example", MCPAllowedHosts: []string{"alias.example:8443"}}}
	for _, host := range []string{"myhot.example", "myhot.example:443", "alias.example:8080", "localhost:3000", "127.0.0.1:3000", "[::1]:3000"} {
		request := httptest.NewRequest(http.MethodGet, "http://unused/api/mcp", nil)
		request.Host = host
		if !server.mcpAllowedHost(request) {
			t.Errorf("expected host %q to be allowed", host)
		}
	}
	for _, host := range []string{"attacker.example", "myhot.example/path", "myhot.example:65536", "site.example, attacker.example"} {
		request := httptest.NewRequest(http.MethodGet, "http://unused/api/mcp", nil)
		request.Host = host
		if server.mcpAllowedHost(request) {
			t.Errorf("unexpected allowed host %q", host)
		}
	}
	request := httptest.NewRequest(http.MethodGet, "http://unused/api/mcp", nil)
	request.Host = "myhot.example"
	request.Header.Add("X-Forwarded-Host", "myhot.example")
	request.Header.Add("X-Forwarded-Host", "attacker.example")
	if server.mcpAllowedHost(request) {
		t.Fatal("multiple forwarded authorities must be rejected")
	}

	for _, origin := range []string{"", "https://myhot.example", "https://myhot.example:9443", "http://localhost:3000", "https://127.0.0.1:5173"} {
		if !server.mcpAllowedOrigin(origin) {
			t.Errorf("expected origin %q to be allowed", origin)
		}
	}
	for _, origin := range []string{"https://attacker.example", "ftp://localhost", "invalid"} {
		if server.mcpAllowedOrigin(origin) {
			t.Errorf("unexpected allowed origin %q", origin)
		}
	}
}

func TestMCPRequestBodyLimitAndBatchDetection(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/mcp", bytes.NewReader([]byte(`[{},{}]`)))
	response := httptest.NewRecorder()
	body, tooLarge, err := readMCPBody(response, request)
	if err != nil || tooLarge || !isMCPBatch(body) {
		t.Fatalf("batch body not recognized: %q %t %v", body, tooLarge, err)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/mcp", io.LimitReader(strings.NewReader(strings.Repeat("x", mcpMaxBody+1)), int64(mcpMaxBody+1)))
	response = httptest.NewRecorder()
	_, tooLarge, err = readMCPBody(response, request)
	if err != nil || !tooLarge {
		t.Fatalf("oversized request body not rejected: large=%t err=%v", tooLarge, err)
	}
}
