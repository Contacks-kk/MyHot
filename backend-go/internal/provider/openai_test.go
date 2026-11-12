package provider

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestUserMessageContentAddsOnlyValidatedInlineImages(t *testing.T) {
	image := "data:image/png;base64,aGVsbG8="
	content, err := userMessageContent("read this image", []string{image})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	var parts []map[string]any
	if err := json.Unmarshal(encoded, &parts); err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 || parts[0]["type"] != "text" || parts[0]["text"] != "read this image" || parts[1]["type"] != "image_url" {
		t.Fatalf("message parts = %s", encoded)
	}
	imageURL, ok := parts[1]["image_url"].(map[string]any)
	if !ok || imageURL["url"] != image {
		t.Fatalf("image part = %s", encoded)
	}

	plain, err := userMessageContent("plain text", nil)
	if err != nil || plain != "plain text" {
		t.Fatalf("text-only content = %#v, %v", plain, err)
	}
	for _, invalid := range [][]string{{"https://images.example.invalid/a.png"}, {"data:image/svg+xml;base64,PHN2Zz4="}, {"data:image/png;base64,%%%"}} {
		if _, err := userMessageContent("prompt", invalid); err == nil {
			t.Errorf("invalid images accepted: %#v", invalid)
		}
	}
}

func TestCompletionErrorsDoNotExposeProviderBody(t *testing.T) {
	for _, code := range []int{301, 400, 401, 429, 502} {
		_, err := readCompletion(&http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader("secret-provider-key"))})
		var safe *Error
		if !errors.As(err, &safe) || strings.Contains(err.Error(), "secret") {
			t.Fatalf("status %d: unsafe error %v", code, err)
		}
		wantRetry := code == 429 || code == 502
		if safe.Retryable != wantRetry {
			t.Fatalf("status %d: retryable %t, want %t", code, safe.Retryable, wantRetry)
		}
	}
}

func TestCompletionUsageAndMalformedResponses(t *testing.T) {
	body := `{"choices":[{"message":{"content":"{\"ok\":true}"}}],"usage":{"prompt_tokens":13,"completion_tokens":4,"total_tokens":17}}`
	got, err := readCompletion(&http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))})
	if err != nil || got.Usage.TotalTokens != 17 || got.Text != `{"ok":true}` {
		t.Fatalf("completion = %+v, %v", got, err)
	}
	for _, body := range []string{"invalid secret", `{"choices":[]}`, strings.Repeat("x", (2<<20)+1)} {
		_, err := readCompletion(&http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))})
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unexpected response error %v", err)
		}
	}
}
