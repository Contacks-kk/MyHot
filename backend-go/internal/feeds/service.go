package feeds

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
	"time"

	"aihot/backend-go/internal/publiccontent"
)

const feedImageLifetime = 7 * 24 * time.Hour

var (
	imgTagPattern      = regexp.MustCompile(`(?is)<img\b[^>]*>`)
	imgSourcePattern   = regexp.MustCompile(`(?i)\ssrc="([^"]+)"`)
	videoPosterPattern = regexp.MustCompile(`(?i)(<video\b[^>]*?\sposter=")([^"]+)(")`)
	bareURLPattern     = regexp.MustCompile(`https?://[^\s<>"'　-〿㐀-䶿一-鿿＀-￯぀-ヿ가-힯☀-➿️‍]+`)
)

type Category struct {
	Key       string
	Label     string
	FeedLabel string
}

type Config struct {
	SiteURL          string
	SiteName         string
	Subject          string
	Categories       []Category
	CategoryMerge    map[string]string
	FeedLabels       map[string]string
	AllLeavesOut     []string
	DailyTime        string
	WeeklyTime       string
	MonthlyTime      string
	ReportMeasure    string
	ReportNoun       string
	ImageProxySecret string
}

type ReportReader interface {
	V1FeedIssues(context.Context, string, int, time.Time, string, string, string, string) ([]publiccontent.V1FeedIssue, error)
}

type Service struct {
	repository ItemRepository
	reports    ReportReader
	config     Config
	categories map[string]Category
}

type feedMetadata struct {
	Title       string
	Description string
	HomePath    string
	SelfPath    string
	TTL         int
}

type DiscoveryFeed struct {
	Path        string
	Description string
}

type xPost struct {
	Text   string  `json:"text"`
	Quoted *xQuote `json:"quoted"`
}

type xQuote struct {
	Handle string `json:"handle"`
	Text   string `json:"text"`
	URL    string `json:"url"`
}

func NewService(repository ItemRepository, reports ReportReader, config Config) *Service {
	if config.Subject == "" {
		config.Subject = "AI"
	}
	if config.ReportMeasure == "" {
		config.ReportMeasure = "件"
	}
	if config.ReportNoun == "" {
		config.ReportNoun = "大事"
	}
	if config.DailyTime == "" {
		config.DailyTime = "08:00"
	}
	if config.WeeklyTime == "" {
		config.WeeklyTime = "10:00"
	}
	if config.MonthlyTime == "" {
		config.MonthlyTime = "10:30"
	}
	if config.CategoryMerge == nil {
		config.CategoryMerge = map[string]string{}
	}
	if config.FeedLabels == nil {
		config.FeedLabels = map[string]string{}
	}
	if config.AllLeavesOut == nil {
		config.AllLeavesOut = []string{}
	}
	categories := make(map[string]Category, len(config.Categories))
	for _, category := range config.Categories {
		categories[category.Key] = category
	}
	return &Service{repository: repository, reports: reports, config: config, categories: categories}
}

func (s *Service) DiscoveryFeeds() (map[string]DiscoveryFeed, []string) {
	result := make(map[string]DiscoveryFeed, 6)
	for _, kind := range []string{"selected", "selected-full", "all", "daily", "weekly", "monthly"} {
		meta := s.metadata(kind)
		result[kind] = DiscoveryFeed{Path: meta.SelfPath, Description: meta.Description}
	}
	categories := make([]string, 0, len(s.config.Categories))
	for _, category := range s.config.Categories {
		if s.IsCategory(category.Key) {
			categories = append(categories, category.Key)
		}
	}
	return result, categories
}

func (s *Service) IsCategory(key string) bool {
	_, exists := s.categories[key]
	return exists && s.config.CategoryMerge[key] == ""
}

func (s *Service) ItemFeed(ctx context.Context, kind, category string, now time.Time) (string, error) {
	if kind != "selected" && kind != "selected-full" && kind != "all" {
		return "", errors.New("unsupported item feed kind")
	}
	if category != "" && (!s.IsCategory(category) || kind == "all") {
		return "", errors.New("unknown feed category")
	}
	var categoryKeys []string
	if category != "" {
		for _, current := range s.config.Categories {
			if s.publicCategory(current.Key) == category {
				categoryKeys = append(categoryKeys, current.Key)
			}
		}
	}
	query := ItemQuery{Kind: kind, Now: now.UTC()}
	if category != "" {
		query.CategoryKeys = categoryKeys
	}
	rows, err := s.repository.Items(ctx, query)
	if err != nil {
		return "", err
	}
	meta, err := s.itemMetadata(kind, category)
	if err != nil {
		return "", err
	}
	items := make([]string, 0, len(rows))
	for _, row := range rows {
		value, err := s.itemXML(row, kind == "selected-full", now)
		if err != nil {
			return "", err
		}
		items = append(items, value)
	}
	return renderChannel(s.config.SiteURL, s.config.SiteName, meta, items), nil
}

func (s *Service) ReportFeed(ctx context.Context, kind string, now time.Time) (string, error) {
	if kind != "daily" && kind != "weekly" && kind != "monthly" {
		return "", errors.New("unsupported report feed kind")
	}
	if s.reports == nil {
		return "", errors.New("report feed service is unavailable")
	}
	limit := 12
	if kind == "daily" {
		limit = 30
	}
	issues, err := s.reports.V1FeedIssues(ctx, kind, limit, now.UTC(), s.config.SiteName, s.config.ReportMeasure, s.config.ReportNoun, s.config.SiteURL)
	if err != nil {
		return "", err
	}
	items := make([]string, 0, len(issues))
	for _, issue := range issues {
		items = append(items, s.issueXML(kind, issue))
	}
	return renderChannel(s.config.SiteURL, s.config.SiteName, s.metadata(kind), items), nil
}

func (s *Service) itemMetadata(kind, category string) (feedMetadata, error) {
	meta := s.metadata(kind)
	if category == "" {
		return meta, nil
	}
	label, found := s.categoryLabel(category)
	if !found {
		return feedMetadata{}, errors.New("unknown feed category")
	}
	meta.Title = s.config.SiteName + " — " + label
	meta.Description = fmt.Sprintf("%s 每日精选里「%s」这一类的摘要，按分类订阅、不被全量精选刷屏。", s.config.SiteName, label)
	if kind == "selected-full" {
		meta.Title += "全文"
		meta.Description = fmt.Sprintf("%s 每日精选「%s」分类全文源。仅对明确允许再分发的来源内联正文。", s.config.SiteName, label)
	}
	meta.HomePath = "/"
	meta.SelfPath = "/feed/category/" + category + ".xml"
	if kind == "selected-full" {
		meta.SelfPath = "/feed/full/category/" + category + ".xml"
	}
	return meta, nil
}

func (s *Service) metadata(kind string) feedMetadata {
	name := s.config.SiteName
	switch kind {
	case "selected":
		return feedMetadata{Title: name + " — 精选", Description: fmt.Sprintf("最新 50 条 %s 精选摘要，保留标题、站内阅读与原文入口；需要阅读器内全文可改订 /feed/full.xml。", name), HomePath: "/", SelfPath: "/feed.xml", TTL: 30}
	case "selected-full":
		return feedMetadata{Title: name + " — 精选全文", Description: "与精选摘要相同的最新 50 条；仅对明确允许再分发的来源内联正文，其余仍提供摘要和阅读入口。", HomePath: "/", SelfPath: "/feed/full.xml", TTL: 30}
	case "all":
		leaves := append(append([]string(nil), s.config.AllLeavesOut...), "未审内容", "低相关条目", "已合并重复条目")
		return feedMetadata{Title: name + " — " + subjectAfter("全部", "动态", s.config.Subject), Description: fmt.Sprintf("最近 7 天公开动态，按真实发布时间倒序；不含%s和%s。", strings.Join(leaves[:len(leaves)-1], "、"), leaves[len(leaves)-1]), HomePath: "/all", SelfPath: "/feed/all.xml", TTL: 30}
	case "daily":
		return feedMetadata{Title: name + " 日报", Description: fmt.Sprintf("%s 每天 %s（北京时间）发布的精编日报，保留最近 30 期。", name, s.config.DailyTime), HomePath: "/daily", SelfPath: "/feed/daily.xml", TTL: 30}
	case "weekly":
		return feedMetadata{Title: name + " 周报", Description: fmt.Sprintf("%s 每周一 %s（北京时间）发布的周报：从上周每天的日报里选出的%s，按栏目分好，附总述；保留最近 12 期。", name, s.config.WeeklyTime, s.config.ReportNoun), HomePath: "/weekly", SelfPath: "/feed/weekly.xml", TTL: 180}
	default:
		return feedMetadata{Title: name + " 月报", Description: fmt.Sprintf("%s 每月 1 日 %s（北京时间）发布的月报：从上个月每天的日报里选出的%s，按栏目分好，附总述；保留最近 12 期。", name, s.config.MonthlyTime, s.config.ReportNoun), HomePath: "/monthly", SelfPath: "/feed/monthly.xml", TTL: 360}
	}
}

func (s *Service) publicCategory(key string) string {
	if merged := s.config.CategoryMerge[key]; merged != "" {
		return merged
	}
	if _, exists := s.categories[key]; exists {
		return key
	}
	return ""
}

func (s *Service) categoryLabel(key string) (string, bool) {
	category, exists := s.categories[key]
	if !exists {
		return "", false
	}
	if label := s.config.FeedLabels[key]; label != "" {
		return label, true
	}
	if category.FeedLabel != "" {
		return category.FeedLabel, true
	}
	return category.Label, true
}

func (s *Service) itemXML(row Item, includeContent bool, now time.Time) (string, error) {
	itemURL := absoluteURL(s.config.SiteURL, "/items/"+row.ID)
	summary := ""
	if row.Summary != nil {
		summary = *row.Summary
	}
	description := fmt.Sprintf("<p>%s</p>\n<p>🔗 <a href=\"%s\">阅读原文</a></p>\n<p>via %s · <a href=\"%s\">%s</a></p>", escapeXML(summary), escapeXML(row.URL), escapeXML(s.config.SiteName), escapeXML(itemURL), escapeXML(itemURL))
	var content string
	if includeContent && row.Syndicate {
		htmlBody, err := s.fullContent(row, itemURL, now)
		if err != nil {
			return "", err
		}
		if htmlBody != "" {
			content = "\n      <content:encoded>" + cdata(htmlBody) + "</content:encoded>"
		}
	}
	var category string
	if row.Category != nil {
		if public := s.publicCategory(*row.Category); public != "" {
			if label, ok := s.categoryLabel(public); ok {
				category = "\n      <category>" + escapeXML(label) + "</category>"
			}
		}
	}
	var published string
	if row.PublishedAt != nil {
		published = "\n      <pubDate>" + rfc822(*row.PublishedAt) + "</pubDate>"
	}
	item := "    <item>\n      <title>" + cdata(row.Title) + "</title>\n      <link>" + escapeXML(itemURL) + "</link>\n      <description>" + cdata(description) + "</description>" + content + category + published + "\n      <guid isPermaLink=\"false\">" + escapeXML(row.ID) + "</guid>\n      <author>" + escapeXML(authorAddress(s.config.SiteURL)) + " (" + escapeXML(publicSourceName(row.SourceName)) + ")</author>\n    </item>"
	return item, nil
}

func (s *Service) fullContent(row Item, itemURL string, now time.Time) (string, error) {
	var body string
	if row.Channel == "x" && len(row.XPost) > 0 && string(row.XPost) != "null" {
		var post xPost
		if err := json.Unmarshal(row.XPost, &post); err != nil {
			return "", errors.New("invalid X post data")
		}
		if post.Text != "" {
			text := post.Text
			if row.ZHText != nil && strings.TrimSpace(*row.ZHText) != strings.TrimSpace(post.Text) {
				text = *row.ZHText
			}
			body = textToHTML(text)
			if post.Quoted != nil && post.Quoted.Text != "" {
				quoted := post.Quoted.Text
				if row.QuotedZH != nil && strings.TrimSpace(*row.QuotedZH) != strings.TrimSpace(post.Quoted.Text) {
					quoted = *row.QuotedZH
				}
				body += "<blockquote><p>引用 @" + escapeXML(post.Quoted.Handle) + "：</p>" + textToHTML(quoted)
				if post.Quoted.URL != "" {
					body += "<p><a href=\"" + escapeXML(post.Quoted.URL) + "\">" + escapeXML(post.Quoted.URL) + "</a></p>"
				}
				body += "</blockquote>"
			}
		}
	}
	if body == "" && row.BodyHTML != nil && *row.BodyHTML != "" {
		if !isChineseBody(row.Language, row.BodyText) && row.TranslationHTML != nil && *row.TranslationHTML != "" && row.TranslationComplete != nil && *row.TranslationComplete {
			body = *row.TranslationHTML
		} else {
			body = *row.BodyHTML
		}
	}
	if body == "" {
		return "", nil
	}
	proxied, err := proxyBodyImages(body, s.config.SiteURL, s.config.ImageProxySecret, now, feedImageLifetime)
	if err != nil {
		return "", err
	}
	return proxied + fmt.Sprintf("<p>—— 本文由 %s 聚合整理，完整版与%s见 <a href=\"%s\">%s</a></p>", escapeXML(s.config.SiteName), escapeXML(subjectAfter("更多", "动态", s.config.Subject)), escapeXML(itemURL), escapeXML(itemURL)), nil
}

func (s *Service) issueXML(kind string, issue publiccontent.V1FeedIssue) string {
	labels := map[string]string{"daily": "日报", "weekly": "周报", "monthly": "月报"}
	path := "/" + kind + "/" + issue.Key
	if kind == "daily" {
		path = "/daily/" + issue.Key
	}
	issueURL := absoluteURL(s.config.SiteURL, path)
	title := s.config.SiteName + " " + labels[kind] + " · " + issue.Key
	if issue.Headline != nil && *issue.Headline != "" {
		title += " — " + *issue.Headline
	}
	sections := make([]string, 0, len(issue.Sections))
	for _, section := range issue.Sections {
		links := make([]string, 0, len(section.Items))
		for _, item := range section.Items {
			links = append(links, "<li><a href=\""+escapeXML(item.Link)+"\">"+escapeXML(item.Title)+"</a></li>")
		}
		sections = append(sections, "<p><strong>"+escapeXML(section.Label)+"</strong></p>\n<ul>"+strings.Join(links, "")+"</ul>")
	}
	lead := ""
	if issue.LeadParagraph != nil {
		lead = *issue.LeadParagraph
	} else if issue.Headline != nil {
		lead = *issue.Headline
	}
	description := append([]string{"<p>" + escapeXML(lead) + "</p>"}, sections...)
	description = append(description, "<p>via "+escapeXML(s.config.SiteName)+" · <a href=\""+escapeXML(issueURL)+"\">"+escapeXML(issueURL)+"</a></p>")
	return "    <item>\n      <title>" + cdata(title) + "</title>\n      <link>" + escapeXML(issueURL) + "</link>\n      <description>" + cdata(strings.Join(description, "\n")) + "</description>\n      <pubDate>" + rfc822(issue.GeneratedAt) + "</pubDate>\n      <guid isPermaLink=\"false\">" + kind + "-" + escapeXML(issue.Key) + "</guid>\n      <author>" + escapeXML(authorAddress(s.config.SiteURL)) + " (" + escapeXML(s.config.SiteName) + ")</author>\n    </item>"
}

func renderChannel(siteURL, siteName string, meta feedMetadata, items []string) string {
	joined := strings.Join(items, "\n")
	if joined != "" {
		joined = "\n" + joined
	}
	return "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<rss version=\"2.0\" xmlns:atom=\"http://www.w3.org/2005/Atom\" xmlns:content=\"http://purl.org/rss/1.0/modules/content/\">\n  <channel>\n    <title>" + escapeXML(meta.Title) + "</title>\n    <link>" + escapeXML(absoluteURL(siteURL, meta.HomePath)) + "</link>\n    <description>" + escapeXML(meta.Description) + "</description>\n    <language>zh-CN</language>\n    <atom:link href=\"" + escapeXML(absoluteURL(siteURL, meta.SelfPath)) + "\" rel=\"self\" type=\"application/rss+xml\" />\n    <ttl>" + fmt.Sprint(meta.TTL) + "</ttl>\n    <generator>" + escapeXML(siteName) + " (" + escapeXML(absoluteURL(siteURL, "/agent")) + ")</generator>" + joined + "\n  </channel>\n</rss>\n"
}

func absoluteURL(base, path string) string { return strings.TrimRight(base, "/") + path }

func authorAddress(siteURL string) string {
	parsed, err := url.Parse(siteURL)
	if err != nil || parsed.Hostname() == "" {
		return "noreply@localhost"
	}
	return "noreply@" + parsed.Hostname()
}

func rfc822(value time.Time) string { return value.UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT") }

func cdata(value string) string {
	value = xmlSafeText(value)
	return "<![CDATA[" + strings.ReplaceAll(value, "]]>", "]]]]><![CDATA[>") + "]]>"
}

func escapeXML(value string) string {
	value = xmlSafeText(value)
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace(value)
}

func xmlSafeText(value string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
			return -1
		}
		return r
	}, value)
}

func textToHTML(value string) string {
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	paragraphs := regexp.MustCompile(`\n{2,}`).Split(value, -1)
	blocks := make([]string, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		paragraph = strings.TrimSpace(paragraph)
		if paragraph == "" {
			continue
		}
		lines := strings.Split(paragraph, "\n")
		for i, line := range lines {
			lines[i] = linkify(line)
		}
		blocks = append(blocks, "<p>"+strings.Join(lines, "<br>")+"</p>")
	}
	return strings.Join(blocks, "")
}

func linkify(value string) string {
	var result strings.Builder
	last := 0
	for _, position := range bareURLPattern.FindAllStringIndex(value, -1) {
		end := position[1]
		for end > position[0] && strings.ContainsRune(".,;:!?*'\"”’»…、。，；：！？）]】}」』》", rune(value[end-1])) {
			end--
		}
		if end <= position[0] || end <= last {
			continue
		}
		link := value[position[0]:end]
		result.WriteString(html.EscapeString(value[last:position[0]]))
		result.WriteString("<a href=\"" + html.EscapeString(link) + "\" target=\"_blank\" rel=\"noopener noreferrer nofollow\">" + html.EscapeString(link) + "</a>")
		last = end
	}
	result.WriteString(html.EscapeString(value[last:]))
	return result.String()
}

func isChineseBody(languageValue, bodyText *string) bool {
	if languageValue != nil && *languageValue == "zh" {
		return true
	}
	if languageValue != nil && *languageValue == "en" || bodyText == nil {
		return false
	}
	return regexp.MustCompile(`[一-鿿]`).MatchString(*bodyText)
}

func proxyBodyImages(body, siteURL, secret string, now time.Time, lifetime time.Duration) (string, error) {
	var proxyErr error
	body = imgTagPattern.ReplaceAllStringFunc(body, func(tag string) string {
		if proxyErr != nil {
			return tag
		}
		match := imgSourcePattern.FindStringSubmatch(tag)
		if len(match) != 2 {
			return tag
		}
		source := strings.ReplaceAll(match[1], "&amp;", "&")
		if nonArticleImage(source) {
			return ""
		}
		proxied, err := signedImageURL(source, "full", siteURL, secret, now, lifetime)
		if err != nil {
			proxyErr = err
			return tag
		}
		return strings.Replace(tag, match[0], ` src="`+html.EscapeString(proxied)+`" loading="lazy" decoding="async"`, 1)
	})
	if proxyErr != nil {
		return "", proxyErr
	}
	body = videoPosterPattern.ReplaceAllStringFunc(body, func(tag string) string {
		parts := videoPosterPattern.FindStringSubmatch(tag)
		if len(parts) != 4 {
			return tag
		}
		proxied, err := signedImageURL(strings.ReplaceAll(parts[2], "&amp;", "&"), "thumb", siteURL, secret, now, lifetime)
		if err != nil {
			proxyErr = err
			return tag
		}
		return parts[1] + html.EscapeString(proxied) + parts[3]
	})
	return body, proxyErr
}

func signedImageURL(source, mode, siteURL, secret string, now time.Time, lifetime time.Duration) (string, error) {
	parsed, err := url.Parse(source)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return "", nil
	}
	if secret == "" {
		return "", errors.New("IMG_PROXY_SIGN_SECRET is not configured")
	}
	const window = int64(24 * time.Hour / time.Second)
	exp := now.Unix() + int64(lifetime/time.Second)
	exp = ((exp + window - 1) / window) * window
	message := fmt.Sprintf("%s|%s|%d", source, mode, exp)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(message))
	signature := hex.EncodeToString(mac.Sum(nil))[:16]
	path := "/api/img-proxy?u=" + url.QueryEscape(source) + "&mode=" + url.QueryEscape(mode) + "&exp=" + fmt.Sprint(exp) + "&sig=" + signature
	return absoluteURL(siteURL, path), nil
}

func nonArticleImage(value string) bool {
	parsed, err := url.Parse(value)
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
	return false
}

func publicSourceName(name string) string { return publiccontent.PublicSourceName(name) }

func subjectAfter(text, noun, subject string) string {
	space := ""
	if len(subject) > 0 && subject[0] < 128 && ((subject[0] >= 'A' && subject[0] <= 'Z') || (subject[0] >= 'a' && subject[0] <= 'z') || (subject[0] >= '0' && subject[0] <= '9')) {
		space = " "
	}
	return text + space + subject + noun
}
