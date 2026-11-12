package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAgentMarkdownETagSupportsNotModifiedAndHead(t *testing.T) {
	body := "# Example\n"
	sum := sha256.Sum256([]byte(body))
	wantETag := `W/"agent-hot-` + hex.EncodeToString(sum[:])[:16] + `"`

	first := httptest.NewRecorder()
	writeAgentMarkdownWithETag(first, httptest.NewRequest(http.MethodGet, "/api/v1/agent/hot", nil), "agent-hot", body)
	if first.Code != http.StatusOK || first.Header().Get("ETag") != wantETag || first.Header().Get("Content-Type") != "text/markdown; charset=utf-8" || first.Body.String() != body {
		t.Fatalf("first response status=%d headers=%v body=%q", first.Code, first.Header(), first.Body.String())
	}

	conditionalRequest := httptest.NewRequest(http.MethodGet, "/api/v1/agent/hot", nil)
	conditionalRequest.Header.Set("If-None-Match", wantETag)
	conditional := httptest.NewRecorder()
	writeAgentMarkdownWithETag(conditional, conditionalRequest, "agent-hot", body)
	if conditional.Code != http.StatusNotModified || conditional.Body.Len() != 0 || conditional.Header().Get("ETag") != wantETag {
		t.Fatalf("conditional response status=%d headers=%v body=%q", conditional.Code, conditional.Header(), conditional.Body.String())
	}

	head := httptest.NewRecorder()
	writeAgentMarkdownWithETag(head, httptest.NewRequest(http.MethodHead, "/api/v1/agent/hot", nil), "agent-hot", body)
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("ETag") != wantETag {
		t.Fatalf("HEAD response status=%d headers=%v body=%q", head.Code, head.Header(), head.Body.String())
	}
}

func TestAgentLatestAndSearchRequestBinding(t *testing.T) {
	server := &Server{CategoryKeys: map[string]struct{}{"ai-models": {}, "paper": {}}, CategoryOrder: []string{"ai-models", "paper"}}
	cases := []struct {
		path   string
		search bool
		check  func(agentItemsRequest) bool
	}{
		{path: "/api/v1/agent/latest", check: func(parsed agentItemsRequest) bool {
			return parsed.Window == "24h" && parsed.Mode == "selected" && parsed.Limit == 10 && parsed.Category == nil
		}},
		{path: "/api/v1/agent/latest?window=7d&mode=all&category=paper&limit=30", check: func(parsed agentItemsRequest) bool {
			return parsed.Window == "7d" && parsed.Mode == "all" && parsed.Limit == 30 && parsed.Category != nil && *parsed.Category == "paper"
		}},
		{path: "/api/v1/agent/search?q=%EF%BB%BFAI+models", search: true, check: func(parsed agentItemsRequest) bool {
			return parsed.Window == "7d" && parsed.Mode == "" && parsed.Limit == 10 && parsed.Text == "AI models"
		}},
	}
	for _, test := range cases {
		parsed, err := server.parseAgentItemsRequest(httptest.NewRequest(http.MethodGet, test.path, nil), test.search)
		if err != nil || !test.check(parsed) {
			t.Errorf("parse %s = %#v, error=%v", test.path, parsed, err)
		}
	}
}

func TestAgentLatestAndSearchRejectInvalidQueryContracts(t *testing.T) {
	server := &Server{CategoryKeys: map[string]struct{}{"ai-models": {}, "paper": {}}, CategoryOrder: []string{"ai-models", "paper"}}
	cases := []struct {
		path   string
		search bool
		want   string
	}{
		{path: "/api/v1/agent/latest?unknown=x", want: "Unknown query parameter: unknown."},
		{path: "/api/v1/agent/latest?mode=all&mode=selected", want: "Query parameter must not be repeated: mode."},
		{path: "/api/v1/agent/latest?mode=invalid", want: "mode must be 'selected' or 'all'."},
		{path: "/api/v1/agent/latest?window=2d", want: "window must be '24h' or '7d'."},
		{path: "/api/v1/agent/latest?category=nope", want: "category must be one of: ai-models, paper."},
		{path: "/api/v1/agent/latest?limit=31", want: "limit must be an integer from 1 to 30."},
		{path: "/api/v1/agent/search?q=x", search: true, want: "q must contain 2 to 200 characters."},
		{path: "/api/v1/agent/search?q=valid&mode=all", search: true, want: "Unknown query parameter: mode."},
	}
	for _, test := range cases {
		_, err := server.parseAgentItemsRequest(httptest.NewRequest(http.MethodGet, test.path, nil), test.search)
		if err == nil || err.Error() != test.want {
			t.Errorf("parse %s error=%v, want %q", test.path, err, test.want)
		}
	}
}
