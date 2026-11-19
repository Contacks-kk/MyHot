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

var sourceConfigCommonKeys = []string{"publisherRole", "publisherUrlPrefixes", "_aihot", "allowUrlPrefixes", "denyUrlPrefixes", "ingestNoiseFilter", "itemUrlPrefixRewrite", "sortByPublishedAt", "detail", "fetchPublicContent", "publishedAfter"}
var sourceConfigNestedKeys = map[string][]string{
	"_aihot":               {"initialBackfillLimit", "initialBackfillMonths"},
	"ingestNoiseFilter":    {"dropMarkers", "dropMarkersTitleOnly", "keepIfMatches"},
	"itemUrlPrefixRewrite": {"from", "to"},
	"requireBoolean":       {"path", "equals"},
	"minNumeric":           {"path", "min"},
	"detail":               {"maxFetches", "publishedAtSelector", "publishedAtRegex", "publishedAtUtcOffset", "publishedAtAuthoritative", "upgradeDatePrecision", "titleSelector", "titleRegex", "titleAuthoritative", "summarySelector"},
}

/** unsupportedSourceConfig reports ignored keys and invalid values for collection adapters. */
func unsupportedSourceConfig(kind string, cfg settings) []string {
	allowed := append([]string(nil), sourceConfigCommonKeys...)
	switch kind {
	case "rss":
		allowed = append(allowed, "feedUrl", "summaryIsBody", "preserveUrlFragment", "allowCategories", "denyCategories")
	case "web_list":
		allowed = append(allowed, "url", "baseUrl", "parseMode", "adapter", "cacheToleranceSeconds", "linksStartLine", "preserveUrlFragment", "itemSelector", "linkSelector", "titleSelector", "publishedAtSelector", "publishedAtRegex", "publishedAtUtcOffset")
	case "json_list":
		allowed = append(allowed, "url", "mode", "method", "headers", "bodyJson", "jsonKey", "windowVar", "itemsPath", "itemsObjectValues", "titlePaths", "summaryPaths", "summaryIsBody", "authorPaths", "publishedAtPath", "publishedAtUnit", "publishedAtUtcOffset", "externalIdPath", "urlTemplate", "urlTemplateFallback", "rawDropKeys", "requireBoolean", "minNumeric")
	case "x_search":
		allowed = []string{"publisherRole", "publisherUrlPrefixes", "_aihot", "ingestNoiseFilter", "itemUrlPrefixRewrite", "query", "searchType"}
	case "mp_account":
		allowed = []string{"publisherRole", "publisherUrlPrefixes", "wxid", "ghid", "nickname"}
	case "external":
		allowed = []string{"publisherRole", "publisherUrlPrefixes"}
	default:
		return []string{"kind=" + kind}
	}
	allowedKeys := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		allowedKeys[key] = true
	}
	unknown := make([]string, 0)
	for key, value := range cfg {
		if !allowedKeys[key] {
			unknown = append(unknown, key)
			continue
		}
		if key == "publishedAfter" && !validUTCInstant(value) {
			unknown = append(unknown, key)
			continue
		}
		if key == "publisherUrlPrefixes" && !validPublisherPrefixes(value) {
			unknown = append(unknown, key)
			continue
		}
		if key == "adapter" && value != "mimo_home" {
			unknown = append(unknown, "adapter="+scalar(value))
			continue
		}
		if key == "parseMode" && !oneOf(scalar(value), "html", "markdown", "docusaurus_changelog", "intercom_changelog") {
			unknown = append(unknown, "parseMode="+scalar(value))
			continue
		}
		if key == "publisherRole" && !oneOf(scalar(value), "organization", "person") {
			unknown = append(unknown, "publisherRole="+scalar(value))
			continue
		}
		if nested, ok := sourceConfigNestedKeys[key]; ok {
			object, isObject := value.(map[string]any)
			if !isObject || object == nil {
				continue
			}
			validKeys := make(map[string]bool, len(nested))
			for _, nestedKey := range nested {
				validKeys[nestedKey] = true
			}
			for nestedKey := range object {
				if !validKeys[nestedKey] {
					unknown = append(unknown, key+"."+nestedKey)
				}
			}
		}
	}
	sort.Strings(unknown)
	return unknown
}

/** validUTCInstant accepts the exact UTC boundary format used by source configuration. */
func validUTCInstant(value any) bool {
	text, ok := value.(string)
	if !ok || !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{3})?Z$`).MatchString(text) {
		return false
	}
	for _, layout := range []string{"2006-01-02T15:04:05Z", "2006-01-02T15:04:05.000Z"} {
		if parsed, err := time.Parse(layout, text); err == nil && parsed.UTC().Format(layout) == text {
			return true
		}
	}
	return false
}

/** validPublisherPrefixes checks the URL shape required for source ownership rules. */
func validPublisherPrefixes(value any) bool {
	values, ok := value.([]any)
	if !ok {
		if typed, typedOK := value.([]string); typedOK {
			values = make([]any, len(typed))
			for index := range typed {
				values[index] = typed[index]
			}
		} else {
			return false
		}
	}
	for _, entry := range values {
		text, ok := entry.(string)
		if !ok {
			return false
		}
		parsed, err := url.Parse(text)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return false
		}
	}
	return true
}

/** oneOf checks whether a string matches one of the adapter's supported values. */
func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
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
	if offset == "" {
		offset = "+08:00"
	}
	if parsed := parseExplicitDateZone(raw); parsed != nil {
		return parsed
	}
	if regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(raw) {
		parsed, err := time.Parse("2006-01-02", raw)
		if err == nil {
			value := parsed.UTC()
			return &value
		}
		return nil
	}
	zone, ok := sourceDateLocation(offset)
	if !ok {
		return nil
	}
	if matches := numericDatePattern.FindStringSubmatch(raw); len(matches) > 0 {
		year, _ := strconv.Atoi(matches[1])
		month, _ := strconv.Atoi(matches[2])
		day, _ := strconv.Atoi(matches[3])
		hour, minute, second, nanosecond := 0, 0, 0, 0
		if matches[4] != "" {
			hour, _ = strconv.Atoi(matches[4])
			minute, _ = strconv.Atoi(matches[5])
			if matches[6] != "" {
				second, _ = strconv.Atoi(matches[6])
			}
			if matches[7] != "" {
				fraction := (matches[7] + "000000000")[:9]
				nanosecond, _ = strconv.Atoi(fraction)
			}
		}
		return sourceCalendarTime(year, month, day, hour, minute, second, nanosecond, zone)
	}
	if matches := englishDatePattern.FindStringSubmatch(raw); len(matches) > 0 {
		month, err := time.Parse("January", matches[1])
		if err != nil {
			month, err = time.Parse("Jan", matches[1])
		}
		if err != nil {
			return nil
		}
		day, _ := strconv.Atoi(matches[2])
		year, _ := strconv.Atoi(matches[3])
		hour, minute, second := dateTimeTail(matches, 4)
		return sourceCalendarTime(year, int(month.Month()), day, hour, minute, second, 0, zone)
	}
	if matches := dayFirstEnglishDatePattern.FindStringSubmatch(raw); len(matches) > 0 {
		month, err := time.Parse("January", matches[2])
		if err != nil {
			month, err = time.Parse("Jan", matches[2])
		}
		if err != nil {
			return nil
		}
		day, _ := strconv.Atoi(matches[1])
		year, _ := strconv.Atoi(matches[3])
		hour, minute, second := dateTimeTail(matches, 4)
		return sourceCalendarTime(year, int(month.Month()), day, hour, minute, second, 0, zone)
	}
	return nil
}

var numericDatePattern = regexp.MustCompile(`(\d{4})[-/.年](\d{1,2})[-/.月](\d{1,2})日?(?:(?:T|\s+)\s*(\d{1,2}):(\d{2})(?::(\d{2})(?:\.(\d+))?)?)?`)
var englishDatePattern = regexp.MustCompile(`(?i)\b([a-z]{3,9})\s+(\d{1,2})(?:st|nd|rd|th)?[,]?\s+(\d{4})(?:[ T]+(\d{1,2}):(\d{2})(?::(\d{2}))?)?`)
var dayFirstEnglishDatePattern = regexp.MustCompile(`(?i)\b(\d{1,2})\s+([a-z]{3,9})\s+(\d{4})(?:[ T]+(\d{1,2}):(\d{2})(?::(\d{2}))?)?`)

/** dateTimeTail extracts optional hour, minute, and second fields from a matched date. */
func dateTimeTail(matches []string, start int) (int, int, int) {
	hour, minute, second := 0, 0, 0
	if len(matches) > start && matches[start] != "" {
		hour, _ = strconv.Atoi(matches[start])
		minute, _ = strconv.Atoi(matches[start+1])
		if len(matches) > start+2 && matches[start+2] != "" {
			second, _ = strconv.Atoi(matches[start+2])
		}
	}
	return hour, minute, second
}

/** parseExplicitDateZone reads dates whose text already includes its own timezone. */
func parseExplicitDateZone(raw string) *time.Time {
	normalized := strings.TrimSpace(regexp.MustCompile(`(?i)UTC$`).ReplaceAllString(raw, "GMT"))
	zoneMatch := regexp.MustCompile(`(?i)\s+(EST|EDT|CST|CDT|MST|MDT|PST|PDT|BST|CET|CEST|JST|KST|AEST|AEDT)$`).FindStringSubmatch(normalized)
	if len(zoneMatch) > 1 {
		zoneOffsets := map[string]string{
			"EST": "-0500", "EDT": "-0400", "CST": "-0600", "CDT": "-0500", "MST": "-0700", "MDT": "-0600", "PST": "-0800", "PDT": "-0700",
			"BST": "+0100", "CET": "+0100", "CEST": "+0200", "JST": "+0900", "KST": "+0900", "AEST": "+1000", "AEDT": "+1100",
		}
		zone := zoneMatch[1]
		normalized = strings.TrimSpace(strings.TrimSuffix(normalized, zone)) + " " + zoneOffsets[strings.ToUpper(zone)]
	}
	if !regexp.MustCompile(`(?i)(?:Z|[+-]\d{2}:?\d{2}|GMT)\s*$`).MatchString(normalized) {
		return nil
	}
	for _, layout := range []string{
		time.RFC3339Nano, time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822,
		"2006-01-02 15:04:05Z07:00", "2006-01-02 15:04Z07:00", "2006-01-02T15:04Z07:00",
		"2006-01-02 15:04:05 -0700", "2006-01-02 15:04 -0700",
		"Mon, 02 Jan 2006 15:04:05 -0700", "Mon, 02 Jan 2006 15:04 -0700",
		"Jan 2, 2006 15:04:05 MST", "January 2, 2006 15:04:05 MST",
		"Jan 2, 2006 15:04:05 -0700", "Jan 2, 2006 15:04 -0700",
		"January 2, 2006 15:04:05 -0700", "January 2, 2006 15:04 -0700",
	} {
		if parsed, err := time.Parse(layout, normalized); err == nil {
			value := parsed.UTC()
			return &value
		}
	}
	return nil
}

/** sourceDateLocation validates a configured UTC offset and returns its fixed location. */
func sourceDateLocation(offset string) (*time.Location, bool) {
	match := regexp.MustCompile(`^([+-])(\d{2}):(\d{2})$`).FindStringSubmatch(offset)
	if len(match) != 4 {
		return nil, false
	}
	hours, _ := strconv.Atoi(match[2])
	minutes, _ := strconv.Atoi(match[3])
	if hours > 23 || minutes > 59 {
		return nil, false
	}
	seconds := hours*3600 + minutes*60
	if match[1] == "-" {
		seconds = -seconds
	}
	return time.FixedZone("source", seconds), true
}

/** sourceCalendarTime rejects impossible dates and interprets valid fields in the source's zone. */
func sourceCalendarTime(year, month, day, hour, minute, second, nanosecond int, zone *time.Location) *time.Time {
	if month < 1 || month > 12 || day < 1 || hour < 0 || hour > 23 || minute < 0 || minute > 59 || second < 0 || second > 59 {
		return nil
	}
	value := time.Date(year, time.Month(month), day, hour, minute, second, nanosecond, zone)
	if value.Year() != year || int(value.Month()) != month || value.Day() != day {
		return nil
	}
	utc := value.UTC()
	return &utc
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
