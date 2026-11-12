// Package articlebody extracts a readable, sanitized body from editorial pages and X Articles.
package articlebody

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"aihot/backend-go/internal/bodyhtml"
	"aihot/backend-go/internal/mediaurl"
	readability "codeberg.org/readeck/go-readability/v2"
	"github.com/PuerkitoBio/goquery"
	xhtml "golang.org/x/net/html"
)

const (
	minBodyChars     = 200
	maxArticleImages = 9
	maxImageAltRunes = 120
)

type Image struct {
	Kind   string  `json:"kind"`
	URL    string  `json:"url"`
	Width  *int    `json:"width"`
	Height *int    `json:"height"`
	Alt    *string `json:"alt,omitempty"`
}

type Extracted struct {
	HTML        string
	Text        string
	Images      []Image
	PublishedAt *time.Time
	Title       string
	XArticle    json.RawMessage
}

var trailingChrome = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^topics$`), regexp.MustCompile(`(?i)^tags?$`),
	regexp.MustCompile(`(?i)^latest in [\p{L}\s&-]{1,30}$`), regexp.MustCompile(`(?i)^latest (news|stories)$`),
	regexp.MustCompile(`(?i)^most (popular|read)$`), regexp.MustCompile(`(?i)^related (articles|posts|stories|content|reading|coverage)$`),
	regexp.MustCompile(`(?i)^more (stories|articles|news)$`), regexp.MustCompile(`(?i)^(keep reading|read next|up next)$`),
	regexp.MustCompile(`(?i)^(you (might|may) also like|recommended( for you)?)$`),
	regexp.MustCompile(`(?i)^(subscribe|sign up)\b.{0,80}$`), regexp.MustCompile(`(?i)^share (this|on)\b.{0,40}$`),
	regexp.MustCompile(`(?i)^follow us\b.{0,40}$`), regexp.MustCompile(`(?i)^advertisement$`),
}

// ParseHTML mirrors the TypeScript Readability threshold, body whitelist and source-relative URLs.
func ParseHTML(raw, pageURL, utcOffset string) (*Extracted, error) {
	base, err := url.Parse(pageURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Hostname() == "" {
		return nil, errors.New("article page URL is invalid")
	}
	parser := readability.NewParser()
	parser.CharThresholds = minBodyChars
	article, err := parser.Parse(strings.NewReader(raw), base)
	if err != nil || article.Node == nil {
		return nil, nil
	}
	var rendered bytes.Buffer
	if err := article.RenderHTML(&rendered); err != nil {
		return nil, err
	}
	clean, _, err := bodyhtml.Sanitize(rendered.String(), pageURL, "", time.Now())
	if err != nil {
		return nil, err
	}
	clean = trimTrailingChrome(clean)
	text, err := visibleText(clean)
	if err != nil || utf16Length(text) < minBodyChars {
		return nil, err
	}
	images, err := extractImages(clean)
	if err != nil {
		return nil, err
	}
	published := parsePublishedDate(raw, utcOffset)
	if published == nil {
		if timestamp, timestampErr := article.PublishedTime(); timestampErr == nil {
			value := timestamp.UTC()
			published = &value
		}
	}
	return &Extracted{HTML: clean, Text: text, Images: images, PublishedAt: published}, nil
}

func trimTrailingChrome(raw string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(raw))
	if err != nil {
		return raw
	}
	container := doc.Find("body").First()
	blocks := container.Children().Nodes
	removed := false
	for len(blocks) > 1 {
		last := goquery.NewDocumentFromNode(blocks[len(blocks)-1]).Selection
		if last.Find("img, video, picture").Length() > 0 {
			break
		}
		text := strings.Join(strings.Fields(last.Text()), " ")
		if strings.ContainsRune(".。!！?？:：", lastRune(text)) || !matchesTrailingChrome(text) {
			break
		}
		last.Remove()
		blocks = blocks[:len(blocks)-1]
		removed = true
	}
	if !removed {
		return raw
	}
	var output bytes.Buffer
	for _, node := range container.Children().Nodes {
		if err := xhtml.Render(&output, node); err != nil {
			return raw
		}
	}
	return output.String()
}

func matchesTrailingChrome(value string) bool {
	for _, pattern := range trailingChrome {
		if pattern.MatchString(value) {
			return true
		}
	}
	return false
}

func lastRune(value string) rune {
	runes := []rune(value)
	if len(runes) == 0 {
		return 0
	}
	return runes[len(runes)-1]
}

func visibleText(raw string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(raw))
	if err != nil {
		return "", err
	}
	return strings.Join(strings.Fields(doc.Selection.Text()), " "), nil
}

/** extractImages collects up to nine deduplicated content images and optional captions from sanitized HTML. */
func extractImages(raw string) ([]Image, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(raw))
	if err != nil {
		return nil, err
	}

	images := make([]Image, 0, maxArticleImages)
	seen := make(map[string]struct{}, maxArticleImages)

	// Walk sanitized <img> elements in reading order, filtering noise and tracking captions.
	doc.Find("img").EachWithBreak(func(_ int, selection *goquery.Selection) bool {
		source, _ := selection.Attr("src")
		source = strings.TrimSpace(source)
		if source == "" {
			return true
		}

		if _, exists := seen[source]; exists {
			return true
		}

		width := imageDimension(selection, "width")
		height := imageDimension(selection, "height")

		var wFloat, hFloat *float64
		if width != nil {
			v := float64(*width)
			wFloat = &v
		}
		if height != nil {
			v := float64(*height)
			hFloat = &v
		}

		if mediaurl.IsNonArticleImage(source, wFloat, hFloat) {
			return true
		}

		altRaw, _ := selection.Attr("alt")
		if strings.TrimSpace(altRaw) == "" {
			altRaw = selection.Closest("figure").Find("figcaption").First().Text()
		}

		var altPtr *string
		if cleanedAlt := cleanCaption(altRaw, maxImageAltRunes); cleanedAlt != "" {
			altPtr = &cleanedAlt
		}

		seen[source] = struct{}{}
		images = append(images, Image{
			Kind:   "image",
			URL:    source,
			Width:  width,
			Height: height,
			Alt:    altPtr,
		})

		return len(images) < maxArticleImages
	})

	return images, nil
}

/** cleanCaption normalizes whitespace and bounds extracted image caption length. */
func cleanCaption(raw string, limit int) string {
	clean := strings.Join(strings.Fields(strings.TrimSpace(raw)), " ")
	if clean == "" || limit <= 0 {
		return ""
	}

	runes := []rune(clean)
	if len(runes) <= limit {
		return clean
	}

	return string(runes[:limit]) + "…"
}

func imageDimension(selection *goquery.Selection, name string) *int {
	raw, ok := selection.Attr(name)
	if !ok {
		return nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return nil
	}
	return &value
}

var datePattern = regexp.MustCompile(`(?i)(\d{4})[-/.年](\d{1,2})[-/.月](\d{1,2})日?(?:[T\s]+(\d{1,2}):(\d{2})(?::(\d{2}))?)?`)
var explicitZone = regexp.MustCompile(`(?i)\d{1,2}:\d{2}(?::\d{2}(?:\.\d+)?)?\s*(?:Z|[+-]\d{2}:?\d{2}|GMT|UTC)\b`)
var zoneSuffixPattern = regexp.MustCompile(`(?i)(Z|[+-]\d{2}:?\d{2}|GMT|UTC)$`)

func parsePublishedDate(raw, utcOffset string) *time.Time {
	if utcOffset == "" {
		utcOffset = "+08:00"
	}
	value := publishedMetadata(raw)
	if value == "" {
		return nil
	}
	value = strings.TrimSpace(value)
	if parsed, err := time.Parse("2006-01-02", value); err == nil {
		return timePtr(parsed.UTC())
	}
	if explicitZone.MatchString(value) {
		for _, layout := range []string{
			time.RFC3339Nano, time.RFC3339, time.RFC1123Z, time.RFC1123,
			"2006-01-02 15:04:05 -0700", "2006-01-02 15:04:05 MST", "2006-01-02 15:04:05Z07:00",
			"Jan 2, 2006 15:04:05 MST", "January 2, 2006 15:04:05 MST",
		} {
			if parsed, err := time.Parse(layout, value); err == nil {
				return timePtr(parsed.UTC())
			}
		}
	}
	if numeric := datePattern.FindStringSubmatch(value); numeric != nil {
		datePart := numeric[1] + "-" + two(numeric[2]) + "-" + two(numeric[3])
		timePart := "00:00:00"
		if numeric[4] != "" {
			timePart = two(numeric[4]) + ":" + numeric[5] + ":" + twoDefault(numeric[6], "00")
		}
		if explicitZone.MatchString(value) {
			parsed, err := time.Parse(time.RFC3339, datePart+"T"+timePart+zoneSuffix(value))
			if err == nil {
				return timePtr(parsed.UTC())
			}
			return nil
		}
		location, err := time.Parse("-07:00", utcOffset)
		if err != nil {
			return nil
		}
		_, offset := location.Zone()
		zone := time.FixedZone(utcOffset, offset)
		parsed, err := time.ParseInLocation("2006-01-02 15:04:05", datePart+" "+timePart, zone)
		if err != nil {
			return nil
		}
		return timePtr(parsed.UTC())
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, time.RFC1123Z, time.RFC1123, "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return timePtr(parsed.UTC())
		}
	}
	offset, err := time.Parse("-07:00", utcOffset)
	if err != nil {
		return nil
	}
	_, offsetSeconds := offset.Zone()
	for _, layout := range []string{"Jan 2, 2006 15:04:05", "January 2, 2006 15:04:05", "Jan 2, 2006", "January 2, 2006"} {
		if parsed, err := time.ParseInLocation(layout, value, time.FixedZone(utcOffset, offsetSeconds)); err == nil {
			return timePtr(parsed.UTC())
		}
	}
	return nil
}

func publishedMetadata(raw string) string {
	doc, err := xhtml.Parse(strings.NewReader(raw))
	if err != nil {
		return ""
	}
	var result string
	var walk func(*xhtml.Node)
	walk = func(node *xhtml.Node) {
		if result != "" {
			return
		}
		if node.Type == xhtml.ElementNode && node.Data == "meta" {
			key := ""
			content := ""
			for _, attribute := range node.Attr {
				name := strings.ToLower(attribute.Key)
				if name == "property" || name == "name" || name == "itemprop" {
					key = strings.ToLower(attribute.Val)
				} else if name == "content" {
					content = strings.TrimSpace(attribute.Val)
				}
			}
			if content != "" && (strings.Contains(key, "published_time") || strings.Contains(key, "datepublished") || strings.Contains(key, "pubdate") || strings.Contains(key, "pub-date") || key == "date") {
				result = content
				return
			}
		}
		if node.Type == xhtml.ElementNode && node.Data == "script" {
			isJSONLD := false
			for _, attribute := range node.Attr {
				if attribute.Key == "type" && strings.EqualFold(strings.TrimSpace(attribute.Val), "application/ld+json") {
					isJSONLD = true
					break
				}
			}
			if isJSONLD {
				var script strings.Builder
				for child := node.FirstChild; child != nil; child = child.NextSibling {
					if child.Type == xhtml.TextNode {
						script.WriteString(child.Data)
					}
				}
				result = jsonLDPublishedDate([]byte(script.String()))
				if result != "" {
					return
				}
			}
		}
		if node.Type == xhtml.ElementNode && node.Data == "time" {
			for _, attribute := range node.Attr {
				if attribute.Key == "datetime" && strings.TrimSpace(attribute.Val) != "" {
					result = strings.TrimSpace(attribute.Val)
					return
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return result
}

func jsonLDPublishedDate(raw []byte) string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	var find func(any) string
	find = func(current any) string {
		switch item := current.(type) {
		case map[string]any:
			for key, value := range item {
				if strings.EqualFold(key, "datePublished") || strings.EqualFold(key, "dateCreated") {
					if date, ok := value.(string); ok && strings.TrimSpace(date) != "" {
						return strings.TrimSpace(date)
					}
				}
			}
			for _, value := range item {
				if date := find(value); date != "" {
					return date
				}
			}
		case []any:
			for _, value := range item {
				if date := find(value); date != "" {
					return date
				}
			}
		}
		return ""
	}
	return find(value)
}

func zoneSuffix(value string) string {
	zone := zoneSuffixPattern.FindString(strings.TrimSpace(value))
	if strings.EqualFold(zone, "GMT") || strings.EqualFold(zone, "UTC") {
		return "Z"
	}
	if zone == "" {
		return "Z"
	}
	if len(zone) == 5 && zone[3] != ':' {
		zone = zone[:3] + ":" + zone[3:]
	}
	return zone
}

func two(value string) string {
	if len(value) == 1 {
		return "0" + value
	}
	return value
}

func twoDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return two(value)
}

func timePtr(value time.Time) *time.Time { return &value }

func utf16Length(value string) int { return len(utf16.Encode([]rune(value))) }
