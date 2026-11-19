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
	"math/rand"
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
	bodies map[string][]byte
	type_  string
	status int
	delay  time.Duration
	calls  atomic.Int64
}

/** Do returns the configured image fixture and counts each upstream request. */
func (d *fakeImageDoer) Do(req *http.Request) (*http.Response, error) {
	d.calls.Add(1)
	if d.delay > 0 {
		time.Sleep(d.delay)
	}
	body := d.body
	if routeBody, exists := d.bodies[req.URL.Path]; exists {
		body = routeBody
	}
	return &http.Response{
		StatusCode: d.status,
		Header:     http.Header{"Content-Type": []string{d.type_}},
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    req,
	}, nil
}

/** TestProduceSharesOriginalAcrossModesAndCachesEachRendition verifies source reuse and disk caches. */
func TestProduceSharesOriginalAcrossModesAndCachesEachRendition(t *testing.T) {
	dataDir := t.TempDir()
	doer := &fakeImageDoer{body: testPNG(t, 1600, 800), type_: "image/png", status: http.StatusOK, delay: 25 * time.Millisecond}
	service := NewService(Config{DataDir: dataDir}, nil, doer)
	type result struct {
		mode  string
		image PreparedImage
		err   error
	}
	modes := []string{"thumb", "full", "avatar", "card"}
	start := make(chan struct{})
	results := make(chan result, len(modes))
	for _, mode := range modes {
		go func(mode string) {
			<-start
			image, err := service.Produce(context.Background(), "https://images.example.invalid/shared.png", mode)
			results <- result{mode: mode, image: image, err: err}
		}(mode)
	}
	close(start)
	wantWidths := map[string]int{"thumb": 720, "full": 1600, "avatar": 96, "card": 336}
	var thumb PreparedImage
	for range modes {
		current := <-results
		if current.err != nil {
			t.Fatalf("produce %s: %v", current.mode, current.err)
		}
		config, err := webp.DecodeConfig(bytes.NewReader(current.image.Body))
		if err != nil || config.Width != wantWidths[current.mode] {
			t.Fatalf("%s width=%d err=%v", current.mode, config.Width, err)
		}
		if current.mode == "thumb" {
			thumb = current.image
		}
	}
	if got := doer.calls.Load(); got != 1 {
		t.Fatalf("shared original requests = %d, want 1", got)
	}
	if cached, err := service.Produce(context.Background(), "https://images.example.invalid/shared.png", "thumb"); err != nil || !bytes.Equal(cached.Body, thumb.Body) {
		t.Fatalf("same-service disk cache differs: err=%v", err)
	}
	diskCacheDoer := &fakeImageDoer{body: testPNG(t, 8, 8), type_: "image/png", status: http.StatusOK}
	diskCacheService := NewService(Config{DataDir: dataDir}, nil, diskCacheDoer)
	if cached, err := diskCacheService.Produce(context.Background(), "https://images.example.invalid/shared.png", "thumb"); err != nil || !bytes.Equal(cached.Body, thumb.Body) {
		t.Fatalf("new-service disk cache differs: err=%v", err)
	}
	if got := diskCacheDoer.calls.Load(); got != 0 {
		t.Fatalf("disk cache miss made %d upstream requests", got)
	}
}

/** TestProduceReusesRecentOriginalAcrossSequentialModes verifies completed source reuse for responsive candidates. */
func TestProduceReusesRecentOriginalAcrossSequentialModes(t *testing.T) {
	doer := &fakeImageDoer{body: testPNG(t, 1600, 800), type_: "image/png", status: http.StatusOK}
	service := NewService(Config{DataDir: t.TempDir()}, nil, doer)
	for _, mode := range []string{"image-336", "image-720", "image-1200"} {
		image, err := service.Produce(context.Background(), "https://images.example.invalid/sequential.png", mode)
		if err != nil {
			t.Fatalf("produce %s: %v", mode, err)
		}
		if _, err := webp.DecodeConfig(bytes.NewReader(image.Body)); err != nil {
			t.Fatalf("decode %s rendition: %v", mode, err)
		}
	}
	if got := doer.calls.Load(); got != 1 {
		t.Fatalf("sequential responsive candidates fetched the original %d times, want 1", got)
	}
}

/** TestProduceCachesOriginalFailureAcrossModes avoids repeated requests for one failed source. */
func TestProduceCachesOriginalFailureAcrossModes(t *testing.T) {
	doer := &fakeImageDoer{body: []byte("unavailable"), type_: "image/png", status: http.StatusServiceUnavailable}
	service := NewService(Config{DataDir: t.TempDir()}, nil, doer)
	for _, mode := range []string{"thumb", "full"} {
		if _, err := service.Produce(context.Background(), "https://images.example.invalid/failing.png", mode); err == nil {
			t.Fatalf("produce %s unexpectedly succeeded", mode)
		}
	}
	if got := doer.calls.Load(); got != 1 {
		t.Fatalf("failed original requests = %d, want one cached failure", got)
	}
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

	headRequest := httptest.NewRequest(http.MethodHead, requestURL, nil)
	headRequest.Header.Set("If-None-Match", first.Header().Get("ETag"))
	head := httptest.NewRecorder()
	handler.ServeHTTP(head, headRequest)
	if head.Code != http.StatusNotModified || head.Body.Len() != 0 || head.Header().Get("ETag") != first.Header().Get("ETag") {
		t.Fatalf("conditional HEAD status=%d headers=%v body=%q", head.Code, head.Header(), head.Body.String())
	}

	parsedURL, err := url.Parse(requestURL)
	if err != nil {
		t.Fatal(err)
	}
	invalidQuery := parsedURL.Query()
	invalidQuery.Set("sig", "0000000000000000")
	invalidRequest := httptest.NewRequest(http.MethodGet, parsedURL.Path+"?"+invalidQuery.Encode(), nil)
	invalidRequest.Header.Set("If-None-Match", first.Header().Get("ETag"))
	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, invalidRequest)
	if invalid.Code != http.StatusForbidden || invalid.Header().Get("Cache-Control") != "no-store" || invalid.Header().Get("ETag") != "" {
		t.Fatalf("conditional invalid signature status=%d headers=%v", invalid.Code, invalid.Header())
	}
}

/** TestHandlerDoesNotVaryImageBytesByAccept preserves a stable representation for signed URLs. */
func TestHandlerDoesNotVaryImageBytesByAccept(t *testing.T) {
	const secret = "accept-test-secret"
	doer := &fakeImageDoer{body: testPNG(t, 12, 8), type_: "image/png", status: http.StatusOK}
	service := NewService(Config{DataDir: t.TempDir(), SignSecret: secret}, nil, doer)
	handler := NewHandler(service, nil)
	requestURL := signedRequestURL("https://images.example.invalid/accept.png", "thumb", secret)
	var first []byte
	for index, accept := range []string{"image/avif", "image/webp"} {
		request := httptest.NewRequest(http.MethodGet, requestURL, nil)
		request.Header.Set("Accept", accept)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Header().Get("Vary") != "" {
			t.Fatalf("Accept %q status=%d headers=%v", accept, response.Code, response.Header())
		}
		if index == 0 {
			first = response.Body.Bytes()
		} else if !bytes.Equal(response.Body.Bytes(), first) {
			t.Fatalf("response bytes changed for Accept %q", accept)
		}
	}
	if got := doer.calls.Load(); got != 1 {
		t.Fatalf("Accept variants made %d upstream requests, want cached representation", got)
	}
}

/** TestHandlerBoundsFailureCacheBySignatureExpiry limits error caching to its signature lifetime. */
func TestHandlerBoundsFailureCacheBySignatureExpiry(t *testing.T) {
	const secret = "failure-cache-secret"
	doer := &fakeImageDoer{body: []byte("upstream unavailable"), type_: "image/png", status: http.StatusServiceUnavailable}
	service := NewService(Config{DataDir: t.TempDir(), SignSecret: secret}, nil, doer)
	handler := NewHandler(service, nil)
	for _, lifetime := range []time.Duration{time.Hour, 30 * time.Second} {
		t.Run(lifetime.String(), func(t *testing.T) {
			requestURL := signedRequestURLAt("https://images.example.invalid/failure-"+lifetime.String(), "thumb", secret, time.Now().Add(lifetime))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, requestURL, nil))
			if response.Code != http.StatusBadGateway || response.Header().Get("ETag") != "" {
				t.Fatalf("failure status=%d headers=%v", response.Code, response.Header())
			}
			cache := response.Header().Get("Cache-Control")
			if !strings.HasPrefix(cache, "public, max-age=") {
				t.Fatalf("failure cache policy = %q", cache)
			}
			maxAgePart := strings.SplitN(strings.TrimPrefix(cache, "public, max-age="), ",", 2)[0]
			seconds, err := strconv.Atoi(maxAgePart)
			if err != nil || seconds <= 0 || seconds > 60 || time.Duration(seconds)*time.Second > lifetime {
				t.Fatalf("failure cache policy %q exceeds its limit: seconds=%d err=%v", cache, seconds, err)
			}
			if !strings.HasSuffix(cache, "s-maxage="+strconv.Itoa(seconds)) {
				t.Fatalf("shared cache lifetime differs from browser cache lifetime: %q", cache)
			}
		})
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

/** TestHandlerAcceptsBinaryLabeledImagesAndRejectsErrorPages checks payload sniffing over MIME labels. */
func TestHandlerAcceptsBinaryLabeledImagesAndRejectsErrorPages(t *testing.T) {
	doer := &fakeImageDoer{
		bodies: map[string][]byte{
			"/binary-image": testPNG(t, 24, 12),
			"/binary-error": []byte("<html>upstream error</html>"),
		},
		type_:  "application/octet-stream",
		status: http.StatusOK,
	}
	service := NewService(Config{DataDir: t.TempDir(), SignSecret: "binary-test-secret"}, nil, doer)
	handler := NewHandler(service, nil)
	imageResponse := httptest.NewRecorder()
	handler.ServeHTTP(imageResponse, httptest.NewRequest(http.MethodGet, signedRequestURL("https://images.example.invalid/binary-image", "card", "binary-test-secret"), nil))
	if imageResponse.Code != http.StatusOK || imageResponse.Header().Get("Content-Type") != "image/webp" {
		t.Fatalf("binary image status=%d headers=%v", imageResponse.Code, imageResponse.Header())
	}
	errorResponse := httptest.NewRecorder()
	handler.ServeHTTP(errorResponse, httptest.NewRequest(http.MethodGet, signedRequestURL("https://images.example.invalid/binary-error", "card", "binary-test-secret"), nil))
	if errorResponse.Code != http.StatusBadGateway || errorResponse.Body.String() != "Upstream image unavailable" {
		t.Fatalf("binary error page status=%d body=%q", errorResponse.Code, errorResponse.Body.String())
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
	body := testCompressibleGIF(t)
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
	if response.Header().Get("ETag") == "" {
		t.Fatalf("unprepared GIF response is missing ETag: %v", response.Header())
	}
	initialETag := response.Header().Get("ETag")
	if enqueued.Load() != 1 {
		t.Fatalf("queued preparations = %d, want 1", enqueued.Load())
	}

	parsed, _ := urlpkgParse(url)
	query := parsed.Query()
	if _, err := service.ConvertAnimated(context.Background(), query.Get("u"), query.Get("mode")); err != nil {
		t.Fatalf("ConvertAnimated: %v", err)
	}
	second := httptest.NewRecorder()
	secondRequest := httptest.NewRequest(http.MethodGet, url, nil)
	secondRequest.Header.Set("If-None-Match", initialETag)
	handler.ServeHTTP(second, secondRequest)
	if second.Code != http.StatusOK || second.Header().Get("Cache-Control") == "no-store" || second.Header().Get("Content-Type") != "image/webp" || second.Header().Get("ETag") == initialETag {
		t.Fatalf("prepared GIF response status=%d headers=%v", second.Code, second.Header())
	}
	if enqueued.Load() != 1 || doer.calls.Load() != 1 {
		t.Fatalf("preparation repeated: queue=%d fetch=%d", enqueued.Load(), doer.calls.Load())
	}
}

/** TestGIFPreparationLeavesTinyAndOverBudgetOriginalsUntouched covers animation conversion limits. */
func TestGIFPreparationLeavesTinyAndOverBudgetOriginalsUntouched(t *testing.T) {
	overBudget := testGIF(t)
	overBudget[6] = 0xf8
	overBudget[7] = 0x2a
	overBudget[8] = 0xf8
	overBudget[9] = 0x2a
	for _, fixture := range []struct {
		name    string
		body    []byte
		pending bool
	}{
		{name: "tiny", body: testGIF(t), pending: true},
		{name: "over-budget", body: overBudget},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			dataDir := t.TempDir()
			rawURL := "https://images.example.invalid/" + fixture.name + ".gif"
			doer := &fakeImageDoer{body: fixture.body, type_: "image/gif", status: http.StatusOK}
			service := NewService(Config{DataDir: dataDir}, nil, doer)
			first, err := service.Produce(context.Background(), rawURL, "thumb")
			if err != nil || first.PendingAnimation != fixture.pending {
				t.Fatalf("initial GIF result pending=%v err=%v", first.PendingAnimation, err)
			}
			if saved, err := service.ConvertAnimated(context.Background(), rawURL, "thumb"); err != nil || saved != 0 {
				t.Fatalf("ConvertAnimated saved=%d err=%v, want unchanged original", saved, err)
			}
			second, err := service.Produce(context.Background(), rawURL, "thumb")
			if err != nil || second.PendingAnimation || second.Type != "image/gif" || !bytes.Equal(second.Body, fixture.body) {
				t.Fatalf("cached GIF result type=%q pending=%v err=%v", second.Type, second.PendingAnimation, err)
			}
			if got := doer.calls.Load(); got != 1 {
				t.Fatalf("unchanged GIF fetched %d times, want one", got)
			}
		})
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
	return signedRequestURLAt(raw, mode, secret, time.Now().Add(time.Hour))
}

/** signedRequestURLAt signs an image request with a caller-selected expiration. */
func signedRequestURLAt(raw, mode, secret string, expiry time.Time) string {
	exp := strconv.FormatInt(expiry.Unix(), 10)
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

/** testCompressibleGIF builds deterministic animation data that benefits from WebP conversion. */
func testCompressibleGIF(t *testing.T) []byte {
	t.Helper()
	palette := make(color.Palette, 256)
	for index := range palette {
		palette[index] = color.Gray{Y: uint8(index)}
	}
	random := rand.New(rand.NewSource(42))
	animation := &gif.GIF{LoopCount: 0}
	for frameIndex := 0; frameIndex < 10; frameIndex++ {
		frame := image.NewPaletted(image.Rect(0, 0, 160, 120), palette)
		for index := range frame.Pix {
			frame.Pix[index] = uint8(random.Intn(len(palette)))
		}
		animation.Image = append(animation.Image, frame)
		animation.Delay = append(animation.Delay, 9)
	}
	var out bytes.Buffer
	if err := gif.EncodeAll(&out, animation); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func urlpkgParse(raw string) (*url.URL, error) { return url.Parse(raw) }
