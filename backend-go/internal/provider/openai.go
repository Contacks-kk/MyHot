package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"aihot/backend-go/internal/modelconfig"
)

type Config struct {
	BaseURL     string
	APIKey      string
	Model       string
	System      string
	Images      []string
	Temperature *float64
	MaxTokens   int
	Timeout     time.Duration
	Extra       map[string]any
	DisableJSON bool
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type ChatFunc func(context.Context, Config, string) (Completion, error)

type Completion struct {
	Text         string
	Usage        Usage
	UsageJSON    json.RawMessage
	Response     json.RawMessage
	RequestID    string
	FinishReason string
}

// Error exposes a safe message, without provider bodies, URLs, or credentials.
type Error struct {
	Message             string
	Retryable           bool
	definitiveRejection bool
}

func (e *Error) Error() string { return e.Message }

func (e *Error) ProviderRejected() bool { return e.definitiveRejection }

func (e *Error) SafeMessage() string { return e.Message }

func Chat(ctx context.Context, cfg Config, prompt string) (Completion, error) {
	return ChatWithOptions(ctx, cfg, prompt)
}

func ChatWithOptions(ctx context.Context, cfg Config, prompt string) (Completion, error) {
	base, err := modelconfig.ValidateBaseURL(ctx, cfg.BaseURL)
	if err != nil {
		return Completion{}, &Error{Message: err.Error(), definitiveRejection: true}
	}
	u := *base
	u.Path = strings.TrimRight(base.Path, "/") + "/chat/completions"
	u.RawPath = ""
	messages := make([]map[string]any, 0, 2)
	if cfg.System != "" {
		messages = append(messages, map[string]any{"role": "system", "content": cfg.System})
	}
	content, err := userMessageContent(prompt, cfg.Images)
	if err != nil {
		return Completion{}, &Error{Message: err.Error(), definitiveRejection: true}
	}
	messages = append(messages, map[string]any{"role": "user", "content": content})
	temperature := 0.2
	if cfg.Temperature != nil {
		temperature = *cfg.Temperature
	}
	body := map[string]any{"model": cfg.Model, "messages": messages, "temperature": temperature}
	if !cfg.DisableJSON {
		body["response_format"] = map[string]string{"type": "json_object"}
	}
	if cfg.MaxTokens > 0 {
		body["max_tokens"] = cfg.MaxTokens
	}
	for key, value := range cfg.Extra {
		body[key] = value
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Completion{}, &Error{Message: "模型请求无法编码", definitiveRejection: true}
	}
	transport := &http.Transport{DialContext: modelconfig.DialPublic, TLSHandshakeTimeout: 8 * time.Second, ResponseHeaderTimeout: 45 * time.Second, DisableKeepAlives: true}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	client := &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(payload))
	if err != nil {
		return Completion{}, &Error{Message: "模型请求配置无效", definitiveRejection: true}
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return Completion{}, &Error{Message: "模型服务连接失败", Retryable: true, definitiveRejection: requestNotSent(err)}
	}
	defer resp.Body.Close()
	completion, err := readCompletion(resp)
	if completion.RequestID == "" {
		completion.RequestID = resp.Header.Get("x-request-id")
	}
	return completion, err
}

// userMessageContent emits the ordinary text form unless inline vision material
// is present. Image URLs are restricted to bounded data URLs so the provider
// adapter never introduces a second, unvalidated network fetch.
func userMessageContent(prompt string, images []string) (any, error) {
	if len(images) == 0 {
		return prompt, nil
	}
	if err := ValidateImages(images); err != nil {
		return nil, err
	}
	parts := make([]map[string]any, 0, len(images)+1)
	parts = append(parts, map[string]any{"type": "text", "text": prompt})
	for _, image := range images {
		parts = append(parts, map[string]any{
			"type": "image_url", "image_url": map[string]string{"url": image},
		})
	}
	return parts, nil
}

func ValidateImages(images []string) error {
	if len(images) > 4 {
		return errors.New("模型图片数量超出限制")
	}
	for _, image := range images {
		if !validInlineImage(image) {
			return errors.New("模型图片格式无效")
		}
	}
	return nil
}

func validInlineImage(value string) bool {
	if len(value) > 16<<20 {
		return false
	}
	for _, prefix := range []string{"data:image/jpeg;base64,", "data:image/png;base64,", "data:image/webp;base64,"} {
		if strings.HasPrefix(value, prefix) {
			decoded, err := base64.StdEncoding.DecodeString(value[len(prefix):])
			return err == nil && len(decoded) > 0
		}
	}
	return false
}

func readCompletion(resp *http.Response) (Completion, error) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Completion{}, &Error{Message: fmt.Sprintf("模型服务返回 HTTP %d", resp.StatusCode), Retryable: resp.StatusCode == 408 || resp.StatusCode == 429 || resp.StatusCode >= 500, definitiveRejection: true}
	}
	const maxResponseBytes = 2 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return Completion{}, &Error{Message: "模型服务响应无法读取", Retryable: true}
	}
	if len(body) > maxResponseBytes {
		return Completion{}, &Error{Message: "模型服务响应过大"}
	}
	output, err := ParseCompletion(body)
	if err != nil {
		if json.Valid(body) {
			output.Response = append(json.RawMessage(nil), body...)
		} else {
			output.Response, _ = json.Marshal(map[string]string{"unparsable": string(body[:min(len(body), 20000)])})
		}
		return output, &Error{Message: "模型服务返回格式不兼容"}
	}
	output.Response = append(json.RawMessage(nil), body...)
	if output.Text == "" {
		return output, &Error{Message: "模型服务返回了空内容", Retryable: true}
	}
	return output, nil
}

func ParseCompletion(raw []byte) (Completion, error) {
	var parsed struct {
		ID      string `json:"id"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Completion{}, err
	}
	if len(parsed.Choices) == 0 {
		return Completion{}, errors.New("model response has no choices")
	}
	var usage Usage
	if len(parsed.Usage) > 0 && string(parsed.Usage) != "null" {
		_ = json.Unmarshal(parsed.Usage, &usage)
	}
	return Completion{
		Text: strings.TrimSpace(parsed.Choices[0].Message.Content), Usage: usage,
		UsageJSON: append(json.RawMessage(nil), parsed.Usage...), RequestID: parsed.ID,
		FinishReason: parsed.Choices[0].FinishReason,
	}, nil
}

func requestNotSent(err error) bool {
	var networkError *net.OpError
	if errors.As(err, &networkError) && networkError.Op == "dial" {
		return true
	}
	var dnsError *net.DNSError
	return errors.As(err, &dnsError)
}
