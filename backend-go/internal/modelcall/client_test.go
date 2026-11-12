package modelcall

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/provider"
	"aihot/backend-go/internal/receipts"
)

type fakeResolver struct {
	model Model
	err   error
}

func (r fakeResolver) Resolve(context.Context, string) (Model, error) { return r.model, r.err }

type fakeOverrides map[string]string

func (o fakeOverrides) Overrides(context.Context) (map[string]string, error) { return o, nil }

type fakeReceiptRepository struct {
	request   receipts.Request
	response  json.RawMessage
	completed int64
	rejected  int64
	failure   string
}

func (r *fakeReceiptRepository) Claim(_ context.Context, request receipts.Request, _ string) (receipts.Claim, error) {
	r.request = request
	return receipts.Claim{ReceiptID: 17, AttemptID: 18}, nil
}

func (r *fakeReceiptRepository) RecordFailure(_ context.Context, _ receipts.Claim, status, _ string, _ time.Duration) error {
	r.failure = status
	return nil
}

func (r *fakeReceiptRepository) RecordReceived(_ context.Context, _ receipts.Claim, outcome receipts.Outcome, _ time.Duration) error {
	r.response = outcome.Response
	return nil
}

func (r *fakeReceiptRepository) Complete(_ context.Context, id int64) error {
	r.completed = id
	return nil
}

func (r *fakeReceiptRepository) Reject(_ context.Context, id int64, _ string) error {
	r.rejected = id
	return nil
}

func TestClientCallsInjectedModelAndPersistsOnlyRedactedRequestSummary(t *testing.T) {
	repository := &fakeReceiptRepository{}
	apiKey := []byte("private-model-key")
	model := Model{Key: "preset", Service: "vendor", Name: "model-name", BaseURL: "https://model.example/", APIKey: apiKey, JSONMode: true,
		Extra: map[string]any{"enable_thinking": false}, ReasoningTokens: 100}
	var providerConfig provider.Config
	client := NewClient(true, fakeResolver{model: model}, receipts.NewService(repository), func(_ context.Context, cfg provider.Config, prompt string) (provider.Completion, error) {
		providerConfig = cfg
		if prompt != "user text" {
			t.Fatalf("provider prompt = %q", prompt)
		}
		return provider.Completion{Response: json.RawMessage(`{"id":"req-1","choices":[{"message":{"content":"{\"ok\":true}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4}}`),
			RequestID: "req-1", UsageJSON: json.RawMessage(`{"prompt_tokens":4}`)}, nil
	})
	temperature := 0.5
	result, err := client.Call(context.Background(), Request{Capability: "score", Purpose: "score_article", Subject: "article:a@1", PromptVersion: "score-v1",
		System: "rules", User: "user text", Temperature: &temperature, MaxTokens: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if result.ReceiptID != 17 || result.Model != "preset" || result.Completion.Text != `{"ok":true}` {
		t.Fatalf("model result = %#v", result)
	}
	if providerConfig.APIKey != string(apiKey) || providerConfig.MaxTokens != 1124 || providerConfig.Temperature == nil || *providerConfig.Temperature != temperature || providerConfig.System != "rules" || providerConfig.DisableJSON {
		t.Fatalf("provider config = %#v", providerConfig)
	}
	if repository.request.Service != "vendor" || repository.request.Model != "model-name" || repository.request.Purpose != "score_article" || repository.request.AttemptTag != "" {
		t.Fatalf("paid request = %#v", repository.request)
	}
	identity, ok := repository.request.Identity.(map[string]any)
	if !ok {
		t.Fatalf("paid identity = %#v", repository.request.Identity)
	}
	if _, exists := identity["images"]; exists {
		t.Fatalf("text-only identity must remain compatible with TypeScript receipts: %#v", identity)
	}
	encoded, err := json.Marshal(repository.request.Summary)
	if err != nil || strings.Contains(string(encoded), "private-model-key") {
		t.Fatalf("request summary contains a secret: %s (%v)", encoded, err)
	}
	if err := client.Complete(context.Background(), result.ReceiptID); err != nil || repository.completed != 17 {
		t.Fatalf("completed receipt = %d: %v", repository.completed, err)
	}
}

func TestClientRejectsUnusableReceivedResponseWithoutRepeatingCall(t *testing.T) {
	repository := &fakeReceiptRepository{}
	client := NewClient(true, fakeResolver{model: Model{Key: "preset", Service: "vendor", Name: "m", BaseURL: "https://model.example", APIKey: []byte("key"), JSONMode: true}},
		receipts.NewService(repository), func(context.Context, provider.Config, string) (provider.Completion, error) {
			return provider.Completion{Response: json.RawMessage(`{"unparsable":"truncated"}`)}, nil
		})
	_, err := client.Call(context.Background(), Request{Capability: "score", Purpose: "score_article", User: "prompt"})
	var outputError *OutputError
	if !errors.As(err, &outputError) || outputError.ReceiptID != 17 || repository.rejected != 17 || repository.failure != "" {
		t.Fatalf("unusable response error = %#v, rejected=%d failure=%s", err, repository.rejected, repository.failure)
	}
}

func TestClientKeepsModelCallsOffByDefault(t *testing.T) {
	client := NewClient(false, fakeResolver{}, receipts.NewService(&fakeReceiptRepository{}), func(context.Context, provider.Config, string) (provider.Completion, error) {
		t.Fatal("disabled client called provider")
		return provider.Completion{}, nil
	})
	if _, err := client.Call(context.Background(), Request{Capability: "score", Purpose: "score_article"}); err == nil {
		t.Fatal("disabled model calls returned no error")
	}
}

func TestClientSendsVisionInputAndStoresOnlyItsHash(t *testing.T) {
	repository := &fakeReceiptRepository{}
	image := "data:image/png;base64,aGVsbG8="
	client := NewClient(true, fakeResolver{model: Model{Key: "vision", Service: "vendor", Name: "vision-model", BaseURL: "https://model.example", APIKey: []byte("key"), Vision: true}},
		receipts.NewService(repository), func(_ context.Context, cfg provider.Config, prompt string) (provider.Completion, error) {
			if prompt != "inspect" || len(cfg.Images) != 1 || cfg.Images[0] != image {
				t.Fatalf("provider input = prompt %q, images %#v", prompt, cfg.Images)
			}
			return provider.Completion{Response: json.RawMessage(`{"choices":[{"message":{"content":"ok"}}]}`)}, nil
		})
	if _, err := client.Call(context.Background(), Request{Capability: "understand", Purpose: "understand_article", User: "inspect", Images: []string{image}}); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(repository.request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), image) || !strings.Contains(string(encoded), hashText(image)) || !strings.Contains(string(encoded), `"imageCount":1`) {
		t.Fatalf("vision receipt contains raw image or omits its identity: %s", encoded)
	}
}

func TestClientRejectsImagesForTextOnlyModelsBeforeReceiptClaim(t *testing.T) {
	repository := &fakeReceiptRepository{}
	called := false
	client := NewClient(true, fakeResolver{model: Model{Key: "text", Service: "vendor", Name: "text-model", BaseURL: "https://model.example", APIKey: []byte("key")}},
		receipts.NewService(repository), func(context.Context, provider.Config, string) (provider.Completion, error) {
			called = true
			return provider.Completion{}, nil
		})
	if _, err := client.Call(context.Background(), Request{Capability: "understand", Purpose: "understand_article", User: "inspect", Images: []string{"data:image/png;base64,aGVsbG8="}}); err == nil {
		t.Fatal("text-only model accepted image input")
	}
	if called || repository.request.Purpose != "" {
		t.Fatalf("unsupported vision request reached paid receipt/provider: called=%t request=%#v", called, repository.request)
	}
}

func TestCatalogResolverAppliesAdminEnvironmentAndDefaultPrecedence(t *testing.T) {
	catalog := Catalog{
		Presets: map[string]Preset{
			"default-preset": {Service: "vendor-a", Model: "model-a", BaseURLEnv: "VENDOR_A_URL", APIKeyEnv: "VENDOR_A_KEY", JSONMode: true},
			"admin-preset":   {Service: "vendor-b", Model: "model-b", BaseURLEnv: "VENDOR_B_URL", APIKeyEnv: "VENDOR_B_KEY", Extra: map[string]any{"thinking": "off"}, JSONMode: true},
		},
		Defaults:     map[string]string{"score": "default-preset"},
		Capabilities: map[string]Capability{"score": {Key: "score", Env: "SCORE_MODEL", Default: "default"}},
	}
	credentials := map[string]string{"VENDOR_A_URL": "https://a.example", "VENDOR_A_KEY": "key-a", "VENDOR_B_URL": "https://b.example", "VENDOR_B_KEY": "key-b"}
	resolver := NewCatalogResolver(fakeOverrides{"score": "admin-preset"}, catalog, map[string]string{"SCORE_MODEL": "default-preset"}, func(name string) string { return credentials[name] })
	got, err := resolver.Resolve(context.Background(), "score")
	if err != nil || got.Key != "admin-preset" || got.Service != "vendor-b" || got.Name != "model-b" || got.BaseURL != "https://b.example" || string(got.APIKey) != "key-b" || got.Extra["thinking"] != "off" {
		t.Fatalf("admin override resolution = %#v: %v", got, err)
	}

	resolver = NewCatalogResolver(fakeOverrides{}, catalog, map[string]string{"SCORE_MODEL": "default-preset"}, func(name string) string { return credentials[name] })
	got, err = resolver.Resolve(context.Background(), "score")
	if err != nil || got.Key != "default-preset" || got.Name != "model-a" {
		t.Fatalf("environment resolution = %#v: %v", got, err)
	}

	catalog.Defaults["score"] = "default"
	resolver = NewCatalogResolver(fakeOverrides{}, catalog, nil, func(name string) string {
		if name == "LLM_MODEL" {
			return "deployment-model"
		}
		if name == "LLM_BASE_URL" {
			return "https://deployment.example"
		}
		if name == "LLM_API_KEY" {
			return "deployment-key"
		}
		return ""
	})
	got, err = resolver.Resolve(context.Background(), "score")
	if err != nil || got.Key != "default" || got.Service != "llm" || got.Name != "deployment-model" || got.BaseURL != "https://deployment.example" || got.JSONMode != true {
		t.Fatalf("code default resolution = %#v: %v", got, err)
	}
}

func TestCatalogResolverAllowsOptionalVisionAndEnforcesRequiredVision(t *testing.T) {
	catalog := Catalog{
		Presets: map[string]Preset{
			"text":   {Service: "vendor", Model: "text-model", BaseURLEnv: "VENDOR_URL", APIKeyEnv: "VENDOR_KEY"},
			"vision": {Service: "vendor", Model: "vision-model", BaseURLEnv: "VENDOR_URL", APIKeyEnv: "VENDOR_KEY", Vision: true},
		},
		Capabilities: map[string]Capability{
			"understand": {Key: "understand", Env: "UNDERSTAND_MODEL", Default: "default"},
			"image":      {Key: "image", Env: "IMAGE_MODEL", Default: "default", Vision: true},
		},
	}
	credentials := func(name string) string {
		if name == "VENDOR_URL" {
			return "https://vendor.example"
		}
		if name == "VENDOR_KEY" {
			return "key"
		}
		return ""
	}

	resolver := NewCatalogResolver(fakeOverrides{"understand": "vision", "image": "vision"}, catalog, nil, credentials)
	got, err := resolver.Resolve(context.Background(), "understand")
	if err != nil || got.Key != "vision" || !got.Vision {
		t.Fatalf("optional vision model resolution = %#v: %v", got, err)
	}
	client := NewClient(false, resolver, nil, nil)
	supported, err := client.SupportsVision(context.Background(), "understand")
	if err != nil || !supported {
		t.Fatalf("understand vision support = %t: %v", supported, err)
	}

	got, err = resolver.Resolve(context.Background(), "image")
	if err != nil || got.Key != "vision" || !got.Vision {
		t.Fatalf("required vision model resolution = %#v: %v", got, err)
	}

	resolver = NewCatalogResolver(fakeOverrides{"image": "text"}, catalog, nil, credentials)
	if _, err := resolver.Resolve(context.Background(), "image"); err == nil {
		t.Fatal("required vision capability accepted a text-only model")
	}
}
