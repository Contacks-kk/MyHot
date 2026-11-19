package topics

import (
	"context"
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

	"aihot/backend-go/internal/model"
)

const pageSize = 20
const imageWindow = 24 * time.Hour
const imageLifetime = 48 * time.Hour

var topicSourceNotePattern = regexp.MustCompile(`（[^（）]*）`)
var topicXSourceNamePattern = regexp.MustCompile(`^X[:：]\s*(.*?)\s*(?:\(@[^)]*\))?\s*$`)
var topicXHandlePattern = regexp.MustCompile(`@\w+`)

type PageRepository interface {
	Page(context.Context, Definition, int, time.Time) (PageData, error)
}

type PageData struct {
	Total     int
	Recent    int
	PoolTotal int
	Latest    *Latest
	Items     []PageItemRow
}

type PageItemRow struct {
	ID          string
	Title       string
	Summary     *string
	Reason      *string
	Category    *string
	Tags        model.TextArray `gorm:"column:tags;type:text[]"`
	Score       *float64
	Selected    *bool
	Channel     string
	PublishedAt *time.Time
	TimelineAt  time.Time
	BodyMode    string
	SourceName  string
	XPost       json.RawMessage
	QuotedZH    *string
}

type TopicPage struct {
	Topic     PageSummary    `json:"topic"`
	Modules   map[string]any `json:"modules"`
	Items     []FeedItem     `json:"items"`
	Page      int            `json:"page"`
	PageCount int            `json:"pageCount"`
	PageSize  int            `json:"pageSize"`
}

type PageSummary struct {
	Summary
	GroupName string `json:"groupName"`
	PoolTotal int    `json:"poolTotal"`
}

type FeedItem struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Summary     *string    `json:"summary"`
	Reason      *string    `json:"reason"`
	Source      SourceRef  `json:"source"`
	PublishedAt *string    `json:"publishedAt"`
	TimelineAt  string     `json:"timelineAt"`
	Category    *string    `json:"category"`
	Tags        []string   `json:"tags"`
	Score       *int       `json:"score"`
	Selected    bool       `json:"selected"`
	Channel     string     `json:"channel"`
	X           *FeedX     `json:"x"`
	SameEvent   *SameEvent `json:"sameEvent,omitempty"`
}

type SameEvent struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type SourceRef struct {
	Name string `json:"name"`
}

type FeedX struct {
	AuthorName   string      `json:"authorName"`
	Handle       string      `json:"handle"`
	AvatarURL    *string     `json:"avatarUrl"`
	AvatarSrcSet *string     `json:"avatarSrcSet,omitempty"`
	Media        []MediaView `json:"media"`
	Quoted       *QuotedCard `json:"quoted"`
}

type QuotedCard struct {
	AuthorName  string  `json:"authorName"`
	Handle      string  `json:"handle"`
	Text        string  `json:"text"`
	Translation *string `json:"translation"`
}

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

type RuntimeConfig struct {
	ImageProxySecret string
	XPostIsFullText  bool
}

type rawXPost struct {
	AuthorName any        `json:"authorName"`
	Handle     any        `json:"handle"`
	AvatarURL  *string    `json:"avatarUrl"`
	Quoted     *rawQuoted `json:"quoted"`
	Media      []rawMedia `json:"media"`
}

type rawQuoted struct {
	AuthorName any `json:"authorName"`
	Handle     any `json:"handle"`
	Text       any `json:"text"`
}

type rawMedia struct {
	Kind   string   `json:"kind"`
	URL    string   `json:"url"`
	Poster string   `json:"poster"`
	Width  *float64 `json:"width"`
	Height *float64 `json:"height"`
	Alt    *string  `json:"alt"`
}

func (s *Service) Page(ctx context.Context, slug string, page float64, now time.Time) (*TopicPage, error) {
	definition, ok := s.find(slug)
	if !ok || math.IsNaN(page) || math.IsInf(page, 0) || page < 1 || math.Trunc(page) != page || page > float64(math.MaxInt) {
		return nil, nil
	}
	repository, ok := s.repository.(PageRepository)
	if !ok {
		return nil, errors.New("topic page repository is unavailable")
	}
	data, err := repository.Page(ctx, definition, int(page), now)
	if err != nil {
		return nil, err
	}
	pageCount := max(1, (data.Total+pageSize-1)/pageSize)
	if int(page) > pageCount {
		return nil, nil
	}

	var brand *Brand
	if definition.Group == "company" {
		brand = &Brand{Monogram: monogram(definition.Name)}
	}
	var latest *Latest
	if data.Latest != nil {
		latest = data.Latest
	}
	groupName := ""
	for _, group := range s.groups {
		if group.Key == definition.Group {
			groupName = group.Name
			break
		}
	}
	items := make([]FeedItem, 0, len(data.Items))
	for _, row := range data.Items {
		item, err := toFeedItem(row, now, s.runtime)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}

	return &TopicPage{
		Topic: PageSummary{
			Summary: Summary{
				Slug: definition.Slug, Name: definition.Name, Group: definition.Group, Definition: definition.Definition,
				Brand: brand, Total: data.Total, Recent: data.Recent,
				Indexable: data.Total >= 50 || data.Total >= 20 && data.Recent > 0, Latest: latest,
			},
			GroupName: groupName, PoolTotal: data.PoolTotal,
		},
		Modules: map[string]any{}, Items: items, Page: int(page), PageCount: pageCount, PageSize: pageSize,
	}, nil
}

func (s *Service) find(slug string) (Definition, bool) {
	for _, definition := range s.topics {
		if definition.Slug == slug {
			return definition, true
		}
	}
	return Definition{}, false
}

func toFeedItem(row PageItemRow, now time.Time, runtime RuntimeConfig) (FeedItem, error) {
	selected := true
	if row.Selected != nil {
		selected = *row.Selected
	}
	reason := row.Reason
	if !selected {
		reason = nil
	}
	item := FeedItem{
		ID: row.ID, Title: row.Title, Summary: row.Summary, Reason: reason,
		Source: SourceRef{Name: topicPublicSourceName(row.SourceName)}, TimelineAt: row.TimelineAt.UTC().Format("2006-01-02T15:04:05.000Z"),
		Category: row.Category, Tags: displayTags([]string(row.Tags)), Selected: selected, Channel: row.Channel,
	}
	if item.Tags == nil {
		item.Tags = []string{}
	}
	if row.PublishedAt != nil {
		published := row.PublishedAt.UTC().Format("2006-01-02T15:04:05.000Z")
		item.PublishedAt = &published
	}
	if row.Score != nil {
		score := int(math.Round(*row.Score))
		item.Score = &score
	}
	if row.Channel != "x" || runtime.XPostIsFullText && row.BodyMode != "full" || len(row.XPost) == 0 || string(row.XPost) == "null" {
		return item, nil
	}
	var post rawXPost
	if err := json.Unmarshal(row.XPost, &post); err != nil {
		return FeedItem{}, errors.New("invalid X post data")
	}
	avatar, err := proxyImage(post.AvatarURL, "avatar", now, runtime.ImageProxySecret)
	if err != nil {
		return FeedItem{}, err
	}
	avatarSet, err := proxyImageSet(post.AvatarURL, []string{"avatar-48", "avatar-96"}, []int{48, 96}, now, runtime.ImageProxySecret)
	if err != nil {
		return FeedItem{}, err
	}
	feedX := &FeedX{AuthorName: jsString(post.AuthorName, jsString(post.Handle, "")), Handle: jsString(post.Handle, ""), AvatarURL: avatar, AvatarSrcSet: avatarSet, Media: []MediaView{}}
	if post.Quoted != nil {
		quotedText := jsString(post.Quoted.Text, "")
		var translation *string
		if row.QuotedZH != nil && strings.TrimSpace(*row.QuotedZH) != strings.TrimSpace(quotedText) {
			translation = row.QuotedZH
		}
		feedX.Quoted = &QuotedCard{AuthorName: jsString(post.Quoted.AuthorName, ""), Handle: jsString(post.Quoted.Handle, ""), Text: quotedText, Translation: translation}
	}
	for _, media := range post.Media {
		mode := "thumb"
		if len(post.Media) > 1 {
			mode = "card"
		}
		view, err := toMediaView(media, mode, now, runtime.ImageProxySecret)
		if err != nil {
			return FeedItem{}, err
		}
		if view != nil {
			feedX.Media = append(feedX.Media, *view)
		}
	}
	item.X = feedX
	return item, nil
}

// BuildFeedItem projects a publication row into the card DTO shared by site feeds.
func BuildFeedItem(row PageItemRow, now time.Time, runtime RuntimeConfig) (FeedItem, error) {
	return toFeedItem(row, now, runtime)
}

func toMediaView(raw rawMedia, mode string, now time.Time, secret string) (*MediaView, error) {
	if nonArticleImage(raw.URL, raw.Width, raw.Height) {
		return nil, nil
	}
	urlValue, err := proxyImage(stringPointer(raw.URL), mode, now, secret)
	if err != nil {
		return nil, err
	}
	if urlValue == nil {
		return nil, nil
	}
	fullURL, err := proxyImage(stringPointer(raw.URL), "full", now, secret)
	if err != nil {
		return nil, err
	}
	setURL := raw.URL
	if raw.Poster != "" {
		setURL = raw.Poster
	}
	set, err := proxyImageSet(stringPointer(setURL), []string{"image-336", "image-720"}, []int{336, 720}, now, secret)
	if err != nil {
		return nil, err
	}
	kind := "image"
	if raw.Kind == "video" {
		kind = "video"
	}
	poster, err := proxyImage(optionalString(raw.Poster), mode, now, secret)
	if err != nil {
		return nil, err
	}
	return &MediaView{Kind: kind, URL: *urlValue, FullURL: fullURL, Width: raw.Width, Height: raw.Height, Alt: raw.Alt, Poster: poster, SrcSet: set}, nil
}

func proxyImageSet(raw *string, modes []string, widths []int, now time.Time, secret string) (*string, error) {
	if raw == nil || !isHTTPURL(*raw) || nonArticleImage(*raw, nil, nil) {
		return nil, nil
	}
	values := make([]string, 0, len(modes))
	for i, mode := range modes {
		value, err := proxyImage(raw, mode, now, secret)
		if err != nil {
			return nil, err
		}
		if value != nil {
			values = append(values, fmt.Sprintf("%s %dw", *value, widths[i]))
		}
	}
	if len(values) == 0 {
		return nil, nil
	}
	set := strings.Join(values, ", ")
	return &set, nil
}

func proxyImage(raw *string, mode string, now time.Time, secret string) (*string, error) {
	if raw == nil || *raw == "" {
		return nil, nil
	}
	if strings.HasPrefix(strings.ToLower(*raw), "data:") {
		return raw, nil
	}
	if !isHTTPURL(*raw) || nonArticleImage(*raw, nil, nil) {
		return nil, nil
	}
	if secret == "" {
		return nil, errors.New("IMG_PROXY_SIGN_SECRET is not configured")
	}
	exp := int64(math.Ceil(float64(now.Unix()+int64(imageLifetime/time.Second))/float64(int64(imageWindow/time.Second))) * float64(int64(imageWindow/time.Second)))
	message := fmt.Sprintf("%s|%s|%d", *raw, mode, exp)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(message))
	signature := hex.EncodeToString(mac.Sum(nil))[:16]
	path := "/api/img-proxy?u=" + url.QueryEscape(*raw) + "&mode=" + url.QueryEscape(mode) + "&exp=" + fmt.Sprint(exp) + "&sig=" + signature
	return &path, nil
}

func isHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https")) && u.Hostname() != ""
}

func nonArticleImage(raw string, width, height *float64) bool {
	if width != nil && height != nil && *width <= 1 && *height <= 1 {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "ids4.ad.gt", "ids.ad.gt", "secure.adnxs.com", "sync.1rx.io", "ssum-sec.casalemedia.com", "sync.smartadserver.com", "token.rubiconproject.com", "image2.pubmatic.com", "sync.go.sonobi.com", "onetag-sys.com":
		return true
	}
	host := strings.ToLower(u.Hostname())
	if strings.HasPrefix(host, "www.") {
		host = strings.TrimPrefix(host, "www.")
	} else if strings.HasPrefix(host, "m.") {
		host = strings.TrimPrefix(host, "m.")
	}
	if host != "youtube.com" {
		return false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	return len(parts) == 1 && parts[0] == "watch" || len(parts) == 2 && parts[0] == "shorts"
}

func displayTags(tags []string) []string {
	result := make([]string, 0, len(tags))
	for _, tag := range tags {
		if !strings.HasPrefix(tag, "entity:") {
			result = append(result, tag)
		}
	}
	return result
}

func topicPublicSourceName(name string) string {
	bare := name
	for topicSourceNotePattern.MatchString(bare) {
		bare = topicSourceNotePattern.ReplaceAllString(bare, " ")
	}
	if matches := topicXSourceNamePattern.FindStringSubmatch(bare); len(matches) > 1 {
		bare = matches[1]
	}
	bare = strings.Join(strings.Fields(bare), " ")
	if bare != "" {
		return bare
	}
	if handle := topicXHandlePattern.FindString(name); handle != "" {
		return handle
	}
	return strings.TrimSpace(name)
}

func jsString(value any, fallback string) string {
	if value == nil {
		return fallback
	}
	return fmt.Sprint(value)
}

func stringPointer(value string) *string { return &value }

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
