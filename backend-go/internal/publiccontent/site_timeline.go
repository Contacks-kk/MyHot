package publiccontent

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"

	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/topics"
	"golang.org/x/text/unicode/norm"
)

const siteTimelineCursorPrefix = "tl2"

type SiteTimelineInvalidCursorError struct{ Detail string }

func (e SiteTimelineInvalidCursorError) Error() string { return e.Detail }

type SiteTimelineQuery struct {
	Channel  string
	Category *string
	Tag      *string
	Cursor   *string
	Limit    int
}

type SiteTimelineFilterView struct {
	Channel  string  `json:"channel"`
	Category *string `json:"category"`
	Tag      *string `json:"tag"`
}

type SiteTimelineGroup struct {
	FactID                string `json:"factId"`
	AdditionalSourceCount int    `json:"additionalSourceCount"`
	ReportCount           int    `json:"reportCount"`
}

type SiteTimelineCard struct {
	Key      string             `json:"key"`
	AnchorAt string             `json:"anchorAt"`
	Item     topics.FeedItem    `json:"item"`
	Group    *SiteTimelineGroup `json:"group"`
}

type SiteHotStripEntry struct {
	Rank             int                  `json:"rank"`
	Title            string               `json:"title"`
	Heat             float64              `json:"heat"`
	Trend            string               `json:"trend"`
	StoryPublicID    *string              `json:"storyPublicId"`
	ItemID           *string              `json:"itemId"`
	Participants     []SiteHotParticipant `json:"participants"`
	ParticipantCount int                  `json:"participantCount"`
}

type SiteTimelineResponse struct {
	Filters    SiteTimelineFilterView `json:"filters"`
	Cards      []SiteTimelineCard     `json:"cards"`
	NextCursor *string                `json:"nextCursor"`
	Hot        *[]SiteHotStripEntry   `json:"hot"`
	DayCounts  map[string]int         `json:"dayCounts"`
}

type SiteTimelineAnchor struct {
	Key      string
	AnchorAt time.Time
}

type SiteTimelineMember struct {
	FactID        int64
	ArticleID     string
	SourceID      string
	SourceTier    string
	PublisherRole *string
	OwnerEntityID *string
	FactSubject   *string
	Item          topics.PageItemRow
}

type SiteTimelinePoolReport struct {
	FactID    int64
	ArticleID string
	SourceID  string
}

type SiteTimelineFact struct {
	ID       int64
	PublicID string
}

type SiteTimelineFactData struct {
	Members []SiteTimelineMember
	Pool    []SiteTimelinePoolReport
	Facts   []SiteTimelineFact
}

type SiteTimelineRepository interface {
	SiteTimelineAnchors(context.Context, SiteTimelineQuery, time.Time, *siteTimelineCursor, int) ([]SiteTimelineAnchor, error)
	SiteTimelineFacts(context.Context, SiteTimelineQuery, time.Time, []int64) (SiteTimelineFactData, error)
	SiteTimelineItems(context.Context, SiteTimelineQuery, time.Time, []string) ([]topics.PageItemRow, error)
	SiteTimelineDayCounts(context.Context, SiteTimelineQuery, time.Time, []string) (map[string]int, error)
}

type siteTimelineCursor struct {
	Anchor   int64  `json:"a"`
	GroupKey string `json:"g"`
	Binding  string `json:"b"`
}

type siteTimelineBinding struct {
	Channel  string  `json:"c"`
	Category *string `json:"k"`
	Tag      *string `json:"t"`
}

type siteTimelinePlanned struct {
	Key      string
	AnchorAt time.Time
	ID       string
	FactID   int64
	Group    *SiteTimelineGroup
}

func (s *Service) SiteTimeline(ctx context.Context, query SiteTimelineQuery, now time.Time, imageProxySecret string, xPostIsFullText bool) (SiteTimelineResponse, error) {
	repository, ok := s.repository.(SiteTimelineRepository)
	if !ok {
		return SiteTimelineResponse{}, errors.New("site timeline repository is unavailable")
	}
	if query.Limit < 1 || query.Limit > 40 {
		return SiteTimelineResponse{}, errors.New("invalid timeline limit")
	}
	now = now.UTC().Truncate(time.Millisecond)
	var after *siteTimelineCursor
	if query.Cursor != nil && *query.Cursor != "" {
		cursor, err := decodeSiteTimelineCursor(*query.Cursor)
		if err != nil {
			return SiteTimelineResponse{}, SiteTimelineInvalidCursorError{Detail: err.Error()}
		}
		if cursor.Binding != siteTimelineBindingHash(query) || !validJSDateMillis(cursor.Anchor) || !validTimelineGroupKey(cursor.GroupKey) {
			return SiteTimelineResponse{}, SiteTimelineInvalidCursorError{Detail: "cursor does not match this query"}
		}
		after = &cursor
	}
	anchors, err := repository.SiteTimelineAnchors(ctx, query, now, after, query.Limit+1)
	if err != nil {
		return SiteTimelineResponse{}, err
	}
	page := anchors
	hasMore := len(page) > query.Limit
	if hasMore {
		page = page[:query.Limit]
	}
	factIDs := make([]int64, 0, len(page))
	for _, anchor := range page {
		if strings.HasPrefix(anchor.Key, "f") {
			id, err := strconv.ParseInt(anchor.Key[1:], 10, 64)
			if err != nil || id < 1 {
				return SiteTimelineResponse{}, errors.New("invalid timeline fact key")
			}
			factIDs = append(factIDs, id)
		}
	}
	var factData SiteTimelineFactData
	if len(factIDs) > 0 {
		factData, err = repository.SiteTimelineFacts(ctx, query, now, factIDs)
		if err != nil {
			return SiteTimelineResponse{}, err
		}
	}
	planned := siteTimelinePlan(page, factData, s.entities)
	itemIDs := make([]string, 0, len(planned))
	for _, item := range planned {
		itemIDs = append(itemIDs, item.ID)
	}
	rows, err := repository.SiteTimelineItems(ctx, query, now, itemIDs)
	if err != nil {
		return SiteTimelineResponse{}, err
	}
	byID := make(map[string]topics.PageItemRow, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	cards := make([]SiteTimelineCard, 0, len(planned))
	days := make(map[string]struct{}, len(page))
	for _, anchor := range page {
		days[beijingDate(anchor.AnchorAt)] = struct{}{}
	}
	runtime := topics.RuntimeConfig{ImageProxySecret: imageProxySecret, XPostIsFullText: xPostIsFullText}
	for _, item := range planned {
		row, found := byID[item.ID]
		if !found {
			continue
		}
		feedItem, err := topics.BuildFeedItem(row, now, runtime)
		if err != nil {
			return SiteTimelineResponse{}, err
		}
		cards = append(cards, SiteTimelineCard{Key: item.Key, AnchorAt: formatV1Time(item.AnchorAt), Item: feedItem, Group: item.Group})
	}
	dayKeys := make([]string, 0, len(days))
	for day := range days {
		dayKeys = append(dayKeys, day)
	}
	dayCounts, err := repository.SiteTimelineDayCounts(ctx, query, now, dayKeys)
	if err != nil {
		return SiteTimelineResponse{}, err
	}
	if dayCounts == nil {
		dayCounts = map[string]int{}
	}
	filters := SiteTimelineFilterView{Channel: query.Channel, Category: query.Category, Tag: query.Tag}
	response := SiteTimelineResponse{Filters: filters, Cards: cards, DayCounts: dayCounts}
	if hasMore && len(page) > 0 {
		last := page[len(page)-1]
		token := encodeSiteTimelineCursor(siteTimelineCursor{Anchor: last.AnchorAt.UnixMilli(), GroupKey: last.Key, Binding: siteTimelineBindingHash(query)})
		response.NextCursor = &token
	}
	if query.Channel == "all" && query.Category == nil && query.Tag == nil && after == nil {
		hot, err := s.SiteHot(ctx, now, imageProxySecret)
		if err != nil {
			return SiteTimelineResponse{}, err
		}
		if len(hot.Entries) >= 3 {
			strip := make([]SiteHotStripEntry, 0, min(5, len(hot.Entries)))
			for _, entry := range hot.Entries[:min(5, len(hot.Entries))] {
				storyID := entry.Story.PublicID
				strip = append(strip, SiteHotStripEntry{
					Rank: entry.Rank, Title: entry.Story.Title, Heat: entry.Heat, Trend: entry.Trend,
					StoryPublicID: &storyID, ItemID: entry.ItemID,
					Participants: entry.Participants, ParticipantCount: entry.ParticipantCount,
				})
			}
			response.Hot = &strip
		}
	}
	return response, nil
}

func siteTimelinePlan(anchors []SiteTimelineAnchor, data SiteTimelineFactData, entities map[string]interests.Entity) []siteTimelinePlanned {
	members := make(map[int64][]SiteTimelineMember)
	for _, row := range data.Members {
		members[row.FactID] = append(members[row.FactID], row)
	}
	pool := make(map[int64][]SiteTimelinePoolReport)
	for _, row := range data.Pool {
		pool[row.FactID] = append(pool[row.FactID], row)
	}
	facts := make(map[int64]string, len(data.Facts))
	for _, row := range data.Facts {
		facts[row.ID] = row.PublicID
	}
	planned := make([]siteTimelinePlanned, 0, len(anchors))
	for _, anchor := range anchors {
		if strings.HasPrefix(anchor.Key, "a") {
			planned = append(planned, siteTimelinePlanned{Key: anchor.Key, AnchorAt: anchor.AnchorAt, ID: anchor.Key[1:]})
			continue
		}
		factID, err := strconv.ParseInt(anchor.Key[1:], 10, 64)
		if err != nil {
			continue
		}
		candidates := members[factID]
		if len(candidates) == 0 {
			continue
		}
		sortTimelineMembers(candidates, entities)
		representative := candidates[0]
		reports := pool[factID]
		seenArticles := make(map[string]struct{}, len(reports))
		additionalSources := make(map[string]struct{}, len(reports))
		representativeSource := ""
		for _, report := range reports {
			seenArticles[report.ArticleID] = struct{}{}
			if report.ArticleID == representative.ArticleID {
				representativeSource = report.SourceID
			}
		}
		for _, report := range reports {
			if report.SourceID != representativeSource {
				additionalSources[report.SourceID] = struct{}{}
			}
		}
		var group *SiteTimelineGroup
		if len(seenArticles) > 1 {
			publicID := facts[factID]
			if publicID == "" {
				publicID = strconv.FormatInt(factID, 10)
			}
			group = &SiteTimelineGroup{FactID: publicID, ReportCount: len(seenArticles), AdditionalSourceCount: len(additionalSources)}
		}
		planned = append(planned, siteTimelinePlanned{Key: anchor.Key, AnchorAt: anchor.AnchorAt, ID: representative.ArticleID, FactID: factID, Group: group})
	}
	return planned
}

func sortTimelineMembers(rows []SiteTimelineMember, entities map[string]interests.Entity) {
	// A stable insertion sort keeps article id as the final deterministic tie breaker.
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && timelineMemberLess(rows[j], rows[j-1], entities); j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}

func timelineMemberLess(a, b SiteTimelineMember, entities map[string]interests.Entity) bool {
	if rank := timelineRepresentativeRank(a, entities); rank != timelineRepresentativeRank(b, entities) {
		return rank < timelineRepresentativeRank(b, entities)
	}
	if (a.Item.BodyMode == "full") != (b.Item.BodyMode == "full") {
		return a.Item.BodyMode == "full"
	}
	aScore, bScore := 0.0, 0.0
	if a.Item.Score != nil {
		aScore = *a.Item.Score
	}
	if b.Item.Score != nil {
		bScore = *b.Item.Score
	}
	if aScore != bScore {
		return aScore > bScore
	}
	if !a.Item.TimelineAt.Equal(b.Item.TimelineAt) {
		return a.Item.TimelineAt.Before(b.Item.TimelineAt)
	}
	return a.ArticleID < b.ArticleID
}

func timelineRepresentativeRank(row SiteTimelineMember, entities map[string]interests.Entity) int {
	if row.SourceTier == "T1" {
		return 0
	}
	ownerID := ""
	if row.OwnerEntityID != nil {
		ownerID = *row.OwnerEntityID
	}
	owner := siteTimelineEntityIdentity(ownerID, entities)
	if owner == "" || row.FactSubject == nil {
		return 3
	}
	subjects := strings.FieldsFunc(*row.FactSubject, func(value rune) bool {
		return strings.ContainsRune(",，、;；+＋&＆/", value)
	})
	if len(subjects) == 0 {
		return 3
	}
	containsOwner := false
	for _, subject := range subjects {
		identity := siteTimelineEntityIdentity(subject, entities)
		if identity == "" {
			return 3
		}
		containsOwner = containsOwner || identity == owner
	}
	if !containsOwner || row.PublisherRole == nil {
		return 3
	}
	switch *row.PublisherRole {
	case "organization":
		return 1
	case "person":
		return 2
	default:
		return 3
	}
}

func siteTimelineEntityIdentity(value string, entities map[string]interests.Entity) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	wanted := timelineNormalizeIdentity(value)
	matched := ""
	for id, entity := range entities {
		values := append([]string{id, entity.Name}, entity.Aliases...)
		values = append(values, entity.OtherNames...)
		for _, alias := range values {
			if timelineNormalizeIdentity(alias) != wanted {
				continue
			}
			if matched != "" && matched != id {
				return ""
			}
			matched = id
		}
	}
	return matched
}

func timelineNormalizeIdentity(value string) string {
	value = norm.NFKC.String(strings.TrimSpace(value))
	return strings.ToLower(strings.Join(strings.FieldsFunc(value, unicode.IsSpace), ""))
}

func siteTimelineBindingHash(query SiteTimelineQuery) string {
	encoded, _ := json.Marshal(siteTimelineBinding{Channel: query.Channel, Category: query.Category, Tag: query.Tag})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])[:12]
}

func encodeSiteTimelineCursor(cursor siteTimelineCursor) string {
	encoded, _ := json.Marshal(cursor)
	return siteTimelineCursorPrefix + "." + base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeSiteTimelineCursor(token string) (siteTimelineCursor, error) {
	var cursor siteTimelineCursor
	if !strings.HasPrefix(token, siteTimelineCursorPrefix+".") {
		return cursor, errors.New("unknown cursor format")
	}
	encoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, siteTimelineCursorPrefix+"."))
	if err != nil || json.Unmarshal(encoded, &cursor) != nil {
		return siteTimelineCursor{}, errors.New("malformed cursor")
	}
	return cursor, nil
}

func validTimelineGroupKey(value string) bool {
	if len(value) < 2 {
		return false
	}
	if value[0] == 'a' {
		return articleIDPattern.MatchString(value[1:])
	}
	if value[0] == 'f' {
		id, err := strconv.ParseInt(value[1:], 10, 64)
		return err == nil && id > 0
	}
	return false
}

func beijingDate(value time.Time) string {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		location = time.FixedZone("Asia/Shanghai", 8*60*60)
	}
	return value.In(location).Format("2006-01-02")
}
