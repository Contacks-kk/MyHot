package mediaurl

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProxyURLMatchesSignedImageProxyContract(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 123_000_000, time.UTC)
	raw := "https://images.example.invalid/a b.jpg?x=hello&tag=!~'()*"
	secret := "local-test-secret"
	got, err := ProxyURL(&raw, "full", now, 48*time.Hour, secret)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected a proxied image URL")
	}
	parsed, err := url.Parse(*got)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if parsed.Path != "/api/img-proxy" || query.Get("u") != raw || query.Get("mode") != "full" {
		t.Fatalf("proxy URL = %q", *got)
	}
	expectedExpiry := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC).Unix()
	if query.Get("exp") != strconv.FormatInt(expectedExpiry, 10) {
		t.Fatalf("expiry = %q, expected %d", query.Get("exp"), expectedExpiry)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(raw + "|full|" + query.Get("exp")))
	if query.Get("sig") != hex.EncodeToString(mac.Sum(nil))[:16] {
		t.Fatalf("signature = %q", query.Get("sig"))
	}
	if !strings.Contains(*got, "a%20b.jpg%3Fx%3Dhello%26tag%3D!~'()*") {
		t.Fatalf("u is not encodeURIComponent-compatible: %q", *got)
	}

	modes := []string{"image-336", "image-720"}
	widths := []int{336, 720}
	set, err := ProxyURLSet(&raw, modes, widths, now, 48*time.Hour, secret)
	if err != nil || set == nil {
		t.Fatalf("ProxyURLSet = %v, %v", set, err)
	}
	repeated, err := ProxyURLSet(&raw, modes, widths, now, 48*time.Hour, secret)
	if err != nil || repeated == nil || *repeated != *set {
		t.Fatalf("responsive URLs changed for the same input and expiry: %v, %v", repeated, err)
	}
	for index, candidate := range strings.Split(*set, ", ") {
		parts := strings.Split(candidate, " ")
		parsedCandidate, err := url.Parse(parts[0])
		if err != nil || len(parts) != 2 || parts[1] != strconv.Itoa(widths[index])+"w" {
			t.Fatalf("responsive candidate %q is malformed: %v", candidate, err)
		}
		candidateQuery := parsedCandidate.Query()
		if candidateQuery.Get("u") != raw || candidateQuery.Get("mode") != modes[index] || candidateQuery.Get("exp") != strconv.FormatInt(expectedExpiry, 10) {
			t.Fatalf("responsive candidate %q changed its signed inputs", candidate)
		}
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(raw + "|" + modes[index] + "|" + candidateQuery.Get("exp")))
		if candidateQuery.Get("sig") != hex.EncodeToString(mac.Sum(nil))[:16] {
			t.Fatalf("responsive candidate %q has an invalid signature", candidate)
		}
	}
}

func TestProxyURLSkipsInvalidAndTrackingImages(t *testing.T) {
	now := time.Now()
	for _, raw := range []string{"", "javascript:alert(1)", "https://secure.adnxs.com/pixel", "https://youtube.com/watch?v=1"} {
		if result, err := ProxyURL(&raw, "full", now, 48*time.Hour, ""); err != nil || result != nil {
			t.Errorf("ProxyURL(%q) = %v, %v", raw, result, err)
		}
	}
	data := "data:image/png;base64,AA=="
	if result, err := ProxyURL(&data, "full", now, 48*time.Hour, ""); err != nil || result == nil || *result != data {
		t.Fatalf("data URI = %v, %v", result, err)
	}
	if result, err := ProxyURLSet(&data, []string{"image-720"}, []int{720}, now, 48*time.Hour, ""); err != nil || result != nil {
		t.Fatalf("data URI srcset = %v, %v", result, err)
	}
}

func TestVerifyProxyRequestAcceptsCurrentAndLegacySignatures(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	const secret = "local-test-secret"
	raw := "https://images.example.invalid/a.jpg"
	expiry := strconv.FormatInt(now.Add(time.Hour).Unix(), 10)
	for _, signatureLength := range []int{16, 64} {
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(raw + "|thumb|" + expiry))
		sig := hex.EncodeToString(mac.Sum(nil))[:signatureLength]
		params := url.Values{"u": {raw}, "mode": {"thumb"}, "exp": {expiry}, "sig": {sig}}
		verified := VerifyProxyRequest(params, now, secret)
		if !verified.OK || verified.URL != raw || verified.Mode != "thumb" {
			t.Fatalf("signature length %d was not accepted: %#v", signatureLength, verified)
		}
	}

	// JavaScript's existing verifier accepts every 9-11 digit epoch without overflowing.
	farExpiry := "99999999999"
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(raw + "|full|" + farExpiry))
	params := url.Values{"u": {raw}, "mode": {"full"}, "exp": {farExpiry}, "sig": {hex.EncodeToString(mac.Sum(nil))[:16]}}
	if result := VerifyProxyRequest(params, now, secret); !result.OK {
		t.Fatalf("11-digit expiry was not accepted: %#v", result)
	}

	// Older URLs omitted mode but used the literal "default" in the signed message.
	mac = hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(raw + "|default|" + expiry))
	params = url.Values{"u": {raw}, "exp": {expiry}, "sig": {hex.EncodeToString(mac.Sum(nil))[:16]}}
	verified := VerifyProxyRequest(params, now, secret)
	if !verified.OK || verified.Mode != "full" {
		t.Fatalf("mode-less legacy URL did not resolve to full: %#v", verified)
	}
}

func TestVerifyProxyRequestClassifiesInvalidRequests(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		params url.Values
		secret string
		want   string
	}{
		{name: "missing", params: url.Values{}, secret: "secret", want: "missing"},
		{name: "bad expiry", params: url.Values{"u": {"https://images.example.invalid/a.jpg"}, "mode": {"full"}, "exp": {"soon"}, "sig": {"abc"}}, secret: "secret", want: "missing"},
		{name: "expired", params: url.Values{"u": {"https://images.example.invalid/a.jpg"}, "mode": {"full"}, "exp": {"1700000000"}, "sig": {"0123456789abcdef"}}, secret: "secret", want: "expired"},
		{name: "bad url", params: url.Values{"u": {"file:///etc/passwd"}, "mode": {"full"}, "exp": {"1799999999"}, "sig": {"0123456789abcdef"}}, secret: "secret", want: "bad-url"},
		{name: "bad signature", params: url.Values{"u": {"https://images.example.invalid/a.jpg"}, "mode": {"full"}, "exp": {"1799999999"}, "sig": {"0123456789abcdef"}}, secret: "secret", want: "bad-signature"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if result := VerifyProxyRequest(test.params, now, test.secret); result.OK || result.Reason != test.want {
				t.Fatalf("result = %#v, want reason %q", result, test.want)
			}
		})
	}
}

func TestIsNonArticleImageAndBuildViewsFiltersAndDegradesSafely(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	small := float64(64)
	validW := float64(800)
	validH := float64(450)
	if !IsNonArticleImage("https://cdn.example.invalid/a.png", &small, &small) {
		t.Fatal("expected small 64x64 image to be filtered")
	}
	if !IsNonArticleImage("https://cdn.example.invalid/avatar/u1.png", &validW, &validH) {
		t.Fatal("expected avatar image path to be filtered")
	}
	if IsNonArticleImage("https://cdn.example.invalid/posts/architecture.png", &validW, &validH) {
		t.Fatal("expected legitimate 800x450 photo to be kept")
	}

	raw := json.RawMessage(`[
		{"kind":"image","url":"https://cdn.example.invalid/avatar/author.png"},
		{"kind":"image","url":"https://cdn.example.invalid/posts/arch.png","width":800,"height":450,"alt":" 系统架构图 "},
		{"kind":"image","url":"https://cdn.example.invalid/posts/arch.png","width":800,"height":450},
		{"kind":"image","url":"https://cdn.example.invalid/posts/bench.png","width":900,"height":500}
	]`)

	if empty := BuildViews(raw, "image-336", now, "", 4); len(empty) != 0 {
		t.Fatalf("expected empty views without secret, got %#v", empty)
	}

	views := BuildViews(raw, "image-336", now, "local-test-secret", 4)
	if len(views) != 2 {
		t.Fatalf("expected 2 deduplicated valid views, got %#v", views)
	}
	if !strings.Contains(views[0].URL, "mode=image-336") || views[0].FullURL == nil || !strings.Contains(*views[0].FullURL, "mode=full") {
		t.Fatalf("unexpected signed proxy URLs: %#v", views[0])
	}
	if views[0].Alt == nil || *views[0].Alt != "系统架构图" || views[0].SrcSet == nil {
		t.Fatalf("unexpected alt/srcSet metadata: %#v", views[0])
	}

	capped := BuildViews(raw, "image-336", now, "local-test-secret", 1)
	if len(capped) != 1 {
		t.Fatalf("expected cap=1 to return 1 item, got %d", len(capped))
	}
}
