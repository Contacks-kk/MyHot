package mcpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"aihot/backend-go/internal/publiccontent"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpFixtureRepository struct{}

func (mcpFixtureRepository) Availability(context.Context, []string) ([]publiccontent.Record, error) {
	return nil, nil
}

func (mcpFixtureRepository) StoryFollowups(context.Context, string, time.Time) ([]publiccontent.FollowupMember, error) {
	return nil, nil
}

func (mcpFixtureRepository) GroupReports(context.Context, publiccontent.GroupReportsQuery, time.Time) ([]publiccontent.GroupReportRow, error) {
	return nil, nil
}

func (mcpFixtureRepository) V1Items(_ context.Context, _ publiccontent.V1ItemsQuery, now time.Time, _ *time.Time, _ string, _ []string) ([]publiccontent.V1ItemRecord, error) {
	return []publiccontent.V1ItemRecord{{
		ID: "item-1", Title: "Fixture title", SourceName: "Fixture source", URL: "https://source.example/item-1",
		DiscoveredAt: now, SortAt: now, Selected: true,
	}}, nil
}

func TestHandlerListsCoreToolsForBothMCPLifecycles(t *testing.T) {
	content := publiccontent.NewService(mcpFixtureRepository{})
	service := NewService(content, Config{Prefix: "example", SiteName: "Example", SiteURL: "https://example.test"})
	server := httptest.NewServer(service.Handler())
	defer server.Close()

	for _, protocolVersion := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(protocolVersion, func(t *testing.T) {
			client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, &mcp.ClientOptions{
				Capabilities: &mcp.ClientCapabilities{},
			})
			transport := &mcp.StreamableClientTransport{
				Endpoint: server.URL, HTTPClient: server.Client(), DisableStandaloneSSE: true, MaxRetries: -1,
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			session, err := client.Connect(ctx, transport, &mcp.ClientSessionOptions{ProtocolVersion: protocolVersion})
			if err != nil {
				t.Fatalf("connect MCP client: %v", err)
			}
			defer session.Close()
			tools, err := session.ListTools(ctx, &mcp.ListToolsParams{})
			if err != nil {
				t.Fatalf("list MCP tools: %v", err)
			}
			got := make([]string, 0, len(tools.Tools))
			for _, tool := range tools.Tools {
				got = append(got, tool.Name)
			}
			want := map[string]bool{
				"example_get_latest": true, "example_search": true, "example_get_hot_topics": true,
				"example_get_story": true, "example_get_daily": true, "example_get_weekly": true, "example_get_monthly": true,
			}
			for _, name := range got {
				delete(want, name)
			}
			if len(tools.Tools) != 7 || len(want) != 0 {
				t.Fatalf("unexpected tools: %#v", got)
			}
			call, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "example_search", Arguments: map[string]any{"q": "x"}})
			if err != nil {
				t.Fatalf("call MCP tool: %v", err)
			}
			if !call.IsError || call.StructuredContent == nil {
				t.Fatalf("invalid search should be reported as a structured tool error: %#v", call)
			}
			latest, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "example_get_latest"})
			if err != nil || latest.IsError {
				t.Fatalf("call MCP latest tool: result=%#v err=%v", latest, err)
			}
			if latest.Meta["example/contentTrust"] != "untrusted_external_data" {
				t.Fatalf("missing untrusted content metadata: %#v", latest.Meta)
			}
			structured, ok := latest.StructuredContent.(map[string]any)
			if !ok || structured["_example"] == nil {
				t.Fatalf("missing structured trust policy: %#v", latest.StructuredContent)
			}
		})
	}
}

/** TestListChangedClientDoesNotOpenPersistentListen verifies the MCP server keeps tool-list change notifications disabled. */
func TestListChangedClientDoesNotOpenPersistentListen(t *testing.T) {
	content := publiccontent.NewService(mcpFixtureRepository{})
	service := NewService(content, Config{Prefix: "example", SiteName: "Example", SiteURL: "https://example.test"})
	methods := make([]string, 0, 4)
	var methodsMu sync.Mutex
	handler := service.Handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read request body", http.StatusBadRequest)
			return
		}
		var request struct {
			Method string `json:"method"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			http.Error(w, "decode request", http.StatusBadRequest)
			return
		}
		methodsMu.Lock()
		methods = append(methods, request.Method)
		methodsMu.Unlock()
		r.Body = io.NopCloser(bytes.NewReader(body))
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {},
	})
	transport := &mcp.StreamableClientTransport{Endpoint: server.URL, HTTPClient: server.Client(), DisableStandaloneSSE: true, MaxRetries: -1}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := client.Connect(ctx, transport, &mcp.ClientSessionOptions{ProtocolVersion: "2026-07-28"})
	if err != nil {
		t.Fatalf("connect MCP client: %v", err)
	}
	defer session.Close()
	capabilities := session.InitializeResult().Capabilities
	if capabilities == nil || capabilities.Tools == nil || capabilities.Tools.ListChanged {
		t.Fatalf("tool list-change capability should remain disabled: %#v", capabilities)
	}
	if _, err := session.ListTools(ctx, &mcp.ListToolsParams{}); err != nil {
		t.Fatalf("list MCP tools: %v", err)
	}

	methodsMu.Lock()
	defer methodsMu.Unlock()
	for _, method := range methods {
		if method == "subscriptions/listen" {
			t.Fatalf("client opened a persistent list-change stream: %#v", methods)
		}
	}
}

/** TestServiceCloseDrainsLiveListen verifies active MCP streams finish before HTTP shutdown. */
func TestServiceCloseDrainsLiveListen(t *testing.T) {
	content := publiccontent.NewService(mcpFixtureRepository{})
	service := NewService(content, Config{Prefix: "example", SiteName: "Example", SiteURL: "https://example.test"})
	server := httptest.NewServer(service.Handler())
	defer server.Close()
	defer service.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"subscriptions/listen","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}},"notifications":{"toolsListChanged":true}}}`
	request, err := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2026-07-28")
	request.Header.Set("MCP-Method", "subscriptions/listen")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("open MCP listen stream: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
		responseBody, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected listen response: status=%d headers=%v body=%s", response.StatusCode, response.Header, responseBody)
	}

	reader := bufio.NewReader(response.Body)
	acknowledged := make(chan error, 1)
	go func() {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				acknowledged <- err
				return
			}
			if strings.Contains(line, "acknowledged") {
				acknowledged <- nil
				return
			}
		}
	}()
	select {
	case err := <-acknowledged:
		if err != nil {
			t.Fatalf("read MCP listen acknowledgement: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for MCP listen acknowledgement")
	}

	streamClosed := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, reader)
		streamClosed <- err
	}()
	service.Close()
	select {
	case err := <-streamClosed:
		if err != nil {
			t.Fatalf("drain MCP listen stream: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("MCP listen stream remained open after service shutdown")
	}
}
