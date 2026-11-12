package admincontent

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"
)

var articleIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)
var tweetURLPattern = regexp.MustCompile(`(?i)^/[^/]+/status(?:es)?/(\d+)(?:/|$)`)
var trackingParameterPattern = regexp.MustCompile(`(?i)^(utm_[a-z]+|spm|from|ref|ref_src|ref_url|source|share_source|share_token|fbclid|gclid|igshid|mc_cid|mc_eid|_hsenc|_hsmi|scene|chksm|sessionid|srcid|clicktime|enterid|mkt_tok)$`)
var tweetHostPattern = regexp.MustCompile(`^(?:(?:www|mobile|m)\.)?(?:x|twitter)\.com$`)

var (
	ErrSEORepositoryUnavailable = errors.New("content SEO repository is unavailable")
	ErrInvalidSEO               = errors.New("reason must not be empty")
	ErrCommandsUnavailable      = errors.New("content command repository is unavailable")
	ErrInvalidCommand           = errors.New("invalid content command")
	ErrCommandConflict          = errors.New("这条内容的人工设置已被修改，请刷新后再操作")
)

type Repository interface {
	ByID(context.Context, string) ([]Row, error)
	ByURL(context.Context, []string, []string) ([]Row, error)
	ByTitle(context.Context, string) ([]Row, error)
}

type Service struct {
	repository Repository
	commands   CommandRepository
	categories map[string]struct{}
}

func NewService(repository Repository) *Service { return &Service{repository: repository} }

func (s *Service) SetCommands(repository CommandRepository, categories []string) {
	s.commands = repository
	s.categories = make(map[string]struct{}, len(categories))
	for _, category := range categories {
		s.categories[category] = struct{}{}
	}
}

type SEOInput struct {
	Indexed bool
	Reason  string
}

type PublishResult struct {
	ArticleID  string  `json:"articleId"`
	Changed    bool    `json:"changed"`
	ChangeKind string  `json:"changeKind"`
	Selected   bool    `json:"selected"`
	Visibility string  `json:"visibility"`
	Ledger     *string `json:"ledger"`
	Reduced    bool    `json:"reduced"`
}

type SEORepository interface {
	SetSEO(context.Context, string, SEOInput, string) (*PublishResult, error)
}

func (s *Service) SetSEO(ctx context.Context, id string, input SEOInput, actor string) (*PublishResult, error) {
	if strings.TrimSpace(input.Reason) == "" {
		return nil, ErrInvalidSEO
	}
	repository, ok := s.repository.(SEORepository)
	if !ok {
		return nil, ErrSEORepositoryUnavailable
	}
	return repository.SetSEO(ctx, id, input, actor)
}

type VisibilityInput struct {
	Visibility string
	Reason     string
	Version    int
}

type OverrideInput struct {
	Fields  map[string]json.RawMessage
	Clear   []string
	Reason  string
	Version int
}

type RerunResult struct {
	JobID *string `json:"jobId"`
}

type CommandRepository interface {
	SetVisibility(context.Context, string, VisibilityInput, string) (*PublishResult, bool, error)
	OverrideFields(context.Context, string, OverrideInput, string) (*PublishResult, bool, error)
	Rerun(context.Context, string, string, string, string) (*RerunResult, bool, error)
	Detach(context.Context, string, string, string) (map[string]int, bool, error)
	MergeStories(context.Context, int64, int64, string, string) (map[string]int, bool, error)
}

func (s *Service) SetVisibility(ctx context.Context, id string, input VisibilityInput, actor string) (*PublishResult, bool, error) {
	if input.Visibility != "public" && input.Visibility != "summary-only" && input.Visibility != "withdrawn" || strings.TrimSpace(input.Reason) == "" || input.Version < 0 {
		return nil, false, ErrInvalidCommand
	}
	if s.commands == nil {
		return nil, false, ErrCommandsUnavailable
	}
	return s.commands.SetVisibility(ctx, id, input, actor)
}

func (s *Service) OverrideFields(ctx context.Context, id string, input OverrideInput, actor string) (*PublishResult, bool, error) {
	if strings.TrimSpace(input.Reason) == "" || input.Version < 0 || validateOverrideFields(input.Fields, s.categories) != nil {
		return nil, false, ErrInvalidCommand
	}
	if s.commands == nil {
		return nil, false, ErrCommandsUnavailable
	}
	return s.commands.OverrideFields(ctx, id, input, actor)
}

func (s *Service) Rerun(ctx context.Context, id, step, requestID, actor string) (*RerunResult, bool, error) {
	if step != "extract" && step != "analyze" && step != "group" || !requestIDPattern.MatchString(requestID) {
		return nil, false, ErrInvalidCommand
	}
	if s.commands == nil {
		return nil, false, ErrCommandsUnavailable
	}
	return s.commands.Rerun(ctx, id, step, requestID, actor)
}

func (s *Service) Detach(ctx context.Context, id, reason, actor string) (map[string]int, bool, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, false, ErrInvalidCommand
	}
	if s.commands == nil {
		return nil, false, ErrCommandsUnavailable
	}
	return s.commands.Detach(ctx, id, reason, actor)
}

func (s *Service) MergeStories(ctx context.Context, from, into int64, reason, actor string) (map[string]int, bool, error) {
	if from < 1 || into < 1 || from == into || strings.TrimSpace(reason) == "" {
		return nil, false, ErrInvalidCommand
	}
	if s.commands == nil {
		return nil, false, ErrCommandsUnavailable
	}
	return s.commands.MergeStories(ctx, from, into, reason, actor)
}

var requestIDPattern = regexp.MustCompile(`^[\w-]{8,80}$`)

func validateOverrideFields(fields map[string]json.RawMessage, categories map[string]struct{}) error {
	allowed := map[string]struct{}{"title": {}, "summary": {}, "reason": {}, "category": {}, "tags": {}, "selected": {}, "silent": {}}
	for key, raw := range fields {
		if _, ok := allowed[key]; !ok {
			return ErrInvalidCommand
		}
		var text string
		switch key {
		case "title", "summary", "reason", "category":
			if err := json.Unmarshal(raw, &text); err != nil {
				return ErrInvalidCommand
			}
			size := len(utf16.Encode([]rune(text)))
			switch key {
			case "title":
				if size < 1 || size > 300 {
					return ErrInvalidCommand
				}
			case "summary":
				if size > 2000 {
					return ErrInvalidCommand
				}
			case "reason":
				if size > 1000 {
					return ErrInvalidCommand
				}
			case "category":
				if _, ok := categories[text]; !ok {
					return ErrInvalidCommand
				}
			}
		case "tags":
			var tags []string
			if err := json.Unmarshal(raw, &tags); err != nil || tags == nil || len(tags) > 20 {
				return ErrInvalidCommand
			}
			for _, tag := range tags {
				if len(utf16.Encode([]rune(tag))) > 60 {
					return ErrInvalidCommand
				}
			}
		case "selected", "silent":
			var value bool
			if err := json.Unmarshal(raw, &value); err != nil {
				return ErrInvalidCommand
			}
		}
	}
	return nil
}

type Row struct {
	ID              string    `json:"id"`
	Title           string    `json:"title"`
	URL             string    `json:"url"`
	Source          string    `json:"source"`
	DiscoveredAt    time.Time `json:"discovered_at"`
	ProcessingState string    `json:"processing_state"`
	Visibility      *string   `json:"visibility"`
	Selected        *bool     `json:"selected"`
	Score           *float64  `json:"score"`
}

func (s *Service) Search(ctx context.Context, query string) ([]Row, error) {
	term := strings.TrimSpace(query)
	if term == "" {
		return []Row{}, nil
	}
	if articleIDPattern.MatchString(term) {
		rows, err := s.repository.ByID(ctx, term)
		if err != nil || len(rows) > 0 {
			return emptyIfNil(rows), err
		}
	}
	if urls, identities, ok := contentURLKeys(term); ok {
		rows, err := s.repository.ByURL(ctx, urls, identities)
		if err != nil || len(rows) > 0 {
			return emptyIfNil(rows), err
		}
	}
	rows, err := s.repository.ByTitle(ctx, term)
	return emptyIfNil(rows), err
}

func contentURLKeys(raw string) ([]string, []string, bool) {
	parsed, err := url.Parse(raw)
	if err != nil || (!strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https")) || parsed.Hostname() == "" {
		return nil, nil, false
	}
	host := strings.ToLower(parsed.Hostname())
	if match := tweetURLPattern.FindStringSubmatch(parsed.EscapedPath()); len(match) > 1 && tweetHostPattern.MatchString(host) {
		identity := "x:" + match[1]
		return []string{raw}, []string{identity}, true
	}
	canonical := canonicalContentURL(parsed, false)
	preserved := canonicalContentURL(parsed, true)
	return []string{raw, canonical}, []string{"url:" + canonical, "url:" + preserved}, true
}

func canonicalContentURL(input *url.URL, preserveFragment bool) string {
	u := *input
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	port := u.Port()
	u.Scheme = "https"
	u.Host = host
	if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	}
	if port != "" && port != "80" && port != "443" {
		u.Host += ":" + port
	}
	if !preserveFragment {
		u.Fragment, u.RawFragment = "", ""
	}
	values := u.Query()
	if host == "mp.weixin.qq.com" {
		keep := url.Values{}
		for _, key := range []string{"__biz", "mid", "idx", "sn"} {
			if value := values.Get(key); value != "" {
				keep.Set(key, value)
			}
		}
		values = keep
	} else {
		for key := range values {
			if trackingParameterPattern.MatchString(key) {
				values.Del(key)
			}
		}
	}
	u.RawQuery = values.Encode()
	result := u.String()
	if strings.HasSuffix(result, "/") && u.Path != "/" {
		result = strings.TrimSuffix(result, "/")
	}
	return result
}

func emptyIfNil(rows []Row) []Row {
	if rows == nil {
		return []Row{}
	}
	return rows
}
