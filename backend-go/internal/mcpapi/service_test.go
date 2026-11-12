package mcpapi

import (
	"context"
	"net/http/httptest"
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
