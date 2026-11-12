package media

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gen2brain/webp"
)

type fakeImageDoer struct {
	body   []byte
	type_  string
	status int
	calls  atomic.Int64
}

func (d *fakeImageDoer) Do(req *http.Request) (*http.Response, error) {
	d.calls.Add(1)
	return &http.Response{
		StatusCode: d.status,
		Header:     http.Header{"Content-Type": []string{d.type_}},
		Body:       io.NopCloser(bytes.NewReader(d.body)),
		Request:    req,
	}, nil
}

func TestHandlerVerifiesSignatureCachesImageAndReturns304(t *testing.T) {
	const secret = "test-image-signature-secret"
	body := testPNG(t, 8, 4)
	doer := &fakeImageDoer{body: body, type_: "image/png", status: http.StatusOK}
	service := NewService(Config{DataDir: t.TempDir(), SignSecret: secret}, nil, doer)
	handler := NewHandler(service, nil)
	requestURL := signedRequestURL("https://images.example.invalid/test.png", "thumb", secret)

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, requestURL, nil))
	if first.Code != http.StatusOK || first.Header().Get("X-Img-Proxy-Sig") != "valid" || first.Header().Get("Content-Type") != "image/webp" {
		t.Fatalf("first response status=%d headers=%v body=%q", first.Code, first.Header(), first.Body.String())
	}
	if !strings.HasPrefix(first.Header().Get("Cache-Control"), "public, max-age=") || first.Header().Get("ETag") == "" {
		t.Fatalf("cache headers missing: %v", first.Header())
	}
	if doer.calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1", doer.calls.Load())
	}

	secondRequest := httptest.NewRequest(http.MethodGet, requestURL, nil)
	secondRequest.Header.Set("If-None-Match", first.Header().Get("ETag"))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, secondRequest)
	if second.Code != http.StatusNotModified || second.Body.Len() != 0 || second.Header().Get("ETag") != first.Header().Get("ETag") {
		t.Fatalf("conditional response status=%d headers=%v body=%q", second.Code, second.Header(), second.Body.String())
	}
	if doer.calls.Load() != 1 {
		t.Fatalf("cache miss on second request; upstream calls = %d", doer.calls.Load())
	}
}

func TestHandlerRejectsInvalidSignatureBeforeFetching(t *testing.T) {
	doer := &fakeImageDoer{body: testPNG(t, 2, 2), type_: "image/png", status: http.StatusOK}
	service := NewService(Config{DataDir: t.TempDir(), SignSecret: "expected-secret"}, nil, doer)
	handler := NewHandler(service, nil)
	requestURL := signedRequestURL("https://images.example.invalid/test.png", "full", "wrong-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, requestURL, nil))
	if response.Code != http.StatusForbidden || response.Body.String() != "forbidden\n" || response.Header().Get("X-Img-Proxy-Sig") != "invalid" {
		t.Fatalf("response status=%d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
	}
	if doer.calls.Load() != 0 {
		t.Fatalf("invalid signature triggered %d upstream requests", doer.calls.Load())
	}
}

func TestHandlerReportsUpstreamBudgetLimit(t *testing.T) {
	doer := &fakeImageDoer{body: testPNG(t, 2, 2), type_: "image/png", status: http.StatusOK}
	service := NewService(Config{DataDir: t.TempDir(), SignSecret: "secret", MinuteBudgetBytes: 1}, nil, doer)
	handler := NewHandler(service, nil)
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, signedRequestURL("https://images.example.invalid/one.png", "thumb", "secret"), nil))
	if first.Code != http.StatusOK {
		t.Fatalf("first response status=%d body=%q", first.Code, first.Body.String())
	}
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodGet, signedRequestURL("https://images.example.invalid/two.png", "thumb", "secret"), nil))
	if second.Code != http.StatusServiceUnavailable || second.Header().Get("Retry-After") != "60" || second.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("budget response status=%d headers=%v body=%q", second.Code, second.Header(), second.Body.String())
	}
	if doer.calls.Load() != 1 {
		t.Fatalf("budget-exceeded request reached upstream; calls=%d", doer.calls.Load())
	}
}

func TestGIFPreparationQueueAndWorkerAreIdempotent(t *testing.T) {
	body := testGIF(t)
	doer := &fakeImageDoer{body: body, type_: "image/gif", status: http.StatusOK}
	service := NewService(Config{DataDir: t.TempDir(), SignSecret: "secret"}, nil, doer)
	var enqueued atomic.Int64
	handler := NewHandler(service, func(context.Context, string, string) error {
		enqueued.Add(1)
		return nil
	})
	url := signedRequestURL("https://images.example.invalid/animated.gif", "thumb", "secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, url, nil))
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "image/gif" || response.Header().Get("Cache-Control") != "public, max-age=60, s-maxage=60" {
		t.Fatalf("GIF response status=%d headers=%v", response.Code, response.Header())
	}
	if enqueued.Load() != 1 {
		t.Fatalf("queued preparations = %d, want 1", enqueued.Load())
	}

	parsed, _ := urlpkgParse(url)
	query := parsed.Query()
	if _, err := service.ConvertAnimated(context.Background(), query.Get("u"), query.Get("mode")); err != nil {
		t.Fatalf("ConvertAnimated: %v", err)
	}
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodGet, url, nil))
	if second.Code != http.StatusOK || second.Header().Get("Cache-Control") == "no-store" {
		t.Fatalf("prepared GIF response status=%d headers=%v", second.Code, second.Header())
	}
	if enqueued.Load() != 1 || doer.calls.Load() != 1 {
		t.Fatalf("preparation repeated: queue=%d fetch=%d", enqueued.Load(), doer.calls.Load())
	}
}

func TestResizeImageSupportsSVGAndAvatarCrop(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 2 1"><rect width="2" height="1" fill="#f00"/></svg>`)
	unchanged, err := resizeImage(svg, "image/svg+xml", "full")
	if err != nil || unchanged.Type != "image/svg+xml" || !bytes.Equal(unchanged.Body, svg) {
		t.Fatalf("small SVG result type=%q err=%v", unchanged.Type, err)
	}
	avatar, err := resizeImage(svg, "image/svg+xml", "avatar")
	if err != nil || avatar.Type != "image/webp" {
		t.Fatalf("SVG avatar type=%q err=%v", avatar.Type, err)
	}
	cfg, err := webp.DecodeConfig(bytes.NewReader(avatar.Body))
	if err != nil || cfg.Width != 96 || cfg.Height != 96 {
		t.Fatalf("avatar dimensions=%dx%d err=%v", cfg.Width, cfg.Height, err)
	}
}

func signedRequestURL(raw, mode, secret string) string {
	exp := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(raw + "|" + mode + "|" + exp))
	query := url.Values{"u": {raw}, "mode": {mode}, "exp": {exp}, "sig": {hex.EncodeToString(mac.Sum(nil))[:16]}}
	return "/api/img-proxy?" + query.Encode()
}

func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 30), G: uint8(y * 50), B: 180, A: 255})
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func testGIF(t *testing.T) []byte {
	t.Helper()
	frame := func(c color.Color) *image.Paletted {
		img := image.NewPaletted(image.Rect(0, 0, 8, 8), color.Palette{color.Transparent, c})
		for y := 0; y < 8; y++ {
			for x := 0; x < 8; x++ {
				img.SetColorIndex(x, y, 1)
			}
		}
		return img
	}
	anim := &gif.GIF{Image: []*image.Paletted{frame(color.RGBA{R: 255, A: 255}), frame(color.RGBA{B: 255, A: 255})}, Delay: []int{4, 6}, LoopCount: 0}
	var out bytes.Buffer
	if err := gif.EncodeAll(&out, anim); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func urlpkgParse(raw string) (*url.URL, error) { return url.Parse(raw) }
