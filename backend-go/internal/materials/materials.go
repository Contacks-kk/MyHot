// Package materials contains content identity and timeline rules shared by
// material-ingestion paths.
package materials

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	StaleOnDiscovery = 48 * time.Hour
	FutureTolerance  = time.Hour
)

type TimelineDecision struct {
	PublishedAt    *time.Time
	TimelineAt     time.Time
	Backfill       bool
	BackfillReason string
}

// DecideTimeline applies the same publication-time trust and backfill rules as
// the TypeScript material entry point.
func DecideTimeline(claimed *time.Time, discoveredAt time.Time, explicitBackfill string) TimelineDecision {
	var published *time.Time
	if claimed != nil && !claimed.IsZero() && !claimed.After(discoveredAt.Add(FutureTolerance)) {
		value := claimed.UTC()
		published = &value
	}

	reason := explicitBackfill
	if reason == "" {
		switch {
		case published == nil:
			reason = "unknown-publication-time"
		case discoveredAt.Sub(*published) > StaleOnDiscovery:
			reason = "stale-on-discovery"
		}
	}

	timeline := discoveredAt
	if reason != "" && published != nil {
		timeline = *published
	}
	return TimelineDecision{
		PublishedAt:    published,
		TimelineAt:     timeline,
		Backfill:       reason != "",
		BackfillReason: reason,
	}
}

// CollapseWhitespace matches the content normalization used before hashing.
func CollapseWhitespace(value string) string {
	return strings.Join(strings.FieldsFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || r == '\uFEFF'
	}), " ")
}

// ContentHash identifies the normalized title, body, and excerpt tuple.
func ContentHash(title, body, excerpt string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		CollapseWhitespace(title),
		CollapseWhitespace(body),
		CollapseWhitespace(excerpt),
	}, "\x01")))
	return hex.EncodeToString(sum[:])
}

// SameBarringLoss treats runs of U+FFFD as one unknown character, matching
// feeds whose text decoder intermittently loses characters.
func SameBarringLoss(a, b string) bool {
	x := lossComparable(a)
	y := lossComparable(b)
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] && x[i] != '\uFFFD' && y[i] != '\uFFFD' {
			return false
		}
	}
	return true
}

func lossComparable(value string) []rune {
	runes := []rune(CollapseWhitespace(value))
	result := make([]rune, 0, len(runes))
	for _, current := range runes {
		if current == '\uFFFD' && len(result) > 0 && result[len(result)-1] == '\uFFFD' {
			continue
		}
		result = append(result, current)
	}
	return result
}

var trackingParameter = regexp.MustCompile(`(?i)^(utm_[a-z]+|spm|from|ref|ref_src|ref_url|source|share_source|share_token|fbclid|gclid|igshid|mc_cid|mc_eid|_hsenc|_hsmi|scene|chksm|sessionid|srcid|clicktime|enterid|mkt_tok)$`)

// NormalizeURL produces the canonical material URL used for deduplication.
func NormalizeURL(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (!strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https")) || u.Hostname() == "" {
		return "", false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	port := u.Port()
	u.Scheme = "https"
	u.Host = host
	if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	}
	if port != "" && port != "80" && port != "443" {
		u.Host = net.JoinHostPort(host, port)
	}
	u.Fragment, u.RawFragment = "", ""
	query := u.Query()
	queryOrder := []string(nil)
	if host == "mp.weixin.qq.com" {
		kept := url.Values{}
		queryOrder = []string{"__biz", "mid", "idx", "sn"}
		for _, key := range queryOrder {
			if value := query.Get(key); value != "" {
				kept.Set(key, value)
			}
		}
		query = kept
	} else {
		for key := range query {
			if trackingParameter.MatchString(key) {
				query.Del(key)
			}
		}
	}
	if queryOrder != nil {
		u.RawQuery = encodeMaterialQueryInOrder(query, queryOrder)
	} else {
		u.RawQuery = encodeMaterialQuery(query)
	}
	if u.Path == "" {
		u.Path = "/"
	}
	if u.Path != "/" && u.RawQuery == "" && u.Fragment == "" {
		if strings.HasSuffix(u.Path, "/") {
			u.Path = u.Path[:len(u.Path)-1]
		}
		u.RawPath = ""
	}
	return u.String(), true
}

// IdentityKeyForURL matches tweet identities and canonical URL identities.
func IdentityKeyForURL(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (!strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https")) {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	if strings.HasPrefix(host, "www.") {
		host = strings.TrimPrefix(host, "www.")
	} else if strings.HasPrefix(host, "mobile.") || strings.HasPrefix(host, "m.") {
		host = host[strings.IndexByte(host, '.')+1:]
	}
	if host == "x.com" || host == "twitter.com" {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) >= 3 && (strings.EqualFold(parts[1], "status") || strings.EqualFold(parts[1], "statuses")) && parts[2] != "" {
			valid := true
			for _, r := range parts[2] {
				if r < '0' || r > '9' {
					valid = false
					break
				}
			}
			if valid {
				return "x:" + parts[2], true
			}
		}
	}
	normalized, ok := NormalizeURL(raw)
	if !ok {
		return "", false
	}
	return "url:" + normalized, true
}

func encodeMaterialQuery(values url.Values) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return encodeMaterialQueryInOrder(values, keys)
}

func encodeMaterialQueryInOrder(values url.Values, keys []string) string {
	parts := make([]string, 0)
	for _, key := range keys {
		for _, value := range values[key] {
			parts = append(parts, materialQueryEscape(key)+"="+materialQueryEscape(value))
		}
	}
	return strings.Join(parts, "&")
}

func materialQueryEscape(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(url.QueryEscape(value), "~", "%7E"), "%2A", "*")
}
