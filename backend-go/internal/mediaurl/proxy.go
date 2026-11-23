package mediaurl

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	imageWindow      = 24 * time.Hour
	defaultMediaCap  = 9
	defaultImageLife = 48 * time.Hour
)

// MediaItem mirrors the metadata object stored inside articles.media JSONB.
type MediaItem struct {
	Kind   string  `json:"kind"`
	URL    string  `json:"url"`
	Width  *int    `json:"width,omitempty"`
	Height *int    `json:"height,omitempty"`
	Alt    *string `json:"alt,omitempty"`
	Poster *string `json:"poster,omitempty"`
}

// MediaView matches the signed image card DTO shared with the React frontend.
type MediaView struct {
	Kind    string   `json:"kind"`
	URL     string   `json:"url"`
	FullURL *string  `json:"fullUrl,omitempty"`
	Width   *float64 `json:"width"`
	Height  *float64 `json:"height"`
	Alt     *string  `json:"alt"`
	Poster  *string  `json:"poster"`
	SrcSet  *string  `json:"srcSet,omitempty"`
}

type rawMediaItem struct {
	Kind   string   `json:"kind"`
	URL    string   `json:"url"`
	Width  *float64 `json:"width"`
	Height *float64 `json:"height"`
	Alt    *string  `json:"alt"`
	Poster *string  `json:"poster"`
}

var (
	imageExpiryPattern    = regexp.MustCompile(`^\d{9,11}$`)
	imageSignaturePattern = regexp.MustCompile(`(?i)^(?:[0-9a-f]{16}|[0-9a-f]{64})$`)
)

type VerifyResult struct {
	URL    string
	Mode   string
	Reason string
	OK     bool
}

// ProxyURL creates the same signed rendition URL used by the existing image proxy.
func ProxyURL(raw *string, mode string, now time.Time, lifetime time.Duration, secret string) (*string, error) {
	if raw == nil || *raw == "" {
		return nil, nil
	}
	if strings.HasPrefix(strings.ToLower(*raw), "data:") {
		return raw, nil
	}
	if !isHTTPURL(*raw) || IsNonArticleImage(*raw, nil, nil) {
		return nil, nil
	}
	if secret == "" {
		return nil, errors.New("IMG_PROXY_SIGN_SECRET is not configured")
	}
	signed := signProxyURL(*raw, mode, now, lifetime, secret)
	return &signed, nil
}

/** SourceIconProxyURL signs a stored source icon, including favicons excluded from article media. */
func SourceIconProxyURL(raw *string, now time.Time, secret string) *string {
	if raw == nil || secret == "" || !isHTTPURL(*raw) {
		return nil
	}
	signed := signProxyURL(*raw, "avatar-96", now, defaultImageLife, secret)
	return &signed
}

/** SourceIconDisplayURL serves a stored source icon through the proxy or directly over HTTPS. */
func SourceIconDisplayURL(raw *string, now time.Time, secret string) *string {
	if raw == nil {
		return nil
	}
	if secret != "" {
		return SourceIconProxyURL(raw, now, secret)
	}
	icon, err := url.Parse(*raw)
	if err != nil || icon.Scheme != "https" || icon.Hostname() == "" || icon.User != nil {
		return nil
	}
	return raw
}

/** signProxyURL builds the signed URL for a validated image. */
func signProxyURL(raw, mode string, now time.Time, lifetime time.Duration, secret string) string {
	if now.IsZero() {
		now = time.Now()
	}
	windowSeconds := int64(imageWindow / time.Second)
	expiry := int64(math.Ceil(float64(now.Add(lifetime).UnixNano())/float64(imageWindow.Nanoseconds())) * float64(windowSeconds))
	message := fmt.Sprintf("%s|%s|%d", raw, mode, expiry)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(message))
	signature := hex.EncodeToString(mac.Sum(nil))[:16]
	return "/api/img-proxy?u=" + encodeURIComponent(raw) + "&mode=" + encodeURIComponent(mode) + "&exp=" + fmt.Sprint(expiry) + "&sig=" + signature
}

func encodeURIComponent(value string) string {
	encoded := url.QueryEscape(value)
	encoded = strings.ReplaceAll(encoded, "+", "%20")
	for escaped, literal := range map[string]string{
		"%21": "!", "%27": "'", "%28": "(", "%29": ")", "%2A": "*", "%7E": "~",
	} {
		encoded = strings.ReplaceAll(encoded, escaped, literal)
		encoded = strings.ReplaceAll(encoded, strings.ToLower(escaped), literal)
	}
	return encoded
}

func ProxyURLSet(raw *string, modes []string, widths []int, now time.Time, lifetime time.Duration, secret string) (*string, error) {
	if raw == nil || len(modes) == 0 || len(modes) != len(widths) || !isHTTPURL(*raw) || IsNonArticleImage(*raw, nil, nil) {
		return nil, nil
	}
	values := make([]string, 0, len(modes))
	for index, mode := range modes {
		value, err := ProxyURL(raw, mode, now, lifetime, secret)
		if err != nil {
			return nil, err
		}
		if value != nil {
			values = append(values, fmt.Sprintf("%s %dw", *value, widths[index]))
		}
	}
	if len(values) == 0 {
		return nil, nil
	}
	set := strings.Join(values, ", ")
	return &set, nil
}

// VerifyProxyRequest accepts current signatures and the full-length signatures and
// mode-less URLs issued by older releases. A missing mode was signed as "default"
// and is served as the full rendition, matching the TypeScript image proxy.
func VerifyProxyRequest(params url.Values, now time.Time, secret string) VerifyResult {
	rawURL := params.Get("u")
	mode, hasMode := params["mode"]
	expiry := params.Get("exp")
	signature := params.Get("sig")
	if rawURL == "" || (hasMode && (len(mode) == 0 || mode[0] == "")) || expiry == "" || signature == "" {
		return VerifyResult{Reason: "missing"}
	}
	if !imageExpiryPattern.MatchString(expiry) {
		return VerifyResult{Reason: "missing"}
	}
	seconds, err := parseExpiry(expiry)
	if err != nil {
		return VerifyResult{Reason: "missing"}
	}
	if now.IsZero() {
		now = time.Now()
	}
	if seconds < now.Unix() {
		return VerifyResult{Reason: "expired"}
	}
	if !isHTTPURL(rawURL) {
		return VerifyResult{Reason: "bad-url"}
	}
	if secret == "" {
		return VerifyResult{Reason: "bad-signature"}
	}
	if !imageSignaturePattern.MatchString(signature) {
		return VerifyResult{Reason: "bad-signature"}
	}
	canonicalMode := "default"
	if hasMode && len(mode) > 0 && mode[0] != "" {
		canonicalMode = mode[0]
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(fmt.Sprintf("%s|%s|%s", rawURL, canonicalMode, expiry)))
	want, err := hex.DecodeString(hex.EncodeToString(mac.Sum(nil))[:len(signature)])
	if err != nil {
		return VerifyResult{Reason: "bad-signature"}
	}
	got, err := hex.DecodeString(signature)
	if err != nil || !hmac.Equal(got, want) {
		return VerifyResult{Reason: "bad-signature"}
	}
	resultMode := canonicalMode
	if resultMode == "default" {
		resultMode = "full"
	}
	return VerifyResult{URL: rawURL, Mode: resultMode, OK: true}
}

func parseExpiry(value string) (int64, error) {
	var seconds int64
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return 0, errors.New("invalid image expiry")
		}
		seconds = seconds*10 + int64(digit-'0')
	}
	return seconds, nil
}

func isHTTPURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && (strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https")) && parsed.Hostname() != ""
}

/** IsNonArticleImage reports whether a candidate image looks like an icon, avatar, tracker, or non-article asset. */
func IsNonArticleImage(raw string, width, height *float64) bool {
	if width != nil && height != nil {
		if (*width > 0 && *height > 0) && ((*width <= 120 && *height <= 120) || *width < 80 || *height < 80) {
			return true
		}

		if *height > 0 && (*width / *height) > 12 {
			return true
		}

		if *width <= 1 && *height <= 1 {
			return true
		}
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}

	host := strings.ToLower(parsed.Hostname())
	if strings.HasPrefix(host, "www.") {
		host = strings.TrimPrefix(host, "www.")
	} else if strings.HasPrefix(host, "m.") {
		host = strings.TrimPrefix(host, "m.")
	}

	switch host {
	case "ids4.ad.gt", "ids.ad.gt", "secure.adnxs.com", "sync.1rx.io", "ssum-sec.casalemedia.com", "sync.smartadserver.com", "token.rubiconproject.com", "image2.pubmatic.com", "sync.go.sonobi.com", "onetag-sys.com":
		return true
	case "youtube.com":
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		return len(parts) == 1 && parts[0] == "watch" || len(parts) == 2 && parts[0] == "shorts"
	}

	lowerPath := strings.ToLower(parsed.Path)
	for _, marker := range []string{
		"/avatar/",
		"/avatars/",
		"/emoji/",
		"/emojis/",
		"/favicon",
		"/badges/",
		"favicon.",
		"spacer.gif",
		"pixel.gif",
		"pixel.png",
		"blank.gif",
		"1x1.",
		"qrcode",
	} {
		if strings.Contains(lowerPath, marker) {
			return true
		}
	}

	return false
}

/** BuildViews converts raw stored media JSONB into signed proxy MediaView slices capped at limit. */
func BuildViews(rawJSON json.RawMessage, thumbMode string, now time.Time, secret string, limit int) []MediaView {
	views := []MediaView{}
	if len(rawJSON) == 0 || secret == "" {
		return views
	}

	var rawItems []rawMediaItem
	if err := json.Unmarshal(rawJSON, &rawItems); err != nil || len(rawItems) == 0 {
		return views
	}

	if limit <= 0 || limit > defaultMediaCap {
		limit = defaultMediaCap
	}

	if thumbMode == "" {
		thumbMode = "image-336"
	}

	seen := make(map[string]struct{}, len(rawItems))

	// Sign and deduplicate each valid media entry up to the requested display cap.
	for _, entry := range rawItems {
		target := strings.TrimSpace(entry.URL)
		if target == "" {
			continue
		}

		if _, exists := seen[target]; exists {
			continue
		}

		if IsNonArticleImage(target, entry.Width, entry.Height) {
			continue
		}

		thumbURL, err := ProxyURL(&target, thumbMode, now, defaultImageLife, secret)
		if err != nil || thumbURL == nil {
			continue
		}

		fullURL, err := ProxyURL(&target, "full", now, defaultImageLife, secret)
		if err != nil {
			continue
		}

		posterSource := target
		if entry.Poster != nil && strings.TrimSpace(*entry.Poster) != "" {
			posterSource = strings.TrimSpace(*entry.Poster)
		}

		srcSet, _ := ProxyURLSet(&posterSource, []string{"image-336", "image-720"}, []int{336, 720}, now, defaultImageLife, secret)

		var poster *string
		if entry.Poster != nil && strings.TrimSpace(*entry.Poster) != "" {
			cleaned := strings.TrimSpace(*entry.Poster)
			poster, _ = ProxyURL(&cleaned, thumbMode, now, defaultImageLife, secret)
		}

		kind := "image"
		if strings.EqualFold(strings.TrimSpace(entry.Kind), "video") {
			kind = "video"
		}

		var alt *string
		if entry.Alt != nil {
			trimmedAlt := strings.TrimSpace(*entry.Alt)
			if trimmedAlt != "" {
				alt = &trimmedAlt
			}
		}

		seen[target] = struct{}{}
		views = append(views, MediaView{
			Kind:    kind,
			URL:     *thumbURL,
			FullURL: fullURL,
			Width:   entry.Width,
			Height:  entry.Height,
			Alt:     alt,
			Poster:  poster,
			SrcSet:  srcSet,
		})

		if len(views) >= limit {
			break
		}
	}

	return views
}
