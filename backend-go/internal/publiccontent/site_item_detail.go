package publiccontent

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"regexp"
	"strings"
	"time"

	"aihot/backend-go/internal/bodyhtml"
	"aihot/backend-go/internal/mediaurl"
	"aihot/backend-go/internal/topics"
)

type SiteItemRepository interface {
	SiteItem(context.Context, string, []topics.Definition) (*SiteItemDetailRow, error)
	SiteItemRelatedStories(context.Context, string) ([]SiteItemStoryRef, error)
	SiteItemGroup(context.Context, int64, time.Time) (*SiteItemGroup, error)
	SiteItemSeatHolder(context.Context, int64, time.Time) (*topics.SameEvent, error)
}

type SiteItemStoryRef struct {
	PublicID string `json:"publicId"`
	Title    string `json:"title"`
}

type SiteItemDetailRow struct {
	Item            topics.PageItemRow `gorm:"embedded"`
	OriginalTitle   *string
	OriginalURL     string
	DiscoveredAt    time.Time
	Visibility      string
	SourceMode      string
	Indexable       bool
	Seat            bool
	FactID          *int64
	Author          *string
	Language        *string
	BodyHTML        *string
	BodyText        *string
	BodyStatus      string
	TranslationHTML *string
	TranslationText *string
	TranslationDone *bool
	TopicSlugs      []string
	StoryPublicID   *string
	StoryTitle      *string
}

type SiteItemGroup struct {
	FactID                string `json:"factId"`
	AdditionalSourceCount int    `json:"additionalSourceCount"`
	ReportCount           int    `json:"reportCount"`
}

type SiteItemSummary struct {
	ID            string           `json:"id"`
	Title         string           `json:"title"`
	OriginalTitle *string          `json:"originalTitle"`
	Summary       *string          `json:"summary"`
	Reason        *string          `json:"reason"`
	Source        topics.SourceRef `json:"source"`
	Links         struct {
		Original string `json:"original"`
	} `json:"links"`
	PublishedAt  *string           `json:"publishedAt"`
	DiscoveredAt string            `json:"discoveredAt"`
	TimelineAt   string            `json:"timelineAt"`
	Category     *string           `json:"category"`
	Tags         []string          `json:"tags"`
	Score        *int              `json:"score"`
	Selected     bool              `json:"selected"`
	Channel      string            `json:"channel"`
	Story        *SiteItemStoryRef `json:"story"`
}

type SiteItemBody struct {
	ZH       *string `json:"zh"`
	Original *string `json:"original"`
	ZHKind   *string `json:"zhKind"`
	Complete bool    `json:"complete"`
}

type SiteItemMedia struct {
	Kind   string   `json:"kind"`
	URL    string   `json:"url"`
	Width  *float64 `json:"width"`
	Height *float64 `json:"height"`
	Alt    *string  `json:"alt"`
	Poster *string  `json:"poster"`
	SrcSet *string  `json:"srcSet,omitempty"`
}

type SiteItemQuotedPost struct {
	AuthorName  string  `json:"authorName"`
	Handle      string  `json:"handle"`
	Text        string  `json:"text"`
	URL         string  `json:"url"`
	Translation *string `json:"translation"`
}

type SiteItemX struct {
	AuthorName   string              `json:"authorName"`
	Handle       string              `json:"handle"`
	AvatarURL    *string             `json:"avatarUrl"`
	AvatarSrcSet *string             `json:"avatarSrcSet,omitempty"`
	Quoted       *SiteItemQuotedPost `json:"quoted"`
	Media        []SiteItemMedia     `json:"media"`
}

type SiteItemTopic struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type SiteItemDetail struct {
	SiteItemSummary
	X                 *SiteItemX              `json:"x"`
	SameEvent         *topics.SameEvent       `json:"sameEvent,omitempty"`
	ReadingMode       string                  `json:"readingMode"`
	Author            *string                 `json:"author"`
	Body              *SiteItemBody           `json:"body"`
	Outline           []bodyhtml.OutlineEntry `json:"outline"`
	RelatedStories    []SiteItemStoryRef      `json:"relatedStories"`
	Topics            []SiteItemTopic         `json:"topics"`
	Indexable         bool                    `json:"indexable"`
	MarkdownAvailable bool                    `json:"markdownAvailable"`
	Group             *SiteItemGroup          `json:"group"`
	HasTranslation    bool                    `json:"hasTranslation"`
	BodyLanguage      string                  `json:"bodyLanguage"`
}

type SiteItemLookup struct {
	Item  *SiteItemDetail
	Found bool
}

type siteItemXRaw struct {
	AuthorName any                `json:"authorName"`
	Handle     any                `json:"handle"`
	Text       any                `json:"text"`
	AvatarURL  *string            `json:"avatarUrl"`
	Quoted     *siteItemQuoteRaw  `json:"quoted"`
	Media      []siteItemMediaRaw `json:"media"`
}

type siteItemQuoteRaw struct {
	AuthorName any `json:"authorName"`
	Handle     any `json:"handle"`
	Text       any `json:"text"`
	URL        any `json:"url"`
}

type siteItemMediaRaw struct {
	Kind   string   `json:"kind"`
	URL    string   `json:"url"`
	Poster string   `json:"poster"`
	Width  *float64 `json:"width"`
	Height *float64 `json:"height"`
	Alt    *string  `json:"alt"`
}

var siteItemIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)

func ValidSiteItemID(id string) bool { return siteItemIDPattern.MatchString(id) }

func (s *Service) SiteItem(ctx context.Context, id, language string, now time.Time, imageProxySecret string, xPostIsFullText bool) (SiteItemLookup, error) {
	if !siteItemIDPattern.MatchString(id) || language != "zh" && language != "original" {
		return SiteItemLookup{}, nil
	}
	repository, ok := s.repository.(SiteItemRepository)
	if !ok {
		return SiteItemLookup{}, errors.New("site item repository is unavailable")
	}
	row, err := repository.SiteItem(ctx, id, s.topics)
	if err != nil || row == nil || row.Visibility == "withdrawn" || row.SourceMode != "editorial" {
		return SiteItemLookup{}, err
	}
	item := siteItemSummary(*row)
	if row.Visibility == "summary-only" {
		item.Selected, item.Score, item.Category, item.Story, item.Reason = false, nil, nil, nil, nil
		item.Tags = []string{}
		return SiteItemLookup{Found: true, Item: &SiteItemDetail{
			SiteItemSummary: item, X: nil, ReadingMode: "summary-only", Author: nil, Body: nil,
			Outline: []bodyhtml.OutlineEntry{}, RelatedStories: []SiteItemStoryRef{}, Topics: []SiteItemTopic{},
			Indexable: false, MarkdownAvailable: false, Group: nil, HasTranslation: false, BodyLanguage: "original",
		}}, nil
	}
	result := &SiteItemDetail{
		SiteItemSummary: item, ReadingMode: "full", Author: row.Author, Outline: []bodyhtml.OutlineEntry{},
		RelatedStories: []SiteItemStoryRef{}, Topics: []SiteItemTopic{}, Indexable: row.Indexable,
		BodyLanguage: "original",
	}
	result.RelatedStories, err = repository.SiteItemRelatedStories(ctx, id)
	if err != nil {
		return SiteItemLookup{}, err
	}
	if result.RelatedStories == nil {
		result.RelatedStories = []SiteItemStoryRef{}
	}
	for _, slug := range row.TopicSlugs {
		for _, definition := range s.topics {
			if definition.Slug == slug {
				result.Topics = append(result.Topics, SiteItemTopic{Slug: definition.Slug, Name: definition.Name})
				break
			}
		}
	}
	if row.FactID != nil {
		result.Group, err = repository.SiteItemGroup(ctx, *row.FactID, now)
		if err != nil {
			return SiteItemLookup{}, err
		}
		if row.Item.Selected != nil && *row.Item.Selected && !row.Seat {
			result.SameEvent, err = repository.SiteItemSeatHolder(ctx, *row.FactID, now)
			if err != nil {
				return SiteItemLookup{}, err
			}
			if result.SameEvent != nil {
				result.Reason = nil
			}
		}
	}
	if row.Item.Channel == "x" && (!xPostIsFullText || row.Item.BodyMode == "full") {
		result.X, err = buildSiteItemX(row, now, imageProxySecret)
		if err != nil {
			return SiteItemLookup{}, err
		}
		text := ""
		if len(row.Item.XPost) > 0 {
			var post siteItemXRaw
			if err := json.Unmarshal(row.Item.XPost, &post); err != nil {
				return SiteItemLookup{}, errors.New("invalid X post data")
			}
			text = siteItemString(post.Text, "")
		}
		translation := optionalTrimmed(row.TranslationText)
		var zhHTML, originalHTML *string
		if translation != nil && strings.TrimSpace(*translation) != strings.TrimSpace(text) {
			converted := plainTextHTML(*translation)
			zhHTML = &converted
		}
		if strings.TrimSpace(text) != "" {
			converted := plainTextHTML(text)
			originalHTML = &converted
		}
		body, outline, hasTranslation, bodyLanguage, err := siteItemReadingBody(language, zhHTML, "translation", originalHTML, true, "", imageProxySecret, now)
		if err != nil {
			return SiteItemLookup{}, err
		}
		result.Body, result.Outline, result.HasTranslation, result.BodyLanguage = body, outline, hasTranslation, bodyLanguage
	} else if row.Item.BodyMode == "full" && row.BodyHTML != nil && *row.BodyHTML != "" {
		chinese := isChineseBody(row.Language, row.BodyText)
		var zhHTML, originalHTML *string
		zhKind := "original"
		complete := true
		if chinese {
			zhHTML = row.BodyHTML
		} else {
			originalHTML = row.BodyHTML
			if row.TranslationHTML != nil && *row.TranslationHTML != "" {
				zhHTML, zhKind = row.TranslationHTML, "translation"
			}
			complete = row.TranslationDone != nil && *row.TranslationDone
		}
		body, outline, hasTranslation, bodyLanguage, err := siteItemReadingBody(language, zhHTML, zhKind, originalHTML, complete, row.OriginalURL, imageProxySecret, now)
		if err != nil {
			return SiteItemLookup{}, err
		}
		result.Body, result.Outline, result.HasTranslation, result.BodyLanguage = body, outline, hasTranslation, bodyLanguage
	}
	result.MarkdownAvailable = siteItemMarkdownAvailable(row, xPostIsFullText)
	return SiteItemLookup{Item: result, Found: true}, nil
}

func siteItemSummary(row SiteItemDetailRow) SiteItemSummary {
	item := SiteItemSummary{
		ID: row.Item.ID, Title: row.Item.Title, OriginalTitle: row.OriginalTitle, Summary: row.Item.Summary,
		Reason: row.Item.Reason, Source: topics.SourceRef{Name: PublicSourceName(row.Item.SourceName)},
		PublishedAt: formatNullableTime(row.Item.PublishedAt), DiscoveredAt: formatV1Time(row.DiscoveredAt),
		TimelineAt: formatV1Time(row.Item.TimelineAt), Category: row.Item.Category, Tags: displayItemTags(row.Item.Tags),
		Selected: row.Item.Selected == nil || *row.Item.Selected, Channel: row.Item.Channel,
	}
	item.Links.Original = row.OriginalURL
	if item.Tags == nil {
		item.Tags = []string{}
	}
	if row.Item.Score != nil {
		score := int(math.Round(*row.Item.Score))
		item.Score = &score
	}
	if row.StoryPublicID != nil && row.StoryTitle != nil {
		item.Story = &SiteItemStoryRef{PublicID: *row.StoryPublicID, Title: *row.StoryTitle}
	}
	return item
}

func buildSiteItemX(row *SiteItemDetailRow, now time.Time, secret string) (*SiteItemX, error) {
	if len(row.Item.XPost) == 0 || string(row.Item.XPost) == "null" {
		return nil, nil
	}
	var post siteItemXRaw
	if err := json.Unmarshal(row.Item.XPost, &post); err != nil {
		return nil, errors.New("invalid X post data")
	}
	handle := siteItemString(post.Handle, "")
	avatar, err := mediaurl.ProxyURL(post.AvatarURL, "avatar", now, 48*time.Hour, secret)
	if err != nil {
		return nil, err
	}
	avatarSet, err := mediaurl.ProxyURLSet(post.AvatarURL, []string{"avatar-48", "avatar-96"}, []int{48, 96}, now, 48*time.Hour, secret)
	if err != nil {
		return nil, err
	}
	result := &SiteItemX{AuthorName: siteItemString(post.AuthorName, handle), Handle: handle, AvatarURL: avatar, AvatarSrcSet: avatarSet, Media: []SiteItemMedia{}}
	if post.Quoted != nil {
		quotedText := siteItemString(post.Quoted.Text, "")
		var translated *string
		if row.Item.QuotedZH != nil && strings.TrimSpace(*row.Item.QuotedZH) != strings.TrimSpace(quotedText) {
			translated = row.Item.QuotedZH
		}
		result.Quoted = &SiteItemQuotedPost{AuthorName: siteItemString(post.Quoted.AuthorName, ""), Handle: siteItemString(post.Quoted.Handle, ""), Text: quotedText, URL: siteItemString(post.Quoted.URL, ""), Translation: translated}
	}
	for _, media := range post.Media {
		if mediaurl.IsNonArticleImage(media.URL, media.Width, media.Height) {
			continue
		}
		imageURL, err := mediaurl.ProxyURL(&media.URL, "full", now, 48*time.Hour, secret)
		if err != nil {
			return nil, err
		}
		if imageURL == nil {
			continue
		}
		var poster *string
		if media.Poster != "" {
			poster, err = mediaurl.ProxyURL(&media.Poster, "thumb", now, 48*time.Hour, secret)
			if err != nil {
				return nil, err
			}
		}
		setURL := media.URL
		if media.Poster != "" {
			setURL = media.Poster
		}
		srcSet, err := mediaurl.ProxyURLSet(&setURL, []string{"image-720", "image-1200", "image-1600"}, []int{720, 1200, 1600}, now, 48*time.Hour, secret)
		if err != nil {
			return nil, err
		}
		kind := "image"
		if media.Kind == "video" {
			kind = "video"
		}
		result.Media = append(result.Media, SiteItemMedia{Kind: kind, URL: *imageURL, Width: media.Width, Height: media.Height, Alt: media.Alt, Poster: poster, SrcSet: srcSet})
	}
	return result, nil
}

func siteItemReadingBody(language string, zhHTML *string, zhKind string, originalHTML *string, complete bool, baseURL, secret string, now time.Time) (*SiteItemBody, []bodyhtml.OutlineEntry, bool, string, error) {
	bodyLanguage := "original"
	if language == "original" && originalHTML != nil && *originalHTML != "" {
		bodyLanguage = "original"
	} else if zhHTML != nil && *zhHTML != "" {
		bodyLanguage = "zh"
	} else if originalHTML != nil && *originalHTML != "" {
		bodyLanguage = "original"
	}
	var shown *string
	if bodyLanguage == "zh" {
		shown = zhHTML
	} else {
		shown = originalHTML
	}
	body := &SiteItemBody{Complete: complete}
	outline := []bodyhtml.OutlineEntry{}
	if shown != nil && *shown != "" {
		safe, entries, err := bodyhtml.Sanitize(*shown, baseURL, secret, now)
		if err != nil {
			return nil, nil, false, "", err
		}
		outline = entries
		if bodyLanguage == "zh" {
			body.ZH = &safe
		} else {
			body.Original = &safe
		}
		if zhHTML != nil && *zhHTML != "" {
			body.ZHKind = &zhKind
		}
	}
	hasTranslation := zhKind == "translation" && zhHTML != nil && *zhHTML != "" && originalHTML != nil && *originalHTML != ""
	return body, outline, hasTranslation, bodyLanguage, nil
}

func siteItemMarkdownAvailable(row *SiteItemDetailRow, xPostIsFullText bool) bool {
	if row.Visibility != "public" || row.SourceMode != "editorial" {
		return false
	}
	var post siteItemXRaw
	showPost := row.Item.Channel == "x" && (!xPostIsFullText || row.Item.BodyMode == "full") && json.Unmarshal(row.Item.XPost, &post) == nil
	return row.Item.Summary != nil && *row.Item.Summary != "" || showPost && strings.TrimSpace(siteItemString(post.Text, "")) != "" || row.Item.BodyMode == "full" && row.BodyHTML != nil && *row.BodyHTML != ""
}

func isChineseBody(language, text *string) bool {
	if language != nil && *language == "zh" {
		return true
	}
	if language != nil && *language == "en" || text == nil {
		return false
	}
	return regexp.MustCompile(`[\x{4e00}-\x{9fff}]`).MatchString(*text)
}

func displayItemTags(tags []string) []string {
	result := make([]string, 0, len(tags))
	for _, tag := range tags {
		if !strings.HasPrefix(tag, "entity:") {
			result = append(result, tag)
		}
	}
	return result
}

func formatNullableTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := formatV1Time(*value)
	return &formatted
}

func optionalTrimmed(value *string) *string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	return value
}

func siteItemString(value any, fallback string) string {
	if value == nil {
		return fallback
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fallback
}

func plainTextHTML(value string) string {
	paragraphs := regexp.MustCompile(`\n\s*\n`).Split(strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n"), -1)
	result := make([]string, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		paragraph = strings.TrimSpace(paragraph)
		if paragraph == "" {
			continue
		}
		lines := strings.Split(paragraph, "\n")
		for index, line := range lines {
			lines[index] = htmlEscapeText(line)
		}
		result = append(result, "<p>"+strings.Join(lines, "<br>")+"</p>")
	}
	return strings.Join(result, "")
}

func htmlEscapeText(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;").Replace(value)
}

func resolveSiteOriginalURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return parsed.String()
}
