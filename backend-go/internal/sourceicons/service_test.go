package sourceicons

import (
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type memoryRepository struct {
	xCount  int
	sources []Source
	saved   map[string]*string
	checked map[string]time.Time
	ran     bool
	start   time.Time
	end     time.Time
	err     error
}

func (r *memoryRepository) RefreshXAvatars(context.Context) (int, error) { return r.xCount, r.err }
func (r *memoryRepository) Missing(context.Context, time.Time, int) ([]Source, error) {
	return r.sources, r.err
}
func (r *memoryRepository) Save(_ context.Context, id string, icon *string, checked time.Time) error {
	if r.err != nil {
		return r.err
	}
	if r.saved == nil {
		r.saved = make(map[string]*string)
		r.checked = make(map[string]time.Time)
	}
	r.saved[id] = icon
	r.checked[id] = checked
	return nil
}
func (r *memoryRepository) RanSince(_ context.Context, start, end time.Time) (bool, error) {
	r.start, r.end = start, end
	return r.ran, r.err
}

type fakeDoer func(*http.Request) (*http.Response, error)

func (f fakeDoer) Do(request *http.Request) (*http.Response, error) { return f(request) }

func response(request *http.Request, status int, contentType, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}
}

func TestIconCandidatesRankDeclaredIconsAndResolveRelativeURLs(t *testing.T) {
	html := []byte(`<html><head>
<link rel="icon" href="/favicon.ico">
<link rel="icon" sizes="64x64" href="icons/small.png">
<link rel="apple-touch-icon" href="//cdn.example.test/touch.png">
<link rel="icon" href="data:image/png;base64,AA==">
</head></html>`)
	got := iconCandidates(html, "https://news.example.test/home/index.html")
	want := []string{
		"https://cdn.example.test/touch.png",
		"https://news.example.test/home/icons/small.png",
		"https://news.example.test/favicon.ico",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("icon candidates = %#v, want %#v", got, want)
	}
}

func TestRunRefreshesXAndUsesFirstUsableDeclaredIcon(t *testing.T) {
	repository := &memoryRepository{xCount: 2, sources: []Source{{
		ID: "news", Kind: "web_list", Config: json.RawMessage(`{"url":"https://news.example.test/list"}`),
		URLs: []string{"https://news.example.test/a", "https://news.example.test/b"},
	}}}
	requests := []string{}
	pngData := tinyPNG(t)
	service := NewService(repository, fakeDoer(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.URL.String())
		if request.URL.Host == "news.example.test" && (request.URL.Path == "" || request.URL.Path == "/") {
			return response(request, http.StatusOK, "text/html", `<link rel="apple-touch-icon" href="/broken.png"><link rel="icon" sizes="64x64" href="/avatar.png">`), nil
		}
		switch request.URL.String() {
		case "https://news.example.test/broken.png":
			return response(request, http.StatusOK, "image/png", "not an image"), nil
		case "https://news.example.test/avatar.png":
			return response(request, http.StatusOK, "image/png", pngData), nil
		default:
			return response(request, http.StatusNotFound, "text/plain", "missing"), nil
		}
	}), "test-agent")
	checkedAt := time.Date(2026, 10, 8, 4, 45, 0, 0, time.UTC)
	service.SetClock(func() time.Time { return checkedAt })
	result, err := service.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result != (Result{XAvatars: 2, Checked: 1, Found: 1}) {
		t.Fatalf("result = %#v", result)
	}
	if repository.saved["news"] == nil || *repository.saved["news"] != "https://news.example.test/avatar.png" || !repository.checked["news"].Equal(checkedAt) {
		t.Fatalf("saved icon/time = %v %v", repository.saved["news"], repository.checked["news"])
	}
	if len(requests) != 3 || requests[1] != "https://news.example.test/broken.png" || requests[2] != "https://news.example.test/avatar.png" {
		t.Fatalf("request sequence = %#v", requests)
	}
}

func TestRunKeepsFailedAndForwardedKindsSemantics(t *testing.T) {
	pngData := tinyPNG(t)
	repository := &memoryRepository{sources: []Source{
		{ID: "mp", Kind: "mp_account", Config: json.RawMessage(`{}`), URLs: []string{"https://mp.example.test/article"}},
		{ID: "rss", Kind: "rss", Config: json.RawMessage(`{"feedUrl":"https://rss.example.test/feed"}`)},
	}}
	sleeps := 0
	service := NewService(repository, fakeDoer(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "mp.example.test" {
			return response(request, http.StatusOK, "text/html", `<script>round_head_img: 'http://img.example.test/avatar.png'</script>`), nil
		}
		if request.URL.Host == "img.example.test" {
			return response(request, http.StatusOK, "image/png", pngData), nil
		}
		return response(request, http.StatusServiceUnavailable, "text/plain", "down"), nil
	}), "test-agent")
	service.SetSleeper(func(context.Context, time.Duration) error { sleeps++; return nil })
	if _, err := service.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sleeps != 0 || repository.saved["mp"] == nil || *repository.saved["mp"] != "https://img.example.test/avatar.png" {
		t.Fatalf("MP retry state: sleeps=%d saved=%v", sleeps, repository.saved["mp"])
	}
	if repository.saved["rss"] != nil || repository.checked["rss"].IsZero() {
		t.Fatalf("missing RSS icon was not checked: %#v %#v", repository.saved, repository.checked)
	}
}

func TestDueWindowUsesBeijingSlotAndCatchesUpAfterIt(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	before := time.Date(2026, 10, 8, 4, 39, 59, 0, location)
	start, end, due, err := DueWindow(before)
	if err != nil || due || start.In(location).Format("15:04") != "04:40" || end.Sub(start) != 24*time.Hour {
		t.Fatalf("before slot = %v %v %v %v", start, end, due, err)
	}
	late := time.Date(2026, 10, 8, 8, 0, 0, 0, location)
	start, _, due, err = DueWindow(late)
	if err != nil || !due || start.Format("2006-01-02") != "2026-10-08" {
		t.Fatalf("missed slot was not caught up once: %v %v %v", start, due, err)
	}
}

func TestDueEnqueuesOneCatchUpPerBeijingDay(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	repository := &memoryRepository{}
	service := NewService(repository, nil, "")
	now := time.Date(2026, 10, 8, 8, 0, 0, 0, location)
	day, due, err := service.Due(context.Background(), now)
	if err != nil || !due || day != "2026-10-08" || repository.start.In(location).Format("15:04") != "04:40" || repository.end.Sub(repository.start) != 24*time.Hour {
		t.Fatalf("first due check = %q %v %v, window %v..%v", day, due, err, repository.start, repository.end)
	}
	repository.ran = true
	if day, due, err = service.Due(context.Background(), now.Add(time.Minute)); err != nil || due || day != "2026-10-08" {
		t.Fatalf("same-day repeated check = %q %v %v", day, due, err)
	}
	repository.ran = false
	day, due, err = service.Due(context.Background(), now.Add(24*time.Hour))
	if err != nil || !due || day != "2026-10-09" {
		t.Fatalf("following-day missed slot was not due = %q %v %v", day, due, err)
	}
}

func TestHomeOfUsesSeventyPercentAndStableTie(t *testing.T) {
	config := map[string]any{"url": "https://configured.example.test/feed"}
	if got := homeOf([]string{"https://a.test/1", "https://a.test/2", "https://a.test/3", "https://b.test/4"}, config); got != "https://a.test" {
		t.Fatalf("70 percent host was not selected: %q", got)
	}
	if got := homeOf([]string{"https://a.test/1", "https://b.test/2"}, config); got != "https://configured.example.test" {
		t.Fatalf("split article hosts did not use configured source: %q", got)
	}
	if got := homeOf([]string{"https://b.test/1", "https://a.test/2"}, config); got != "https://configured.example.test" {
		t.Fatalf("equal-frequency article hosts did not use configured source: %q", got)
	}
}

func TestHomeOfMatchesURLOriginDefaultPortNormalization(t *testing.T) {
	got := homeOf([]string{"https://news.example.test:443/1", "https://news.example.test/2", "https://news.example.test:443/3"}, map[string]any{})
	if got != "https://news.example.test" {
		t.Fatalf("default ports split one origin: %q", got)
	}
}

func TestImageBytesRecognizesCommonFormatsAndRejectsHTML(t *testing.T) {
	if !imageBytes("image/png", []byte(tinyPNG(t))) {
		t.Fatal("valid PNG was rejected")
	}
	if imageBytes("image/png", []byte("<html>error</html>")) {
		t.Fatal("HTML error page was accepted as an image")
	}
	if !imageBytes("image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)) {
		t.Fatal("valid SVG was rejected")
	}
}

func tinyPNG(t *testing.T) string {
	t.Helper()
	var data strings.Builder
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	return data.String()
}
