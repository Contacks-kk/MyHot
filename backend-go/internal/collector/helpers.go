package collector

import (
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/PuerkitoBio/goquery"
)

var errUnsupported = errors.New("unsupported source adapter")

type settings map[string]any

func (c settings) text(key string) string     { v, _ := c[key].(string); return strings.TrimSpace(v) }
func (c settings) object(key string) settings { v, _ := c[key].(map[string]any); return settings(v) }
func (c settings) boolean(key string) bool    { v, _ := c[key].(bool); return v }
func (c settings) integer(key string, fallback int) int {
	switch v := c[key].(type) {
	case float64:
		if v >= 0 {
			return int(v)
		}
	case int:
		if v >= 0 {
			return v
		}
	}
	return fallback
}
func (c settings) list(key string) []string {
	items := []string{}
	switch v := c[key].(type) {
	case []any:
		for _, value := range v {
			if s, ok := value.(string); ok {
				items = append(items, s)
			}
		}
	case []string:
		items = v
	case string:
		if v != "" {
			items = []string{v}
		}
	}
	return items
}
func truncate(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit])
}
func plain(s string) string {
	if strings.Contains(s, "<") {
		if doc, err := goquery.NewDocumentFromReader(strings.NewReader(s)); err == nil {
			doc.Find("script,style,noscript").Remove()
			doc.Find("p,div,li,br,h1,h2,h3").Each(func(_ int, n *goquery.Selection) { n.AppendHtml(" ") })
			s = doc.Text()
		}
	}
	return strings.Join(strings.Fields(s), " ")
}

var trackingPattern = regexp.MustCompile("(?i)^(utm_[a-z]+|spm|from|ref|ref_src|ref_url|source|share_source|share_token|fbclid|gclid|igshid|mc_cid|mc_eid|_hsenc|_hsmi|scene|chksm|sessionid|srcid|clicktime|enterid|mkt_tok)$")
var tweetPattern = regexp.MustCompile("(?i)^https://(?:x|twitter)\\.com/[^/]+/status/(\\d+)")

func normalizeURL(raw string, preserve bool) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return raw
	}
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
	if !preserve {
		u.Fragment = ""
		u.RawFragment = ""
	}
	q := u.Query()
	for key := range q {
		if trackingPattern.MatchString(key) {
			q.Del(key)
		}
	}
	if host == "mp.weixin.qq.com" {
		parts := []string{}
		for _, key := range []string{"__biz", "mid", "idx", "sn"} {
			if value := q.Get(key); value != "" {
				parts = append(parts, key+"="+queryEncode(value))
			}
		}
		u.RawQuery = strings.Join(parts, "&")
	} else {
		keys := []string{}
		for key := range q {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := []string{}
		for _, key := range keys {
			for _, value := range q[key] {
				parts = append(parts, queryEncode(key)+"="+queryEncode(value))
			}
		}
		u.RawQuery = strings.Join(parts, "&")
	}
	if u.Path == "" {
		u.Path = "/"
	}
	if u.Path != "/" && u.RawQuery == "" && u.Fragment == "" {
		u.Path = strings.TrimRight(u.Path, "/")
		u.RawPath = ""
	}
	return u.String()
}
func queryEncode(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(url.QueryEscape(s), "~", "%7E"), "%2A", "*")
}
func resolveURL(base, raw string) string {
	b, e := url.Parse(base)
	if e != nil {
		return ""
	}
	u, e := url.Parse(strings.TrimSpace(raw))
	if e != nil {
		return ""
	}
	return b.ResolveReference(u).String()
}
func urlAllowed(raw string, cfg settings) bool {
	trim := func(s string) string {
		return strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(s), "https://"), "http://")
	}
	value := trim(raw)
	allow := cfg.list("allowUrlPrefixes")
	deny := cfg.list("denyUrlPrefixes")
	if len(allow) > 0 {
		ok := false
		for _, prefix := range allow {
			if strings.HasPrefix(value, trim(prefix)) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	for _, prefix := range deny {
		if strings.HasPrefix(value, trim(prefix)) {
			return false
		}
	}
	return true
}

func previewAdmission(items []item, cfg settings, now time.Time) []item {
	after := parseDate(cfg.text("publishedAfter"), cfg)
	rewrite := cfg.object("itemUrlPrefixRewrite")
	filter := cfg.object("ingestNoiseFilter")
	kept := make([]item, 0, len(items))
	for _, candidate := range items {
		candidate.Title = truncate(plain(candidate.Title), 1000)
		candidate.Excerpt = truncate(plain(candidate.Excerpt), 2000)
		candidate.Body = truncate(plain(candidate.Body), 64000)
		if strings.TrimSpace(candidate.Title) == "" || !previewURLAllowed(candidate.URL, cfg) || !categoriesAllowedExact(candidate.Categories, cfg) {
			continue
		}
		if from, to := rewrite.text("from"), rewrite.text("to"); from != "" && strings.HasPrefix(candidate.URL, from) {
			candidate.URL = to + strings.TrimPrefix(candidate.URL, from)
		}
		title := strings.ToLower(candidate.Title)
		haystack := title + "\n" + strings.ToLower(candidate.Excerpt)
		if containsAny(haystack, filter.list("keepIfMatches")) {
			// The source's explicit keep rule takes precedence over all drop markers.
		} else if containsAny(title, filter.list("dropMarkersTitleOnly")) || containsAny(haystack, filter.list("dropMarkers")) {
			continue
		}
		if after != nil && (candidate.Published == nil || !candidate.Published.After(*after) || candidate.Published.After(now.Add(time.Hour))) {
			continue
		}
		kept = append(kept, candidate)
	}
	return kept
}

func previewURLAllowed(raw string, cfg settings) bool {
	overHTTPS := func(value string) string {
		if len(value) >= len("http://") && strings.EqualFold(value[:len("http://")], "http://") {
			return "https://" + value[len("http://"):]
		}
		return value
	}
	listed := overHTTPS(raw)
	allow := cfg.list("allowUrlPrefixes")
	deny := cfg.list("denyUrlPrefixes")
	for _, prefix := range deny {
		prefix = overHTTPS(prefix)
		if strings.HasPrefix(listed, prefix) {
			return false
		}
	}
	if len(allow) == 0 {
		return true
	}
	for _, prefix := range allow {
		prefix = overHTTPS(prefix)
		if strings.HasPrefix(listed, prefix) {
			return true
		}
	}
	return false
}

func categoriesAllowedExact(categories []string, cfg settings) bool {
	for _, category := range categories {
		for _, denied := range cfg.list("denyCategories") {
			if category == denied {
				return false
			}
		}
	}
	allowed := cfg.list("allowCategories")
	if len(allowed) == 0 {
		return true
	}
	for _, category := range categories {
		for _, permitted := range allowed {
			if category == permitted {
				return true
			}
		}
	}
	return false
}

func containsAny(text string, terms []string) bool {
	for _, term := range terms {
		if strings.Contains(text, strings.ToLower(term)) {
			return true
		}
	}
	return false
}

func truncateUTF16(value string, limit int) string {
	units := utf16.Encode([]rune(value))
	if len(units) > limit {
		units = units[:limit]
	}
	return string(utf16.Decode(units))
}
func categoriesAllowed(categories []string, cfg settings) bool {
	allowed := cfg.list("allowCategories")
	denied := cfg.list("denyCategories")
	hasAllow := len(allowed) == 0
	for _, cat := range categories {
		for _, deny := range denied {
			if strings.EqualFold(cat, deny) {
				return false
			}
		}
		for _, allow := range allowed {
			if strings.EqualFold(cat, allow) {
				hasAllow = true
			}
		}
	}
	return hasAllow
}
func parseDate(raw string, cfg settings) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	offset := cfg.text("publishedAtUtcOffset")
	seconds := 0
	if offset != "" {
		var h, m int
		sign := 1
		if offset[0] == '-' {
			sign = -1
		}
		clean := strings.TrimPrefix(strings.TrimPrefix(offset, "+"), "-")
		parts := strings.Split(clean, ":")
		h, _ = strconv.Atoi(parts[0])
		if len(parts) > 1 {
			m, _ = strconv.Atoi(parts[1])
		}
		seconds = sign * (h*3600 + m*60)
	}
	loc := time.FixedZone("source", seconds)
	for _, layout := range []string{time.RFC3339Nano, time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02", "20060102", "January 2, 2006", "Jan 2, 2006", "January 2 2006", "Jan 2 2006", "2 January 2006"} {
		if t, e := time.ParseInLocation(layout, raw, loc); e == nil {
			t = t.UTC()
			return &t
		}
	}
	return nil
}
func atPath(v any, path string) any {
	if path == "" {
		return v
	}
	for _, part := range strings.Split(path, ".") {
		switch x := v.(type) {
		case map[string]any:
			v = x[part]
		case []any:
			i, e := strconv.Atoi(part)
			if e != nil || i < 0 || i >= len(x) {
				return nil
			}
			v = x[i]
		default:
			return nil
		}
	}
	return v
}
func scalar(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	return ""
}
func firstPath(v any, paths []string) string {
	for _, path := range paths {
		if text := scalar(atPath(v, path)); strings.TrimSpace(text) != "" {
			return text
		}
	}
	return ""
}
