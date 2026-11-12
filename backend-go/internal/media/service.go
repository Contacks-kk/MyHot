package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"aihot/backend-go/internal/modelconfig"
)

const (
	maxUpstreamBytes   = 15 * 1024 * 1024
	maxAnimationPixels = int64(200_000_000)
)

var ErrBudgetExceeded = errors.New("image upstream budget exceeded")

type Config struct {
	DataDir           string
	SignSecret        string
	MinuteBudgetBytes int64
	DailyBudgetBytes  int64
	EgressProxyURL    string
	DirectFetchHosts  []string
	SiteURL           string
	CrawlerName       string
}

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type PreparedImage struct {
	Body             []byte
	Type             string
	PendingAnimation bool
}

type sourceEntry struct {
	body  []byte
	type_ string
	until time.Time
	used  time.Time
}

type sourceFailure struct {
	err   error
	until time.Time
}

type Service struct {
	config    Config
	repo      Repository
	client    HTTPDoer
	clientErr error

	mu           sync.Mutex
	minuteAt     int64
	minuteBytes  int64
	dayKey       string
	dayBytes     int64
	tripped      string
	sourceBytes  int
	inflight     map[string]*sourceCall
	recentSource map[string]sourceEntry
	failures     map[string]sourceFailure
	imageCalls   map[string]*imageCall
}

type sourceCall struct {
	done  chan struct{}
	body  []byte
	type_ string
	err   error
}

type imageCall struct {
	done chan struct{}
	img  PreparedImage
	err  error
}

func NewService(cfg Config, repo Repository, client HTTPDoer) *Service {
	if client == nil {
		dialContext := modelconfig.DialPublic
		var clientErr error
		if strings.TrimSpace(cfg.EgressProxyURL) != "" {
			router, err := newEgressRouter(cfg.EgressProxyURL, cfg.DirectFetchHosts)
			if err != nil {
				clientErr = err
			} else {
				dialContext = router.dialContext
			}
		}
		transport := &http.Transport{
			Proxy:                 nil,
			DialContext:           dialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
			MaxIdleConns:          16,
			MaxIdleConnsPerHost:   4,
		}
		client = &http.Client{
			Transport: transport,
			Timeout:   20 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) > 5 {
					return errors.New("too many redirects")
				}
				return validatePublicURL(req.URL)
			},
		}
		return &Service{config: cfg, repo: repo, client: client, clientErr: clientErr, inflight: map[string]*sourceCall{}, recentSource: map[string]sourceEntry{}, failures: map[string]sourceFailure{}, imageCalls: map[string]*imageCall{}}
	}
	return &Service{config: cfg, repo: repo, client: client, inflight: map[string]*sourceCall{}, recentSource: map[string]sourceEntry{}, failures: map[string]sourceFailure{}, imageCalls: map[string]*imageCall{}}
}

func (s *Service) ConvertAnimated(ctx context.Context, rawURL, mode string) (int64, error) {
	return ConvertAnimatedContext(ctx, s.config.DataDir, rawURL, mode)
}

func (s *Service) Produce(ctx context.Context, rawURL, mode string) (PreparedImage, error) {
	key := cacheKey(rawURL, mode)
	file := s.cacheFile(key)
	if image, err := readCached(file); err == nil {
		return image, nil
	}
	s.mu.Lock()
	if call := s.imageCalls[key]; call != nil {
		s.mu.Unlock()
		select {
		case <-call.done:
			return call.img, call.err
		case <-ctx.Done():
			return PreparedImage{}, ctx.Err()
		}
	}
	call := &imageCall{done: make(chan struct{})}
	s.imageCalls[key] = call
	s.mu.Unlock()

	call.img, call.err = s.produce(ctx, rawURL, mode, file)
	s.mu.Lock()
	delete(s.imageCalls, key)
	close(call.done)
	s.mu.Unlock()
	return call.img, call.err
}

func (s *Service) produce(ctx context.Context, rawURL, mode, file string) (PreparedImage, error) {
	body, upstreamType, err := s.original(ctx, rawURL)
	if err != nil {
		return PreparedImage{}, err
	}
	image, err := resizeImage(body, upstreamType, mode)
	if err != nil {
		return PreparedImage{}, err
	}
	if err := writeCache(file, image); err != nil {
		return PreparedImage{}, err
	}
	return image, nil
}

func (s *Service) original(ctx context.Context, rawURL string) ([]byte, string, error) {
	now := time.Now()
	s.mu.Lock()
	if entry, ok := s.recentSource[rawURL]; ok && now.Before(entry.until) {
		entry.used = now
		s.recentSource[rawURL] = entry
		s.mu.Unlock()
		return entry.body, entry.type_, nil
	}
	if failure, ok := s.failures[rawURL]; ok && now.Before(failure.until) {
		s.mu.Unlock()
		return nil, "", failure.err
	}
	delete(s.failures, rawURL)
	if entry, ok := s.recentSource[rawURL]; ok {
		s.sourceBytes -= len(entry.body)
		delete(s.recentSource, rawURL)
	}
	if call := s.inflight[rawURL]; call != nil {
		s.mu.Unlock()
		select {
		case <-call.done:
			return call.body, call.type_, call.err
		case <-ctx.Done():
			return nil, "", ctx.Err()
		}
	}
	if err := s.allowed(now); err != nil {
		s.mu.Unlock()
		return nil, "", err
	}
	call := &sourceCall{done: make(chan struct{})}
	s.inflight[rawURL] = call
	s.mu.Unlock()

	call.body, call.type_, call.err = s.fetch(ctx, rawURL)
	s.mu.Lock()
	delete(s.inflight, rawURL)
	if call.err == nil && len(call.body) <= 32*1024*1024 {
		s.rememberSourceLocked(rawURL, call.body, call.type_, time.Now())
	} else if call.err != nil && !errors.Is(call.err, ErrBudgetExceeded) {
		if len(s.failures) >= 512 {
			for key := range s.failures {
				delete(s.failures, key)
				break
			}
		}
		s.failures[rawURL] = sourceFailure{err: call.err, until: time.Now().Add(time.Minute)}
	}
	close(call.done)
	s.mu.Unlock()
	return call.body, call.type_, call.err
}

func (s *Service) rememberSourceLocked(rawURL string, body []byte, typeName string, now time.Time) {
	for key, entry := range s.recentSource {
		if !now.Before(entry.until) {
			s.sourceBytes -= len(entry.body)
			delete(s.recentSource, key)
		}
	}
	if len(body) > 32*1024*1024 {
		return
	}
	for len(s.recentSource) >= 32 || s.sourceBytes+len(body) > 32*1024*1024 {
		var oldestKey string
		var oldest time.Time
		for key, entry := range s.recentSource {
			if oldestKey == "" || entry.used.Before(oldest) {
				oldestKey, oldest = key, entry.used
			}
		}
		if oldestKey == "" {
			break
		}
		s.sourceBytes -= len(s.recentSource[oldestKey].body)
		delete(s.recentSource, oldestKey)
	}
	s.recentSource[rawURL] = sourceEntry{body: body, type_: typeName, until: now.Add(time.Minute), used: now}
	s.sourceBytes += len(body)
}

func (s *Service) allowed(now time.Time) error {
	minute := now.Unix() / 60
	day := now.In(beijingLocation()).Format("2006-01-02")
	if minute != s.minuteAt {
		s.minuteAt, s.minuteBytes = minute, 0
	}
	if day != s.dayKey {
		s.dayKey, s.dayBytes = day, 0
	}
	window := ""
	switch {
	case s.config.MinuteBudgetBytes > 0 && s.minuteBytes >= s.config.MinuteBudgetBytes:
		window = "minute"
	case s.config.DailyBudgetBytes > 0 && s.dayBytes >= s.config.DailyBudgetBytes:
		window = "day"
	}
	if window == "" {
		s.tripped = ""
		return nil
	}
	if s.tripped != window && s.repo != nil {
		budget := s.config.MinuteBudgetBytes
		unit, description := float64(1_000_000), "每分钟"
		if window == "day" {
			budget, unit, description = s.config.DailyBudgetBytes, 1_000_000_000, "当日"
		}
		state := ImageBudgetState{Window: window, Reason: fmt.Sprintf("图片代理上游流量超过%s %g %s", description, float64(budget)/unit, map[string]string{"minute": "MB", "day": "GB"}[window]), At: now, DayBytes: s.dayBytes}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = s.repo.RecordBudgetTrip(ctx, state)
		}()
	}
	s.tripped = window
	return ErrBudgetExceeded
}

func (s *Service) record(bytes int64, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.allowed(now)
	s.minuteBytes += bytes
	s.dayBytes += bytes
}

func (s *Service) fetch(ctx context.Context, rawURL string) ([]byte, string, error) {
	if s.clientErr != nil {
		return nil, "", s.clientErr
	}
	u, err := url.Parse(rawURL)
	if err != nil || validatePublicURL(u) != nil {
		return nil, "", errors.New("invalid image URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "image/avif,image/webp,image/*,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	crawlerName := strings.TrimSpace(s.config.CrawlerName)
	if crawlerName == "" {
		crawlerName = "MyHOTBot/1.0"
	}
	siteURL := strings.TrimRight(strings.TrimSpace(s.config.SiteURL), "/")
	if siteURL != "" {
		req.Header.Set("User-Agent", fmt.Sprintf("Mozilla/5.0 (compatible; %s; +%s/about)", crawlerName, siteURL))
	} else {
		req.Header.Set("User-Agent", fmt.Sprintf("Mozilla/5.0 (compatible; %s)", crawlerName))
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) > maxUpstreamBytes {
		return nil, "", errors.New("upstream image exceeds size limit")
	}
	s.record(int64(len(body)), time.Now())
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("upstream image returned %d", resp.StatusCode)
	}
	return body, resp.Header.Get("Content-Type"), nil
}

func validatePublicURL(u *url.URL) error {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return errors.New("image URL must be public HTTP or HTTPS without credentials")
	}
	return nil
}

func (s *Service) cacheFile(key string) string {
	return filepath.Join(s.config.DataDir, "imgcache", key[:2], key)
}

func cacheKey(rawURL, mode string) string {
	sum := sha256.Sum256([]byte(mode + "|" + rawURL))
	return hex.EncodeToString(sum[:])
}

func readCached(file string) (PreparedImage, error) {
	body, err := os.ReadFile(file)
	if err != nil {
		return PreparedImage{}, err
	}
	typeBytes, err := os.ReadFile(file + ".type")
	if err != nil {
		return PreparedImage{}, err
	}
	typeName := strings.TrimSpace(string(typeBytes))
	image := PreparedImage{Body: body, Type: typeName}
	if typeName == "image/gif" {
		if _, err := os.Stat(file + ".prepared"); errors.Is(err, os.ErrNotExist) {
			width, height, frames := gifSizeAndFrames(body)
			if int64(width)*int64(height)*int64(frames) > 0 && int64(width)*int64(height)*int64(frames) <= maxAnimationPixels {
				image.PendingAnimation = true
			}
		}
	}
	return image, nil
}

func writeCache(file string, image PreparedImage) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	if err := atomicWrite(file, image.Body); err != nil {
		return err
	}
	return atomicWrite(file+".type", []byte(image.Type))
}

func atomicWrite(file string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(file), ".img-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, file)
}

func beijingLocation() *time.Location {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("Asia/Shanghai", 8*60*60)
	}
	return location
}
