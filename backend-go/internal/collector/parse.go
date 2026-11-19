package collector

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"html"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"aihot/backend-go/internal/bodyhtml"
	"aihot/backend-go/internal/mediaurl"
	"github.com/PuerkitoBio/goquery"
	"github.com/mmcdole/gofeed"
	ext "github.com/mmcdole/gofeed/extensions"
	xhtml "golang.org/x/net/html"
)

const (
	maxRSSMediaItems = 9
	maxAltRunes      = 120
)

/** parseRSS parses RSS, Atom, or Sitemap XML feeds into normalized items with non-downloading image metadata. */
func parseRSS(data []byte, base string) ([]item, error) {
	items, _, err := parseRSSWithTitle(data, base)
	return items, err
}

/** parseRSSWithTitle parses a feed and retains its channel title. */
func parseRSSWithTitle(data []byte, base string) ([]item, string, error) {
	return parseRSSWithSettings(data, base, settings{}, "editorial")
}

/** parseRSSWithSettings parses feed entries with source body and participation settings. */
func parseRSSWithSettings(data []byte, base string, cfg settings, mode string) ([]item, string, error) {
	// Detect and parse Sitemap XML when applicable
	if isSitemapXML(data) {
		items, err := parseSitemap(data, base)
		return items, "", err
	}

	feed, err := gofeed.NewParser().Parse(bytes.NewReader(data))
	if err != nil {
		return nil, "", err
	}
	atomURLs, isAtom, err := parseAtomEntryURLs(data, base)
	if err != nil {
		return nil, "", err
	}

	items := []item{}
	for index, entry := range feed.Items {
		if isAtom && index < len(atomURLs) {
			entry.Link = atomURLs[index]
		}
		if isAtom && strings.TrimSpace(entry.Link) == "" {
			continue
		}
		guid := strings.TrimSpace(entry.GUID)
		link := strings.TrimSpace(entry.Link)
		opaqueGUID := ""
		mediaFile := false
		if link == "" && isHTTPAddress(guid) {
			link = guid
		} else if link == "" && guid != "" {
			for _, enclosure := range entry.Enclosures {
				if enclosure == nil || !(strings.HasPrefix(strings.ToLower(enclosure.Type), "audio/") || strings.HasPrefix(strings.ToLower(enclosure.Type), "video/")) {
					continue
				}
				link = strings.TrimSpace(enclosure.URL)
				if link != "" {
					opaqueGUID = guid
					mediaFile = true
					break
				}
			}
			if link == "" {
				link = guid
				opaqueGUID = guid
			}
		}
		if link == "" {
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

		resolvedItemURL := link
		if opaqueGUID == "" || mediaFile || isHTTPAddress(link) {
			resolvedItemURL = resolveURL(base, link)
		}
		mediaJSON := extractRSSMedia(entry, resolvedItemURL, base)
		videoPage := isVideoPageAddress(resolvedItemURL)
		bodyHTML, bodyText := rssBody(entry, cfg, mode, videoPage, resolvedItemURL)
		excerpt := plain(entry.Description)
		if videoDescription := rssMediaDescription(entry); videoPage && videoDescription != "" {
			excerpt = videoDescription
		}
		if excerpt == "" && rssTeaser(bodyText, mode) {
			excerpt = bodyText
		}
		var rawGUID any
		if guid != "" {
			rawGUID = guid
		}
		raw, _ := json.Marshal(map[string]any{"guid": rawGUID})

		items = append(items, item{
			URL:        resolvedItemURL,
			OpaqueGUID: opaqueGUID,
			Title:      entry.Title,
			Excerpt:    excerpt,
			Body:       bodyHTML,
			BodyHTML:   bodyHTML,
			BodyStatus: rssBodyStatus(bodyHTML, bodyText, cfg, mode, !mediaFile && !videoPage),
			Author:     author,
			Media:      mediaJSON,
			Raw:        raw,
			Published:  published,
			Categories: entry.Categories,
		})
	}

	return items, strings.TrimSpace(feed.Title), nil
}

/** rssBody chooses and sanitizes feed content according to summary-as-body and teaser rules. */
func rssBody(entry *gofeed.Item, cfg settings, mode string, videoPage bool, baseURL string) (string, string) {
	if entry == nil {
		return "", ""
	}
	raw := strings.TrimSpace(entry.Content)
	if raw == "" && cfg.boolean("summaryIsBody") && !videoPage {
		raw = strings.TrimSpace(entry.Description)
	}
	if raw == "" {
		return "", ""
	}
	if !strings.Contains(raw, "<") {
		raw = "<p>" + html.EscapeString(raw) + "</p>"
	}
	safe, _, err := bodyhtml.Sanitize(raw, baseURL, "", time.Now().UTC())
	if err != nil {
		return "", plain(raw)
	}
	bodyText := plain(safe)
	teaser := rssTeaser(bodyText, mode)
	if len(utf16.Encode([]rune(bodyText))) <= 280 && !cfg.boolean("summaryIsBody") || teaser {
		return "", bodyText
	}
	return safe, bodyText
}

/** rssMediaDescription reads the Media RSS description from a direct tag or its group. */
func rssMediaDescription(entry *gofeed.Item) string {
	if entry == nil {
		return ""
	}
	media := entry.Extensions["media"]
	for _, group := range media["group"] {
		for _, content := range group.Children["content"] {
			if description := firstExtensionText(content.Children["description"]); description != "" {
				return description
			}
		}
	}
	for _, content := range media["content"] {
		if description := firstExtensionText(content.Children["description"]); description != "" {
			return description
		}
	}
	if description := firstExtensionText(media["description"]); description != "" {
		return description
	}
	for _, group := range media["group"] {
		if description := firstExtensionText(group.Children["description"]); description != "" {
			return description
		}
	}
	return ""
}

/** firstExtensionText reads extension text while stripping markup only for HTML-typed values. */
func firstExtensionText(values []ext.Extension) string {
	for _, value := range values {
		text := strings.TrimSpace(value.Value)
		if text == "" {
			continue
		}
		if strings.EqualFold(value.Attrs["type"], "html") {
			return plain(text)
		}
		return strings.Join(strings.Fields(text), " ")
	}
	return ""
}

/** isVideoPageAddress recognizes video watch and player URLs that do not have article bodies. */
func isVideoPageAddress(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	path := strings.TrimSuffix(parsed.Path, "/")
	switch host {
	case "youtube.com", "www.youtube.com", "m.youtube.com":
		return path == "/watch" && parsed.Query().Get("v") != "" || regexp.MustCompile(`^/(?:shorts|embed|v)/[^/]+$`).MatchString(path)
	case "youtu.be":
		return regexp.MustCompile(`^/[^/]+$`).MatchString(path)
	case "vimeo.com", "www.vimeo.com":
		return regexp.MustCompile(`^/\d+(?:/[a-zA-Z0-9]+)?$`).MatchString(path)
	case "player.vimeo.com":
		return regexp.MustCompile(`^/video/\d+$`).MatchString(path)
	default:
		return false
	}
}

/** rssBodyStatus marks full feed text as ready and teaser or absent text for later extraction. */
func rssBodyStatus(bodyHTML, bodyText string, cfg settings, mode string, hasPage bool) string {
	if bodyHTML != "" && len(utf16.Encode([]rune(bodyText))) > 0 && (!rssTeaser(bodyText, mode)) && (len(utf16.Encode([]rune(bodyText))) > 280 || cfg.boolean("summaryIsBody")) {
		return "ok"
	}
	if hasPage {
		return "pending"
	}
	return "none"
}

/** rssTeaser identifies short feed content that explicitly directs readers to the full story. */
func rssTeaser(text, mode string) bool {
	if mode != "editorial" || len(utf16.Encode([]rune(strings.TrimSpace(text)))) >= 1200 {
		return false
	}
	return regexp.MustCompile(`(?i)(appeared first on|read (the )?full (story|article)|continue reading|read more|…\s*$|\[\s*(…|\.\.\.)\s*\]\s*$)`).MatchString(strings.TrimSpace(text))
}

/** isHTTPAddress recognizes an address GUID that can serve as an article page. */
func isHTTPAddress(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != "" && parsed.User == nil
}

type atomLinkValue struct {
	href string
	rel  string
	base *url.URL
}

type atomEntryLinks struct {
	links []atomLinkValue
}

type atomXMLFrame struct {
	base  *url.URL
	entry bool
}

/** parseAtomEntryURLs resolves links with the redirect URL and each inherited XML base. */
func parseAtomEntryURLs(data []byte, documentBase string) ([]string, bool, error) {
	documentURL, err := url.Parse(documentBase)
	if err != nil {
		return nil, false, err
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	frames := make([]atomXMLFrame, 0, 8)
	entryURLs := make([]string, 0)
	var currentEntry *atomEntryLinks
	atomFeed := false
	rootSeen := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false, err
		}
		switch value := token.(type) {
		case xml.StartElement:
			if !rootSeen {
				rootSeen = true
				atomFeed = value.Name.Local == "feed" && isAtomNamespace(value.Name.Space)
				if !atomFeed {
					return nil, false, nil
				}
			}
			parentBase := documentURL
			if len(frames) > 0 {
				parentBase = frames[len(frames)-1].base
			}
			elementBase := parentBase
			for _, attribute := range value.Attr {
				if attribute.Name.Space != "http://www.w3.org/XML/1998/namespace" || attribute.Name.Local != "base" {
					continue
				}
				reference, err := url.Parse(attribute.Value)
				if err != nil {
					return nil, false, err
				}
				elementBase = parentBase.ResolveReference(reference)
			}
			entry := false
			if value.Name.Local == "entry" && isAtomNamespace(value.Name.Space) {
				currentEntry = &atomEntryLinks{}
				entry = true
			}
			if value.Name.Local == "link" && isAtomNamespace(value.Name.Space) && currentEntry != nil {
				link := atomLinkValue{base: elementBase}
				for _, attribute := range value.Attr {
					if attribute.Name.Space != "" {
						continue
					}
					switch attribute.Name.Local {
					case "href":
						link.href = attribute.Value
					case "rel":
						link.rel = attribute.Value
					}
				}
				currentEntry.links = append(currentEntry.links, link)
			}
			frames = append(frames, atomXMLFrame{base: elementBase, entry: entry})
		case xml.EndElement:
			if len(frames) == 0 {
				continue
			}
			frame := frames[len(frames)-1]
			frames = frames[:len(frames)-1]
			if frame.entry {
				entryURLs = append(entryURLs, selectAtomEntryURL(currentEntry))
				currentEntry = nil
			}
		}
	}
	return entryURLs, atomFeed, nil
}

/** isAtomNamespace recognizes both published Atom XML namespace versions. */
func isAtomNamespace(namespace string) bool {
	return namespace == "http://www.w3.org/2005/Atom" || namespace == "http://purl.org/atom/ns#"
}

/** selectAtomEntryURL chooses the preferred Atom link and resolves its own base. */
func selectAtomEntryURL(entry *atomEntryLinks) string {
	if entry == nil {
		return ""
	}
	var selected *atomLinkValue
	for index := range entry.links {
		link := &entry.links[index]
		if link.href == "" {
			continue
		}
		if link.rel == "" || link.rel == "alternate" {
			selected = link
			break
		}
		if selected == nil {
			selected = link
		}
	}
	if selected == nil {
		return ""
	}
	href, err := url.Parse(strings.TrimSpace(selected.href))
	if err != nil {
		return ""
	}
	if selected.base == nil {
		return href.String()
	}
	return selected.base.ResolveReference(href).String()
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
	if mode == "intercom_changelog" {
		return parseIntercomChangelog(data, cfg, base)
	}
	if mode == "docusaurus_changelog" {
		return parseDocusaurusChangelog(data, cfg, base)
	}
	if mode != "" && mode != "html" && mode != "markdown" {
		return nil, errUnsupported
	}
	if cfg.text("baseUrl") != "" {
		base = cfg.text("baseUrl")
	}
	if mode == "markdown" {
		return parseMarkdownList(data, cfg, base), nil
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	selector := cfg.text("itemSelector")
	if selector == "" {
		selector = "a[href]"
	}
	listing := listingAddress(cfg, base)
	items := []item{}
	seen := make(map[string]struct{})
	doc.Find(selector).Each(func(_ int, node *goquery.Selection) {
		link := node
		if child := cfg.text("linkSelector"); child != "" {
			if node.Is(child) {
				link = node
			} else {
				link = node.Find(child).First()
			}
		} else if !node.Is("a[href]") {
			link = node.Find("a[href]").First()
		}
		raw, ok := link.Attr("href")
		if !ok {
			return
		}
		address := absoluteSourceURL(raw, base)
		preserveFragment := cfg.boolean("preserveFragment") || cfg.boolean("preserveUrlFragment")
		if address == "" || !preserveFragment && listingItself(address, listing) {
			return
		}
		if _, exists := seen[address]; exists {
			return
		}
		title := link.Text()
		if selector := cfg.text("titleSelector"); selector != "" {
			selected := node.Find(selector).First()
			if node.Is(selector) {
				selected = node
			}
			title = selected.Text()
		}
		if strings.TrimSpace(title) == "" {
			title, _ = link.Attr("title")
		}
		var published *time.Time
		if selector := cfg.text("publishedAtSelector"); selector != "" {
			published = selectedDate(node.Find(selector).First(), cfg)
		}
		if published == nil {
			if pattern := cfg.text("publishedAtRegex"); pattern != "" {
				if re, e := regexp.Compile(pattern); e == nil {
					if match := re.FindStringSubmatch(node.Text()); len(match) > 1 {
						published = parseDate(match[1], cfg)
					}
				}
			}
		}
		excerpt := node.Find(".summary,.description,p").First().Text()
		seen[address] = struct{}{}
		items = append(items, item{URL: address, Title: strings.Join(strings.Fields(title), " "), Excerpt: strings.Join(strings.Fields(excerpt), " "), Published: published})
	})
	return items, nil
}

var (
	markdownListingLink = regexp.MustCompile(`\[([^\]\n]{1,1000})\]\((https?://[^)\s]+|/[^)\s]*)(?:\s+"([^"]*)")?\)`)
	markdownImage       = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	markdownLinePrefix  = regexp.MustCompile(`^[\s>#*+_|-]*(?:\d+[.)]\s*)?[\s*_]*$`)
	listingParam        = regexp.MustCompile(`(?i)^(page|paged|cat|category|categories|tag|tags|label|labels|author|authors)$`)
	listingNavigation   = regexp.MustCompile(`(?i)/(label|labels|tag|tags|category|categories|author|authors|page)(/|$)`)
	listingYearArchive  = regexp.MustCompile(`/((19|20)\d{2})(/\d{1,2})?/?$`)
	mimoRouteTable      = regexp.MustCompile(`\{path:"/",[^{}]*\}`)
	mimoRouteChunk      = regexp.MustCompile(`\.e\("([^"]+)"\)`)
	mimoChunkMap        = regexp.MustCompile(`"(static/js/async/)"\+\w+\+"\."\+\(?\{([^}]*)\}\)?\[\w+\]\+"\.js"`)
	mimoPublicPath      = regexp.MustCompile(`\b\w+\.p="([^"]*)"`)
	mimoChunkName       = regexp.MustCompile(`"?(\w+)"?:"(\w+)"`)
	mimoSectionTitle    = regexp.MustCompile(`sectionTitle:"Blog"`)
	mimoSectionMarker   = regexp.MustCompile(`sectionTitle:`)
	mimoObject          = regexp.MustCompile(`\{(?:[^{}"]|"(?:[^"\\]|\\.)*")*\}`)
	mimoStringLiteral   = regexp.MustCompile(`\b(title|link|desc):("(?:[^"\\]|\\.)*")`)
	mimoHexEscape       = regexp.MustCompile(`\\x([0-9a-fA-F]{2})`)
)

/** listingAddress returns the publisher URL behind a direct or Jina-rendered list. */
func listingAddress(cfg settings, fallback string) string {
	listing := cfg.text("url")
	if listing == "" {
		listing = fallback
	}
	if target, ok := jinaTargetURL(listing); ok {
		return target
	}
	return listing
}

/** parseMarkdownList extracts headline links while excluding card images, navigation, and teaser prose. */
func parseMarkdownList(data []byte, cfg settings, base string) []item {
	text := markdownImage.ReplaceAllString(string(data), "")
	listing := listingAddress(cfg, base)
	seen := make(map[string]int)
	items := make([]item, 0)
	for _, match := range markdownListingLink.FindAllStringSubmatchIndex(text, -1) {
		label := text[match[2]:match[3]]
		address := absoluteSourceURL(text[match[4]:match[5]], base)
		if address == "" || !urlAllowed(address, cfg) || listingNavigationLink(address, listing) {
			continue
		}
		if cfg.boolean("linksStartLine") && !markdownLinkStartsLine(text, match[0]) {
			continue
		}
		title := strings.NewReplacer("*", "", "_", "", "`", "", "#", "").Replace(label)
		title = strings.Join(strings.Fields(title), " ")
		attribute := ""
		if match[6] >= 0 {
			attribute = strings.Join(strings.Fields(text[match[6]:match[7]]), " ")
		}
		if len(attribute) >= 6 && strings.Contains(title, attribute) {
			title = attribute
		}
		if len([]rune(title)) < 6 {
			continue
		}
		if index, exists := seen[address]; exists {
			if isCallToActionTitle(items[index].Title) && !isCallToActionTitle(title) {
				items[index].Title = title
			}
			continue
		}
		seen[address] = len(items)
		items = append(items, item{URL: address, Title: title})
	}
	return items
}

/** markdownLinkStartsLine accepts headline links after common Markdown list and quote markers. */
func markdownLinkStartsLine(text string, index int) bool {
	lineStart := strings.LastIndex(text[:index], "\n") + 1
	prefix := text[lineStart:index]
	prefix = regexp.MustCompile(`\[\]\([^)]*\)`).ReplaceAllString(prefix, "")
	return markdownLinePrefix.MatchString(prefix)
}

/** absoluteSourceURL resolves a listing link and keeps same-host links on the listing's HTTPS scheme. */
func absoluteSourceURL(raw, base string) string {
	address := resolveURL(base, raw)
	parsed, err := url.Parse(address)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return ""
	}
	baseURL, err := url.Parse(base)
	if err == nil && parsed.Scheme == "http" && baseURL.Scheme == "https" && strings.EqualFold(parsed.Host, baseURL.Host) {
		parsed.Scheme = "https"
	}
	return parsed.String()
}

/** listingItself compares a link after removing listing filters and tracking parameters. */
func listingItself(address, listing string) bool {
	return listingURLKey(address, false) == listingURLKey(listing, false)
}

/** listingNavigationLink excludes the listing, its filters, and archive or taxonomy pages. */
func listingNavigationLink(address, listing string) bool {
	if listingItself(address, listing) {
		return true
	}
	parsed, err := url.Parse(address)
	if err != nil {
		return false
	}
	return listingNavigation.MatchString(parsed.Path) || listingYearArchive.MatchString(parsed.Path)
}

/** listingURLKey normalizes a listing address after removing pagination and taxonomy filters. */
func listingURLKey(raw string, preserveFragment bool) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if !preserveFragment {
		parsed.Fragment = ""
		parsed.RawFragment = ""
	}
	query := parsed.Query()
	for key := range query {
		if listingParam.MatchString(key) {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()
	return normalizeURL(parsed.String(), preserveFragment)
}

/** parseMimoHome fetches the homepage's route chunk and extracts its Blog records. */
func parseMimoHome(data []byte, base string, cfg settings, fetchScript func(string) ([]byte, error)) ([]item, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	scriptURLs := make([]string, 0)
	doc.Find("script[src]").Each(func(_ int, node *goquery.Selection) {
		if address := absoluteSourceURL(node.AttrOr("src", ""), base); address != "" {
			scriptURLs = append(scriptURLs, address)
		}
	})
	routeChunks := make([]string, 0)
	chunkHashes := make(map[string]string)
	chunkDirectory := ""
	for index := len(scriptURLs) - 1; index >= 0; index-- {
		if len(routeChunks) > 0 && chunkDirectory != "" && len(chunkHashes) > 0 {
			break
		}
		scriptURL := scriptURLs[index]
		script, fetchErr := fetchScript(scriptURL)
		if fetchErr != nil {
			return nil, fetchErr
		}
		if route := mimoRouteTable.Find(script); route != nil {
			for _, match := range mimoRouteChunk.FindAllSubmatch(route, -1) {
				routeChunks = append(routeChunks, string(match[1]))
			}
		}
		chunkMatch := mimoChunkMap.FindSubmatch(script)
		publicMatch := mimoPublicPath.FindSubmatch(script)
		if len(chunkMatch) > 2 && len(publicMatch) > 1 {
			chunkDirectory = string(chunkMatch[1])
			root := resolveURL(scriptURL, string(publicMatch[1]))
			chunkDirectory = resolveURL(root, chunkDirectory)
			for _, match := range mimoChunkName.FindAllSubmatch(chunkMatch[2], -1) {
				chunkHashes[string(match[1])] = string(match[2])
			}
		}
	}
	if len(routeChunks) == 0 || len(chunkHashes) == 0 {
		return nil, errors.New("mimo_home: no route table or chunk map in the homepage scripts")
	}
	listing := listingAddress(cfg, base)
	for index := len(routeChunks) - 1; index >= 0; index-- {
		chunkID := routeChunks[index]
		hash, exists := chunkHashes[chunkID]
		if !exists {
			continue
		}
		chunkURL := resolveURL(chunkDirectory, chunkID+"."+hash+".js")
		chunk, fetchErr := fetchScript(chunkURL)
		if fetchErr != nil {
			return nil, fetchErr
		}
		blogAt := mimoSectionTitle.FindIndex(chunk)
		if blogAt == nil {
			continue
		}
		section := chunk[blogAt[0]:]
		if next := mimoSectionMarker.FindIndex(section[1:]); next != nil {
			section = section[:next[0]+1]
		}
		items := make([]item, 0)
		for _, row := range mimoObject.FindAll(section, -1) {
			fields := make(map[string]string)
			for _, match := range mimoStringLiteral.FindAllSubmatch(row, -1) {
				fields[string(match[1])] = decodeJavaScriptString(string(match[2]))
			}
			address := absoluteSourceURL(fields["link"], base)
			title := strings.Join(strings.Fields(fields["title"]), " ")
			if address == "" || title == "" || !urlAllowed(address, cfg) || listingNavigationLink(address, listing) {
				continue
			}
			duplicate := false
			for _, existing := range items {
				if existing.URL == address {
					duplicate = true
					break
				}
			}
			if duplicate {
				continue
			}
			excerpt := strings.Join(strings.Fields(fields["desc"]), " ")
			if excerpt == title {
				excerpt = ""
			}
			items = append(items, item{URL: address, Title: title, Excerpt: excerpt})
		}
		if len(items) == 0 {
			return nil, errors.New("mimo_home: no Blog list in the homepage's chunks")
		}
		return items, nil
	}
	return nil, errors.New("mimo_home: no Blog list in the homepage's chunks")
}

/** decodeJavaScriptString decodes a quoted JavaScript field using JSON-compatible escapes. */
func decodeJavaScriptString(literal string) string {
	compatible := mimoHexEscape.ReplaceAllString(literal, `\u00$1`)
	compatible = strings.ReplaceAll(compatible, `\'`, "'")
	var decoded string
	if err := json.Unmarshal([]byte(compatible), &decoded); err != nil {
		return strings.Trim(compatible, `"`)
	}
	return decoded
}

/** parseIntercomChangelog turns dated Intercom sibling blocks into stable section materials. */
func parseIntercomChangelog(data []byte, cfg settings, base string) ([]item, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	dateConfig := cfg
	if dateConfig.text("publishedAtUtcOffset") == "" {
		dateConfig["publishedAtUtcOffset"] = "+00:00"
	}
	items := make([]item, 0)
	doc.Find("article .intercom-interblocks-subheading3:has(h3[id]), article .intercom-interblocks-subheading:has(h2[id])").Each(func(_ int, block *goquery.Selection) {
		heading := block.Find("h2[id], h3[id]").First()
		date := parseDate(heading.Text(), dateConfig)
		if date == nil {
			return
		}
		parts := make([]string, 0)
		titles := make([]string, 0)
		next := block.Next()
		for next.Length() > 0 && next.Find("h2, h3").Length() == 0 {
			if outer, outerErr := selectionOuterHTML(next); outerErr == nil {
				parts = append(parts, outer)
			}
			paragraph := next.Find("p").First()
			children := paragraph.Children()
			if paragraph.Length() > 0 && children.Length() == 1 {
				child := children.First()
				childTag := goquery.NodeName(child)
				text := strings.Join(strings.Fields(paragraph.Text()), " ")
				if (childTag == "b" || childTag == "strong") && text == strings.Join(strings.Fields(child.Text()), " ") && text != "" {
					titles = append(titles, text)
				}
			}
			next = next.Next()
		}
		bodyHTML, bodyText := sanitizedSectionBody(strings.Join(parts, ""), base)
		if strings.TrimSpace(bodyText) == "" {
			return
		}
		address, identity, ok := sectionAddress(base, heading.AttrOr("id", ""))
		if !ok || heading.AttrOr("id", "") == "" {
			return
		}
		title := strings.Join(titles, " · ")
		if title == "" {
			title = strings.Join(strings.Fields(heading.Text()), " ")
		}
		items = append(items, item{URL: address, IdentityKey: identity, Title: title, Published: date, Body: bodyText, BodyHTML: bodyHTML, BodyStatus: "ok"})
	})
	return items, nil
}

/** parseDocusaurusChangelog maps dated section headings and their following content to materials. */
func parseDocusaurusChangelog(data []byte, cfg settings, base string) ([]item, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	items := make([]item, 0)
	var sectionDate *time.Time
	doc.Find("article h2[id], article h3[id], .markdown h2[id], .markdown h3[id]").Each(func(_ int, heading *goquery.Selection) {
		id := heading.AttrOr("id", "")
		title := strings.Join(strings.Fields(strings.TrimSuffix(strings.ReplaceAll(heading.Text(), "\u200b", ""), "#")), " ")
		if date := changelogHeadingDate(title, cfg); date.matched {
			sectionDate = date.value
			return
		}
		if goquery.NodeName(heading) == "h2" {
			sectionDate = nil
		}
		parts := make([]string, 0)
		next := heading.Next()
		for next.Length() > 0 && !next.Is("h2, h3") {
			if outer, outerErr := selectionOuterHTML(next); outerErr == nil {
				parts = append(parts, outer)
			}
			next = next.Next()
		}
		bodyHTML, bodyText := sanitizedSectionBody(strings.Join(parts, ""), base)
		address, identity, ok := sectionAddress(base, id)
		if !ok || id == "" {
			return
		}
		published := parseDate(title, cfg)
		if published == nil {
			published = sectionDate
		}
		if published == nil && len(bodyText) > 0 {
			published = parseDate(truncate(bodyText, 80), cfg)
		}
		items = append(items, item{URL: address, IdentityKey: identity, Title: title, Published: published, Body: bodyText, BodyHTML: bodyHTML, BodyStatus: "ok"})
	})
	return items, nil
}

type changelogDate struct {
	value   *time.Time
	matched bool
}

/** changelogHeadingDate distinguishes a date-only heading from a dated update title. */
func changelogHeadingDate(title string, cfg settings) changelogDate {
	match := regexp.MustCompile(`^(?:[^\d:：]{1,12}[:：])?\s*(\d{4})[-/.年](\d{1,2})[-/.月](\d{1,2})日?$`).FindStringSubmatch(title)
	if len(match) == 0 {
		return changelogDate{}
	}
	date := parseDate(match[1]+"/"+match[2]+"/"+match[3], cfg)
	return changelogDate{value: date, matched: true}
}

/** selectionOuterHTML serializes a selected sibling including its outer element. */
func selectionOuterHTML(selection *goquery.Selection) (string, error) {
	if selection == nil || len(selection.Nodes) == 0 {
		return "", nil
	}
	var output bytes.Buffer
	if err := xhtml.Render(&output, selection.Nodes[0]); err != nil {
		return "", err
	}
	return output.String(), nil
}

/** sanitizedSectionBody sanitizes section markup and derives its plain text representation. */
func sanitizedSectionBody(raw, base string) (string, string) {
	safe, _, err := bodyhtml.Sanitize(raw, base, "", time.Now().UTC())
	if err != nil {
		return "", plain(raw)
	}
	return safe, plain(safe)
}

/** sectionAddress builds the canonical section URL and its persistent material identity. */
func sectionAddress(base, fragment string) (string, string, bool) {
	parsed, err := url.Parse(base)
	if err != nil || fragment == "" {
		return "", "", false
	}
	parsed.Fragment = fragment
	address := parsed.String()
	return address, "url:" + normalizeURL(address, true), true
}

/** enrichDetail applies the source's configured selectors to a successful article-page response. */
func enrichDetail(candidate *item, data []byte, cfg settings) {
	enrichDetailFields(candidate, data, cfg, detailReadNeed{title: true, date: true, summary: true})
}

/** enrichDetailFields applies only the requested selectors or regexes to an HTML detail page. */
func enrichDetailFields(candidate *item, data []byte, cfg settings, needs detailReadNeed) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(data))
	if err != nil {
		return
	}
	doc.Find("script,style,noscript").Remove()
	if needs.summary {
		if selector := cfg.text("summarySelector"); selector != "" {
			candidate.Excerpt = doc.Find(selector).Text()
		}
	}
	if needs.title {
		if selector := cfg.text("titleSelector"); selector != "" {
			if title := strings.TrimSpace(doc.Find(selector).First().Text()); title != "" {
				candidate.Title = title
			}
		} else if pattern := cfg.text("titleRegex"); pattern != "" {
			if re, compileErr := regexp.Compile(pattern); compileErr == nil {
				if match := re.FindStringSubmatch(string(data)); len(match) > 1 && strings.TrimSpace(match[1]) != "" {
					candidate.Title = strings.TrimSpace(match[1])
				}
			}
		}
	}
	if needs.date {
		var published *time.Time
		if selector := cfg.text("publishedAtSelector"); selector != "" {
			published = selectedDate(doc.Find(selector).First(), cfg)
		}
		if published == nil {
			if pattern := cfg.text("publishedAtRegex"); pattern != "" {
				if re, compileErr := regexp.Compile(pattern); compileErr == nil {
					if match := re.FindStringSubmatch(string(data)); len(match) > 1 {
						published = parseDate(match[1], cfg)
					}
				}
			}
		}
		authoritative := cfg.boolean("publishedAtAuthoritative") && (cfg.text("publishedAtSelector") != "" || cfg.text("publishedAtRegex") != "")
		if published == nil && !authoritative {
			published = pagePublishedDate(doc, data, cfg)
		}
		if published != nil {
			candidate.Published = published
		}
	}
}

/** selectedDate tries a selector's publication attributes and visible text until one parses. */
func selectedDate(node *goquery.Selection, cfg settings) *time.Time {
	for _, attribute := range []string{"datetime", "content", "title"} {
		if value, exists := node.Attr(attribute); exists {
			if parsed := parseDate(value, cfg); parsed != nil {
				return parsed
			}
		}
	}
	return parseDate(node.Text(), cfg)
}

/** pagePublishedDate reads page publication metadata, structured data, then its first time element. */
func pagePublishedDate(doc *goquery.Document, data []byte, cfg settings) *time.Time {
	for _, selector := range []string{`meta[property="article:published_time"]`, `meta[name="pubdate"]`, `meta[itemprop="datePublished"]`} {
		if parsed := selectedDate(doc.Find(selector).First(), cfg); parsed != nil {
			return parsed
		}
	}
	var structuredDate *time.Time
	doc.Find(`script[type="application/ld+json"]`).EachWithBreak(func(_ int, node *goquery.Selection) bool {
		var decoded any
		if err := json.Unmarshal([]byte(node.Text()), &decoded); err != nil {
			return true
		}
		if value := jsonLDPublished(decoded, 0); value != "" {
			if parsed := parseDate(value, cfg); parsed != nil {
				structuredDate = parsed
				return false
			}
		}
		return true
	})
	if structuredDate != nil {
		return structuredDate
	}
	for _, match := range jsonLDDatePattern.FindAllSubmatch(data, -1) {
		if parsed := parseDate(string(match[1]), cfg); parsed != nil {
			return parsed
		}
	}
	return selectedDate(doc.Find("time[datetime]").First(), cfg)
}

var jsonLDDatePattern = regexp.MustCompile(`"datePublished"\s*:\s*"([^"]+)"`)

/** jsonLDPublished searches JSON-LD objects and graphs for a datePublished string. */
func jsonLDPublished(value any, depth int) string {
	if depth > 6 || value == nil {
		return ""
	}
	switch item := value.(type) {
	case []any:
		for _, child := range item {
			if found := jsonLDPublished(child, depth+1); found != "" {
				return found
			}
		}
	case map[string]any:
		if date, ok := item["datePublished"].(string); ok && date != "" {
			return date
		}
		for _, child := range item {
			if found := jsonLDPublished(child, depth+1); found != "" {
				return found
			}
		}
	}
	return ""
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
			maximum := int64(8_640_000_000_000_000)
			if cfg.text("publishedAtUnit") == "epoch_s" {
				maximum /= 1000
			}
			if e == nil && n <= maximum && n >= -maximum {
				t := time.Unix(n, 0)
				if cfg.text("publishedAtUnit") == "epoch_ms" {
					t = time.UnixMilli(n)
				}
				t = t.UTC()
				published = &t
			}
		case "yyyymmdd":
			if len(rawDate) == 8 {
				if parsed, e := time.Parse("20060102", rawDate); e == nil && parsed.Format("20060102") == rawDate {
					value := parsed.UTC()
					published = &value
				}
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
