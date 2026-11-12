package publiccontent

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"
	"unicode/utf16"

	"aihot/backend-go/internal/topics"
)

const sitePoolPageSize = 40
const sitePoolMaxPages = 50

type SitePoolQuery struct {
	Channel  string
	Category *string
	Tag      *string
	Search   *string
	Tab      string
	Page     int
}

type SitePoolFilters struct {
	Channel  string  `json:"channel"`
	Category *string `json:"category"`
	Tag      *string `json:"tag"`
	Search   *string `json:"q"`
	Tab      string  `json:"tab"`
}

type SitePoolResponse struct {
	Filters    SitePoolFilters   `json:"filters"`
	Items      []topics.FeedItem `json:"items"`
	Page       int               `json:"page"`
	PageCount  int               `json:"pageCount"`
	Total      int               `json:"total"`
	TodayCount int               `json:"todayCount"`
	Freshness  string            `json:"freshness"`
}

type SitePoolRow struct {
	Item     topics.PageItemRow
	Selected bool
	Seat     bool
	FactID   *int64
}

type SitePoolHolder struct {
	ID    string
	Title string
}

type SitePoolData struct {
	Rows       []SitePoolRow
	Total      int
	TodayCount int
	UpdatedAt  *time.Time
}

type SitePoolRepository interface {
	SitePoolPage(context.Context, SitePoolQuery, time.Time, []string, string, int, int) (SitePoolData, error)
	SitePoolSeatHolders(context.Context, []int64, time.Time) (map[int64]SitePoolHolder, error)
}

func (s *Service) SitePool(ctx context.Context, query SitePoolQuery, now time.Time, imageProxySecret string, xPostIsFullText bool) (SitePoolResponse, error) {
	repository, ok := s.repository.(SitePoolRepository)
	if !ok {
		return SitePoolResponse{}, errors.New("site pool repository is unavailable")
	}
	if query.Channel != "all" && query.Channel != "news" && query.Channel != "x" && query.Channel != "firstParty" {
		return SitePoolResponse{}, errors.New("invalid channel")
	}
	query.Page = max(1, min(query.Page, sitePoolMaxPages))
	if query.Tab != "relevance" {
		query.Tab = "time"
	}
	if query.Search != nil {
		trimmed := trimPoolSearch(*query.Search)
		if trimmed == "" {
			query.Search = nil
		} else {
			trimmed = truncatePoolSearch(trimmed, 200)
			query.Search = &trimmed
		}
	}
	if query.Search == nil {
		query.Tab = "time"
	}
	terms := []string{}
	entityTag := ""
	if query.Search != nil {
		terms = itemSearchTerms(*query.Search)
		entityTag = sitePoolEntityTag(*query.Search, s.topics)
		if len(terms) > 0 {
			release, err := acquirePublicSearch(ctx)
			if err != nil {
				return SitePoolResponse{}, err
			}
			defer release()
		}
	}
	data, err := repository.SitePoolPage(ctx, query, now, terms, entityTag, (query.Page-1)*sitePoolPageSize, sitePoolPageSize)
	if err != nil {
		return SitePoolResponse{}, err
	}
	data.Total = min(data.Total, sitePoolCountCap)
	factIDs := make([]int64, 0, len(data.Rows))
	seenFacts := make(map[int64]struct{})
	for _, row := range data.Rows {
		if row.Selected && !row.Seat && row.FactID != nil {
			if _, exists := seenFacts[*row.FactID]; !exists {
				seenFacts[*row.FactID] = struct{}{}
				factIDs = append(factIDs, *row.FactID)
			}
		}
	}
	holders := map[int64]SitePoolHolder{}
	if len(factIDs) > 0 {
		holders, err = repository.SitePoolSeatHolders(ctx, factIDs, now)
		if err != nil {
			return SitePoolResponse{}, err
		}
	}
	runtime := topics.RuntimeConfig{ImageProxySecret: imageProxySecret, XPostIsFullText: xPostIsFullText}
	items := make([]topics.FeedItem, 0, len(data.Rows))
	for _, row := range data.Rows {
		selected := row.Selected
		row.Item.Selected = &selected
		item, err := topics.BuildFeedItem(row.Item, now, runtime)
		if err != nil {
			return SitePoolResponse{}, err
		}
		if row.Selected && !row.Seat && row.FactID != nil {
			if holder, exists := holders[*row.FactID]; exists {
				item.Reason = nil
				item.SameEvent = &topics.SameEvent{ID: holder.ID, Title: holder.Title}
			}
		}
		items = append(items, item)
	}
	freshness := now.UTC()
	if data.UpdatedAt != nil {
		freshness = data.UpdatedAt.UTC()
	}
	pageCount := max(1, min(sitePoolMaxPages, int(math.Ceil(float64(data.Total)/sitePoolPageSize))))
	return SitePoolResponse{
		Filters: SitePoolFilters{Channel: query.Channel, Category: query.Category, Tag: query.Tag, Search: query.Search, Tab: query.Tab},
		Items:   items, Page: query.Page, PageCount: pageCount, Total: data.Total,
		TodayCount: data.TodayCount, Freshness: formatV1Time(freshness),
	}, nil
}

func sitePoolEntityTag(query string, definitions []topics.Definition) string {
	wanted := normalizePoolAlias(query)
	if wanted == "" {
		return ""
	}
	for _, definition := range definitions {
		if definition.EntityID == "" {
			continue
		}
		aliases := []string{definition.Slug, definition.Name}
		aliases = append(aliases, strings.Split(definition.Name, "/")...)
		aliases = append(aliases, definition.Aliases...)
		for _, alias := range aliases {
			if normalizePoolAlias(alias) == wanted {
				return "entity:" + definition.EntityID
			}
		}
	}
	return ""
}

func normalizePoolAlias(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

func trimPoolSearch(value string) string {
	return strings.TrimFunc(value, func(char rune) bool { return char == '\uFEFF' || isUnicodeSpace(char) })
}

func truncatePoolSearch(value string, limit int) string {
	units := utf16.Encode([]rune(value))
	if len(units) <= limit {
		return value
	}
	return string(utf16.Decode(units[:limit]))
}

func isUnicodeSpace(char rune) bool {
	return strings.ContainsRune(" \t\n\v\f\r\u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000", char)
}
