package collector

import (
	"bytes"
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"aihot/backend-go/internal/mediaurl"
	"github.com/PuerkitoBio/goquery"
	"github.com/mmcdole/gofeed"
)

const (
	maxRSSMediaItems = 9
	maxAltRunes      = 120
)

/** parseRSS parses RSS, Atom, or Sitemap XML feeds into normalized items with non-downloading image metadata. */
func parseRSS(data []byte, base string) ([]item, error) {
	// Detect and parse Sitemap XML when applicable
	if isSitemapXML(data) {
		return parseSitemap(data, base)
	}

	feed, err := gofeed.NewParser().Parse(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}

	items := []item{}
	for _, entry := range feed.Items {
		if strings.TrimSpace(entry.Link) == "" {
			continue
		}

		published := entry.PublishedParsed
		if published == nil {
			published = entry.UpdatedParsed
		}

		author := ""
		if entry.Author != nil {
			author = entry.Author.Name
		}

		resolvedItemURL := resolveURL(base, entry.Link)
		mediaJSON := extractRSSMedia(entry, resolvedItemURL, base)

		items = append(items, item{
			URL:        resolvedItemURL,
			Title:      entry.Title,
			Excerpt:    entry.Description,
			Body:       entry.Content,
			Author:     author,
			Media:      mediaJSON,
			Published:  published,
			Categories: entry.Categories,
		})
	}

	return items, nil
}

/** extractRSSMedia collects, deduplicates, filters, and caps image metadata from one RSS entry without downloading pixels. */
func extractRSSMedia(entry *gofeed.Item, itemURL, base string) json.RawMessage {
	if entry == nil {
		return nil
	}

	urlBase := base
	if itemURL != "" {
		urlBase = itemURL
	}

	candidates := make([]mediaurl.MediaItem, 0, maxRSSMediaItems)
	seen := make(map[string]struct{}, maxRSSMediaItems)

	appendCandidate := func(rawURL string, width, height *int, alt string) {
		if len(candidates) >= maxRSSMediaItems {
			return
		}

		trimmed := strings.TrimSpace(rawURL)
		if trimmed == "" || strings.HasPrefix(strings.ToLower(trimmed), "data:") {
			return
		}

		resolved := resolveURL(urlBase, trimmed)
		parsed, err := url.Parse(resolved)
		if err != nil || (!strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https")) || parsed.Hostname() == "" {
			return
		}

		if _, exists := seen[resolved]; exists {
			return
		}

		var wFloat, hFloat *float64
		if width != nil {
			v := float64(*width)
			wFloat = &v
		}
		if height != nil {
			v := float64(*height)
			hFloat = &v
		}

		if mediaurl.IsNonArticleImage(resolved, wFloat, hFloat) {
			return
		}

		var altPtr *string
		if cleanAlt := truncateCaption(alt, maxAltRunes); cleanAlt != "" {
			altPtr = &cleanAlt
		}

		seen[resolved] = struct{}{}
		candidates = append(candidates, mediaurl.MediaItem{
			Kind:   "image",
			URL:    resolved,
			Width:  width,
			Height: height,
			Alt:    altPtr,
		})
	}

	// 1. Standard RSS/Atom image enclosures.
	for _, enc := range entry.Enclosures {
		if enc == nil {
			continue
		}

		encType := strings.ToLower(strings.TrimSpace(enc.Type))
		if strings.HasPrefix(encType, "image/") || (encType == "" && looksLikeImageURL(enc.URL)) {
			appendCandidate(enc.URL, nil, nil, "")
		}
	}

	// 2. Media RSS extensions (media:content, media:thumbnail) and iTunes image.
	if mediaExt, ok := entry.Extensions["media"]; ok {
		for _, tagName := range []string{"content", "thumbnail"} {
			for _, ext := range mediaExt[tagName] {
				medium := strings.ToLower(strings.TrimSpace(ext.Attrs["medium"]))
				extType := strings.ToLower(strings.TrimSpace(ext.Attrs["type"]))
				if tagName == "content" && medium != "" && medium != "image" && !strings.HasPrefix(extType, "image/") {
					continue
				}

				appendCandidate(
					ext.Attrs["url"],
					parsePositiveAttrInt(ext.Attrs["width"]),
					parsePositiveAttrInt(ext.Attrs["height"]),
					"",
				)
			}
		}
	}

	if entry.ITunesExt != nil && entry.ITunesExt.Image != "" {
		appendCandidate(entry.ITunesExt.Image, nil, nil, "")
	}

	// 3. Item-level RSS image (never feed-level logo).
	if entry.Image != nil && entry.Image.URL != "" {
		appendCandidate(entry.Image.URL, nil, nil, entry.Image.Title)
	}

	// 4. Inline <img> elements embedded inside Content or Description HTML.
	for _, htmlChunk := range []string{entry.Content, entry.Description} {
		if len(candidates) >= maxRSSMediaItems || strings.TrimSpace(htmlChunk) == "" {
			continue
		}

		doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlChunk))
		if err != nil {
			continue
		}

		doc.Find("img").EachWithBreak(func(_ int, sel *goquery.Selection) bool {
			src := firstAttr(sel, "src", "data-src", "data-original")
			if src == "" {
				return true
			}

			altText := firstAttr(sel, "alt", "title")
			if altText == "" {
				altText = sel.Closest("figure").Find("figcaption").First().Text()
			}

			wRaw, _ := sel.Attr("width")
			hRaw, _ := sel.Attr("height")
			appendCandidate(src, parsePositiveAttrInt(wRaw), parsePositiveAttrInt(hRaw), altText)
			return len(candidates) < maxRSSMediaItems
		})
	}

	if len(candidates) == 0 {
		return nil
	}

	encoded, err := json.Marshal(candidates)
	if err != nil {
		return nil
	}

	return encoded
}

/** firstAttr returns the first non-empty attribute value among keys on a DOM selection. */
func firstAttr(sel *goquery.Selection, keys ...string) string {
	for _, key := range keys {
		if value, ok := sel.Attr(key); ok {
			trimmed := strings.TrimSpace(value)
			if trimmed != "" {
				return trimmed
			}
		}
	}

	return ""
}

/** parsePositiveAttrInt parses a positive integer HTML/XML dimension attribute. */
func parsePositiveAttrInt(raw string) *int {
	trimmed := strings.TrimSuffix(strings.TrimSpace(raw), "px")
	if trimmed == "" {
		return nil
	}

	value, err := strconv.Atoi(trimmed)
	if err != nil || value <= 0 {
		return nil
	}

	return &value
}

/** looksLikeImageURL identifies obvious raster image extensions when MIME type is omitted. */
func looksLikeImageURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}

	lowerPath := strings.ToLower(parsed.Path)
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp", ".gif", ".avif"} {
		if strings.HasSuffix(lowerPath, ext) {
			return true
		}
	}

	return false
}

/** truncateCaption normalizes whitespace and caps caption length cleanly at rune boundaries. */
func truncateCaption(raw string, maxRunes int) string {
	clean := strings.Join(strings.Fields(strings.TrimSpace(raw)), " ")
	if clean == "" || maxRunes <= 0 {
		return ""
	}

	runes := []rune(clean)
	if len(runes) <= maxRunes {
		return clean
	}

	return string(runes[:maxRunes]) + "…"
}
func parseWeb(data []byte, cfg settings, base string) ([]item, error) {
	mode := cfg.text("parseMode")
	if mode != "" && mode != "html" && mode != "markdown" {
		return nil, errUnsupported
	}
	if cfg.text("baseUrl") != "" {
		base = cfg.text("baseUrl")
	}
	if mode == "markdown" {
		pattern := regexp.MustCompile("\\[([^\\]\\n]+)\\]\\((https?://[^)\\s]+|/[^)\\s]+)\\)")
		items := []item{}
		for _, match := range pattern.FindAllStringSubmatch(string(data), -1) {
			items = append(items, item{URL: resolveURL(base, match[2]), Title: match[1]})
		}
		return items, nil
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	selector := cfg.text("itemSelector")
	if selector == "" {
		selector = "a[href]"
	}
	items := []item{}
	doc.Find(selector).Each(func(_ int, node *goquery.Selection) {
		link := node
		if child := cfg.text("linkSelector"); child != "" {
			link = node.Find(child).First()
		} else if !node.Is("a[href]") {
			link = node.Find("a[href]").First()
		}
		raw, ok := link.Attr("href")
		if !ok {
			return
		}
		title := link.Text()
		if selector := cfg.text("titleSelector"); selector != "" {
			title = node.Find(selector).First().Text()
		}
		if strings.TrimSpace(title) == "" {
			title, _ = link.Attr("title")
		}
		date := ""
		if selector := cfg.text("publishedAtSelector"); selector != "" {
			dateNode := node.Find(selector).First()
			date, _ = dateNode.Attr("datetime")
			if date == "" {
				date = dateNode.Text()
			}
		}
		if pattern := cfg.text("publishedAtRegex"); pattern != "" {
			if re, e := regexp.Compile(pattern); e == nil {
				if match := re.FindStringSubmatch(node.Text()); len(match) > 1 {
					date = match[1]
				}
			}
		}
		excerpt := node.Find(".summary,.description,p").First().Text()
		address := resolveURL(base, raw)
		if normalizeURL(address, cfg.boolean("preserveFragment")) == normalizeURL(base, cfg.boolean("preserveFragment")) {
			return
		}
		items = append(items, item{URL: address, Title: title, Excerpt: excerpt, Published: parseDate(date, cfg)})
	})
	return items, nil
}
func enrichDetail(candidate *item, data []byte, cfg settings) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(data))
	if err != nil {
		return
	}
	doc.Find("script,style,noscript").Remove()
	if selector := cfg.text("summarySelector"); selector != "" {
		candidate.Body = doc.Find(selector).Text()
	}
	if selector := cfg.text("titleSelector"); selector != "" {
		if title := strings.TrimSpace(doc.Find(selector).First().Text()); title != "" {
			candidate.Title = title
		}
	}
	if selector := cfg.text("publishedAtSelector"); selector != "" {
		node := doc.Find(selector).First()
		date, _ := node.Attr("datetime")
		if date == "" {
			date = node.Text()
		}
		if parsed := parseDate(date, cfg); parsed != nil {
			candidate.Published = parsed
		}
	}
}
func parseJSON(data []byte, cfg settings, base string) ([]item, error) {
	var root any
	decode := func(raw string) (any, error) {
		var value any
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		err := decoder.Decode(&value)
		return value, err
	}
	mode := cfg.text("mode")
	if mode == "html_json_key" {
		doc, err := goquery.NewDocumentFromReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		doc.Find("script").EachWithBreak(func(_ int, node *goquery.Selection) bool {
			value, e := decode(node.Text())
			if e != nil {
				return true
			}
			root = findKey(value, cfg.text("jsonKey"))
			return root == nil
		})
		if root == nil {
			return nil, errUnsupported
		}
	} else if mode == "html_window_var" {
		name := cfg.text("windowVar")
		index := strings.Index(string(data), name)
		if name == "" || index < 0 {
			return nil, errUnsupported
		}
		rest := string(data)[index+len(name):]
		equal := strings.Index(rest, "=")
		if equal < 0 {
			return nil, errUnsupported
		}
		var err error
		root, err = decode(strings.TrimSpace(rest[equal+1:]))
		if err != nil {
			return nil, err
		}
	} else if mode != "" && mode != "json" {
		return nil, errUnsupported
	} else {
		var err error
		root, err = decode(string(data))
		if err != nil {
			return nil, err
		}
	}
	list := atPath(root, cfg.text("itemsPath"))
	rows, ok := list.([]any)
	if !ok && cfg.boolean("itemsObjectValues") {
		if object, yes := list.(map[string]any); yes {
			for _, row := range object {
				rows = append(rows, row)
			}
			ok = true
		}
	}
	if !ok {
		return nil, errUnsupported
	}
	items := []item{}
	for _, row := range rows {
		if requirement := cfg.object("requireBoolean"); len(requirement) > 0 {
			value, exists := atPath(row, requirement.text("path")).(bool)
			expected, _ := requirement["equals"].(bool)
			if !exists || value != expected {
				continue
			}
		}
		if requirement := cfg.object("minNumeric"); len(requirement) > 0 {
			value, e := strconv.ParseFloat(scalar(atPath(row, requirement.text("path"))), 64)
			min, _ := requirement["min"].(float64)
			if e != nil || value < min {
				continue
			}
		}
		title := firstPath(row, cfg.list("titlePaths"))
		if title == "" {
			title = firstPath(row, []string{"title", "name"})
		}
		summary := firstPath(row, cfg.list("summaryPaths"))
		if summary == "" {
			summary = firstPath(row, []string{"summary", "description"})
		}
		author := firstPath(row, cfg.list("authorPaths"))
		address := renderTemplate(cfg.text("urlTemplate"), row)
		if address == "" {
			address = firstPath(row, cfg.list("urlPaths"))
		}
		if address == "" {
			address = firstPath(row, []string{"url", "link", "html_url"})
		}
		if address == "" {
			address = renderTemplate(cfg.text("urlTemplateFallback"), row)
		}
		rawDate := scalar(atPath(row, cfg.text("publishedAtPath")))
		var published *time.Time
		switch cfg.text("publishedAtUnit") {
		case "epoch_ms", "epoch_s":
			n, e := strconv.ParseInt(rawDate, 10, 64)
			if e == nil {
				t := time.Unix(n, 0)
				if cfg.text("publishedAtUnit") == "epoch_ms" {
					t = time.UnixMilli(n)
				}
				t = t.UTC()
				published = &t
			}
		default:
			published = parseDate(rawDate, cfg)
		}
		body := ""
		if cfg.boolean("summaryIsBody") {
			body = summary
		}
		items = append(items, item{URL: resolveURL(base, address), Title: title, Excerpt: summary, Body: body, Author: author, Published: published})
	}
	return items, nil
}
func findKey(value any, key string) any {
	switch x := value.(type) {
	case map[string]any:
		if found, ok := x[key]; ok {
			return found
		}
		for _, child := range x {
			if found := findKey(child, key); found != nil {
				return found
			}
		}
	case []any:
		for _, child := range x {
			if found := findKey(child, key); found != nil {
				return found
			}
		}
	}
	return nil
}

var templatePattern = regexp.MustCompile("\\{(raw:)?([^{}]+)\\}")

func renderTemplate(template string, row any) string {
	missing := false
	rendered := templatePattern.ReplaceAllStringFunc(template, func(placeholder string) string {
		match := templatePattern.FindStringSubmatch(placeholder)
		value := scalar(atPath(row, match[2]))
		if value == "" {
			missing = true
		}
		if match[1] != "" {
			return value
		}
		return strings.ReplaceAll(url.PathEscape(value), "%2F", "/")
	})
	if missing {
		return ""
	}
	return rendered
}
