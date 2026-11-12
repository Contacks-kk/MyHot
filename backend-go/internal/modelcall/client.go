package modelcall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"aihot/backend-go/internal/provider"
	"aihot/backend-go/internal/receipts"
)

type Model struct {
	Key             string
	Service         string
	Name            string
	BaseURL         string
	APIKey          []byte
	Extra           map[string]any
	ReasoningTokens int
	JSONMode        bool
	Vision          bool
}

type Resolver interface {
	Resolve(context.Context, string) (Model, error)
}

type KeyResolver interface {
	ResolveKey(context.Context, string) (Model, error)
}

type OverrideReader interface {
	Overrides(context.Context) (map[string]string, error)
}

type Capability struct {
	Key, Env, Default string
	// Vision means this capability requires an image-capable model. A false
	// value still permits selecting one for optional image input.
	Vision bool
}

type Preset struct {
	Service, Model, BaseURLEnv, APIKeyEnv string
	Extra                                 map[string]any
	ReasoningTokens                       int
	JSONMode, Vision                      bool
}

type Catalog struct {
	Presets      map[string]Preset
	PresetOrder  []string
	Defaults     map[string]string
	Capabilities map[string]Capability
}

type CredentialLookup func(string) string

type CatalogResolver struct {
	overrides   OverrideReader
	catalog     Catalog
	environment map[string]string
	credential  CredentialLookup
}

func NewCatalogResolver(overrides OverrideReader, catalog Catalog, environment map[string]string, credential CredentialLookup) *CatalogResolver {
	if credential == nil {
		credential = func(string) string { return "" }
	}
	return &CatalogResolver{overrides: overrides, catalog: catalog, environment: environment, credential: credential}
}

func (r *CatalogResolver) Resolve(ctx context.Context, capabilityKey string) (Model, error) {
	capability, ok := r.catalog.Capabilities[capabilityKey]
	if !ok {
		return Model{}, errors.New("unknown model capability")
	}
	selected := capability.Default
	if configured := r.catalog.Defaults[capabilityKey]; configured != "" {
		selected = configured
	}
	if !validModel(r.catalog, selected) {
		selected = "default"
	}
	if r.overrides != nil {
		overrides, err := r.overrides.Overrides(ctx)
		if err != nil {
			return Model{}, err
		}
		if value := overrides[capabilityKey]; value != "" && validModel(r.catalog, value) {
			selected = value
		} else if value := r.environment[capability.Env]; value != "" && validModel(r.catalog, value) {
			selected = value
		}
	} else if value := r.environment[capability.Env]; value != "" && validModel(r.catalog, value) {
		selected = value
	}

	model, err := r.ResolveKey(ctx, selected)
	if err != nil {
		return Model{}, err
	}
	// A capability's Vision flag means that images are required for this call.
	// Text capabilities may still select a vision-capable model; callers can
	// then add images opportunistically without excluding that model from the
	// normal text path.
	if capability.Vision && !model.Vision {
		return Model{}, errors.New("selected model does not support the required capability")
	}
	return model, nil
}

func (r *CatalogResolver) ResolveKey(_ context.Context, key string) (Model, error) {
	if key == "default" {
		modelName := r.credential("LLM_MODEL")
		extra, err := extraFromCredential(r.credential("LLM_EXTRA_JSON"))
		if err != nil {
			return Model{}, err
		}
		reasoningTokens, err := reasoningTokensFromCredential(r.credential("LLM_REASONING_TOKENS"))
		if err != nil {
			return Model{}, err
		}
		model := Model{Key: "default", Service: "llm", Name: modelName, BaseURL: r.credential("LLM_BASE_URL"),
			APIKey: []byte(r.credential("LLM_API_KEY")), Extra: extra, ReasoningTokens: reasoningTokens,
			JSONMode: r.credential("LLM_JSON_MODE") != "false", Vision: r.credential("LLM_VISION") == "true"}
		return model, nil
	}
	preset, ok := r.catalog.Presets[key]
	if !ok {
		return Model{}, errors.New("selected model preset is missing")
	}
	return Model{Key: key, Service: preset.Service, Name: preset.Model, BaseURL: r.credential(preset.BaseURLEnv),
		APIKey: []byte(r.credential(preset.APIKeyEnv)), Extra: preset.Extra, ReasoningTokens: preset.ReasoningTokens,
		JSONMode: preset.JSONMode, Vision: preset.Vision}, nil
}

func validModel(catalog Catalog, key string) bool {
	return key == "default" || catalog.Presets[key].Model != ""
}

func extraFromCredential(value string) (map[string]any, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	var extra map[string]any
	if err := json.Unmarshal([]byte(value), &extra); err != nil || extra == nil {
		return nil, errors.New("LLM_EXTRA_JSON must be a JSON object")
	}
	return extra, nil
}

func reasoningTokensFromCredential(value string) (int, error) {
	if strings.TrimSpace(value) == "" {
		return 0, nil
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, errors.New("LLM_REASONING_TOKENS must be a non-negative integer")
		}
	}
	count, err := strconv.Atoi(value)
	if err != nil || count < 0 {
		return 0, errors.New("LLM_REASONING_TOKENS must be a non-negative integer")
	}
	return count, nil
}

type Request struct {
	Capability    string
	ModelKey      string
	Purpose       string
	Subject       string
	PromptVersion string
	System        string
	User          string
	Images        []string
	Temperature   *float64
	MaxTokens     int
	Timeout       time.Duration
	AttemptTag    string
	DisableJSON   bool
}

type Result struct {
	Capability string
	Model      string
	ReceiptID  int64
	Reused     bool
	Completion provider.Completion
}

type OutputError struct {
	ReceiptID int64
	Message   string
}

func (e *OutputError) Error() string { return e.Message }

type ChatFunc func(context.Context, provider.Config, string) (provider.Completion, error)

type Client struct {
	Enabled  bool
	Resolver Resolver
	Receipts *receipts.Service
	Chat     ChatFunc
}

func NewClient(enabled bool, resolver Resolver, receiptService *receipts.Service, chat ChatFunc) *Client {
	if chat == nil {
		chat = provider.ChatWithOptions
	}
	return &Client{Enabled: enabled, Resolver: resolver, Receipts: receiptService, Chat: chat}
}

// ResolveKey returns the configured model catalog key without making a provider request.
func (c *Client) ResolveKey(ctx context.Context, capability string) (string, error) {
	if c.Resolver == nil {
		return "", errors.New("model resolver is not configured")
	}
	model, err := c.Resolver.Resolve(ctx, capability)
	if err != nil {
		return "", err
	}
	return model.Key, nil
}

// SupportsVision resolves a capability without returning provider credentials to its caller.
func (c *Client) SupportsVision(ctx context.Context, capability string) (bool, error) {
	if c.Resolver == nil {
		return false, errors.New("model resolver is not configured")
	}
	model, err := c.Resolver.Resolve(ctx, capability)
	if err != nil {
		return false, err
	}
	return model.Vision, nil
}

func (c *Client) Call(ctx context.Context, request Request) (Result, error) {
	if !c.Enabled {
		return Result{}, errors.New("model calls are disabled")
	}
	if c.Resolver == nil || c.Receipts == nil || c.Chat == nil || request.Capability == "" || request.Purpose == "" {
		return Result{}, errors.New("model request is incomplete")
	}
	var (
		model Model
		err   error
	)
	if request.ModelKey != "" {
		resolver, ok := c.Resolver.(KeyResolver)
		if !ok {
			return Result{}, errors.New("model resolver does not support explicit model keys")
		}
		model, err = resolver.ResolveKey(ctx, request.ModelKey)
	} else {
		model, err = c.Resolver.Resolve(ctx, request.Capability)
	}
	if err != nil {
		return Result{}, err
	}
	if len(request.Images) > 0 && !model.Vision {
		return Result{}, errors.New("selected model does not support image input")
	}
	if err := provider.ValidateImages(request.Images); err != nil {
		return Result{}, err
	}
	if model.Name == "" || model.Service == "" || model.BaseURL == "" || len(model.APIKey) == 0 {
		return Result{}, fmt.Errorf("model capability %s is not configured", request.Capability)
	}
	secret := append([]byte(nil), model.APIKey...)
	defer clear(secret)

	temperature := 0.2
	if request.Temperature != nil {
		temperature = *request.Temperature
	}
	maxTokens := request.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 1500
	}
	if maxTokens < 512 {
		maxTokens = 512
	}
	maxTokens += model.ReasoningTokens
	if maxTokens < 512 || maxTokens > 1_000_000 {
		return Result{}, errors.New("model token limit is invalid")
	}
	if request.Timeout <= 0 {
		request.Timeout = 120 * time.Second
	}
	systemHash := hashText(request.System)
	userHash := hashText(request.User)
	imageHashes := make([]string, len(request.Images))
	for index, image := range request.Images {
		imageHashes[index] = hashText(image)
	}
	identity := map[string]any{
		"model": model.Name, "promptVersion": request.PromptVersion, "system": systemHash,
		"user": userHash, "temperature": temperature, "maxTokens": maxTokens, "extra": model.Extra,
	}
	// The TypeScript receipt identity did not include an images field for text-only calls.
	// Keep those logical keys stable while still distinguishing Go vision requests.
	if len(imageHashes) > 0 {
		identity["images"] = imageHashes
	}
	summary := map[string]any{
		"promptVersion": request.PromptVersion, "systemHash": systemHash, "userHash": userHash,
		"userChars": utf16Length(request.User), "temperature": temperature, "maxTokens": maxTokens,
		"imageCount": len(imageHashes), "imageHashes": imageHashes,
	}
	paidRequest := receipts.Request{
		Service: model.Service, Model: model.Name, Purpose: request.Purpose, Subject: request.Subject,
		Identity: identity, Summary: summary, AttemptTag: request.AttemptTag,
	}
	paid, err := c.Receipts.Do(ctx, paidRequest, func(callCtx context.Context) (receipts.Outcome, error) {
		completion, callErr := c.Chat(callCtx, provider.Config{
			BaseURL: model.BaseURL, APIKey: string(secret), Model: model.Name, System: request.System,
			Images: request.Images, Temperature: &temperature, MaxTokens: maxTokens, Timeout: request.Timeout, Extra: model.Extra,
			DisableJSON: request.DisableJSON || !model.JSONMode,
		}, request.User)
		if callErr != nil && len(completion.Response) == 0 {
			return receipts.Outcome{}, callErr
		}
		if len(completion.Response) == 0 {
			return receipts.Outcome{}, errors.New("model response was empty")
		}
		return receipts.Outcome{Response: completion.Response, RequestID: completion.RequestID, Usage: completion.UsageJSON}, nil
	})
	if err != nil {
		return Result{}, err
	}
	completion, err := provider.ParseCompletion(paid.Response)
	if err != nil || strings.TrimSpace(completion.Text) == "" {
		message := "model returned an unusable response"
		if err != nil {
			message = "model returned an incompatible response"
		}
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		_ = c.Receipts.Reject(persistCtx, paid.ReceiptID, "unusable output: "+message)
		cancel()
		return Result{}, &OutputError{ReceiptID: paid.ReceiptID, Message: message}
	}
	return Result{Capability: request.Capability, Model: model.Key, ReceiptID: paid.ReceiptID, Reused: paid.Reused, Completion: completion}, nil
}

func (c *Client) Complete(ctx context.Context, receiptID int64) error {
	return c.Receipts.Complete(ctx, receiptID)
}

func (c *Client) Reject(ctx context.Context, receiptID int64, reason string) error {
	return c.Receipts.Reject(ctx, receiptID, reason)
}

func hashText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func utf16Length(value string) int { return len(utf16.Encode([]rune(value))) }
