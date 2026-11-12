package siteinfo

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"sync"
)

type Service struct {
	path     string
	openAPI  OpenAPIConfig
	once     sync.Once
	body     []byte
	meta     []byte
	etag     string
	metaETag string
	err      error
}

type OpenAPIConfig struct {
	Path        string
	SiteName    string
	SiteURL     string
	Description string
	Tagline     string
	Locale      string
	Version     string
	DailyTime   string
	WeeklyTime  string
	MonthlyTime string
	Categories  []string
}

func NewService(path string, openAPI ...OpenAPIConfig) *Service {
	service := &Service{path: path}
	if len(openAPI) > 0 {
		service.openAPI = openAPI[0]
	}
	return service
}

func (s *Service) Meta() ([]byte, string, error) {
	s.load()
	return s.meta, s.metaETag, s.err
}

func (s *Service) Changelog() ([]byte, string, error) {
	s.load()
	return s.body, s.etag, s.err
}

func (s *Service) OpenAPIV1() ([]byte, string, error) {
	data, err := os.ReadFile(s.openAPI.Path)
	if err != nil {
		return nil, "", err
	}
	var document any
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, "", err
	}
	values := map[string]string{
		"siteName": s.openAPI.SiteName, "siteUrl": s.openAPI.SiteURL,
		"description": s.openAPI.Description, "tagline": s.openAPI.Tagline,
		"locale": s.openAPI.Locale, "version": s.openAPI.Version,
		"dailyTime": s.openAPI.DailyTime, "weeklyTime": s.openAPI.WeeklyTime,
		"monthlyTime":  s.openAPI.MonthlyTime,
		"categoryList": strings.Join(s.openAPI.Categories, ", "),
	}
	document = fillOpenAPI(document, values, s.openAPI.Categories)
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return nil, "", err
	}
	body := bytes.TrimSuffix(output.Bytes(), []byte("\n"))
	sum := sha256.Sum256(body)
	etag := `W/"` + hex.EncodeToString(sum[:])[:16] + `"`
	return body, etag, nil
}

func fillOpenAPI(node any, values map[string]string, categories []string) any {
	switch value := node.(type) {
	case map[string]any:
		for key, child := range value {
			if (key == "enum" || key == "examples") && containsCategoryPlaceholder(child) {
				value[key] = append([]string(nil), categories...)
				continue
			}
			value[key] = fillOpenAPI(child, values, categories)
		}
		return value
	case []any:
		for i, child := range value {
			value[i] = fillOpenAPI(child, values, categories)
		}
		return value
	case string:
		for key, replacement := range values {
			value = strings.ReplaceAll(value, "{{"+key+"}}", replacement)
		}
		return value
	default:
		return node
	}
}

func containsCategoryPlaceholder(value any) bool {
	values, ok := value.([]any)
	if !ok {
		return false
	}
	for _, item := range values {
		if item == "{{categories}}" {
			return true
		}
	}
	return false
}

func (s *Service) load() {
	s.once.Do(func() {
		data, err := os.ReadFile(s.path)
		if err != nil {
			s.err = err
			return
		}
		var changelog struct {
			LatestVersion string `json:"latestVersion"`
		}
		if err := json.Unmarshal(data, &changelog); err != nil {
			s.err = err
			return
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, data); err != nil {
			s.err = err
			return
		}
		s.body = compact.Bytes()
		s.etag = weakETag("changelog", s.body)
		s.meta, err = json.Marshal(struct {
			ChangelogVersion string `json:"changelogVersion"`
		}{ChangelogVersion: changelog.LatestVersion})
		if err != nil {
			s.err = err
			return
		}
		s.metaETag = weakETag("meta", s.meta)
	})
}

func weakETag(prefix string, body []byte) string {
	sum := sha256.Sum256(body)
	return `W/"` + prefix + `-` + hex.EncodeToString(sum[:])[:16] + `"`
}
