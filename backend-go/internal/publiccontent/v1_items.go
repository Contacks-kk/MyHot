package publiccontent

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"gorm.io/gorm"
)

const itemCursorPrefix = "it3"

var itemCursorIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)

type V1ItemsQuery struct {
	Mode         string
	Window       string
	By           string
	Category     *string
	CategoryKeys []string
	Search       *string
	Limit        int
	Cursor       *string
}

type V1ItemsResponse struct {
	SchemaVersion int                `json:"schemaVersion"`
	Query         V1ItemsQueryResult `json:"query"`
	Items         []V1Item           `json:"items"`
	Page          V1ItemsPage        `json:"page"`
}

type V1ItemsQueryResult struct {
	Mode     string  `json:"mode"`
	Category *string `json:"category"`
	Window   string  `json:"window"`
	Search   *string `json:"q"`
	By       string  `json:"by"`
	Ordering string  `json:"ordering"`
}

type V1ItemsPage struct {
	Count      int     `json:"count"`
	HasMore    bool    `json:"hasMore"`
	NextCursor *string `json:"nextCursor"`
}

type V1Item struct {
	ID            string       `json:"id"`
	Title         string       `json:"title"`
	OriginalTitle *string      `json:"originalTitle"`
	Summary       *string      `json:"summary"`
	Source        V1ItemSource `json:"source"`
	Links         V1ItemLinks  `json:"links"`
	PublishedAt   *string      `json:"publishedAt"`
	DiscoveredAt  string       `json:"discoveredAt"`
	Category      *string      `json:"category"`
	Score         *int         `json:"score"`
	Selected      bool         `json:"selected"`
	Reason        *string      `json:"reason"`
}

type V1ItemSource struct {
	Name string `json:"name"`
}

type V1ItemLinks struct {
	AIHot    string `json:"aihot"`
	Original string `json:"original"`
}

type V1ItemsRepository interface {
	V1Items(context.Context, V1ItemsQuery, time.Time, *time.Time, string, []string) ([]V1ItemRecord, error)
}

type V1ItemRecord struct {
	ID            string     `gorm:"column:id"`
	Title         string     `gorm:"column:title"`
	OriginalTitle *string    `gorm:"column:original_title"`
	Summary       *string    `gorm:"column:summary"`
	SourceName    string     `gorm:"column:source_name"`
	URL           string     `gorm:"column:url"`
	PublishedAt   *time.Time `gorm:"column:published_at"`
	DiscoveredAt  time.Time  `gorm:"column:discovered_at"`
	Category      *string    `gorm:"column:category"`
	Score         *float64   `gorm:"column:score"`
	Selected      bool       `gorm:"column:selected"`
	Reason        *string    `gorm:"column:reason"`
	SortAt        time.Time  `gorm:"column:sort_at"`
}

type itemCursor struct {
	At      int64
	ID      string
	Binding string
}

type itemRawCursor struct {
	At      json.RawMessage `json:"a"`
	ID      json.RawMessage `json:"i"`
	Binding json.RawMessage `json:"c"`
}

type SearchBusyError struct{ RetryAfter int }

func (e SearchBusyError) Error() string { return "search capacity exhausted" }

var publicSearchSlots = make(chan struct{}, 4)
var publicSearchWaiters atomic.Int32

func (r *GormRepository) V1Items(ctx context.Context, query V1ItemsQuery, now time.Time, after *time.Time, afterID string, terms []string) ([]V1ItemRecord, error) {
	orderBy := "coalesce(p.published_at, p.discovered_at)"
	if query.By == "timeline" {
		if query.Mode == "selected" {
			orderBy = "p.sort_at"
		} else {
			orderBy = "p.timeline_at"
		}
	}
	window := 7 * 24 * time.Hour
	if query.Window == "24h" {
		window = 24 * time.Hour
	}
	windowStart := now.Add(-window)
	selectItems := func(base *gorm.DB) ([]V1ItemRecord, error) {
		db := base.Table("publications AS p").
			Select(`p.article_id AS id, p.title, p.original_title, p.summary, s.name AS source_name, p.url,
				p.published_at, p.discovered_at, p.category, p.score, (p.selected AND p.seat) AS selected, p.reason,
				` + orderBy + ` AS sort_at`).
			Joins("JOIN sources AS s ON s.id = p.source_id")
		if query.Mode == "selected" {
			db = db.Where("p.visibility = 'public' AND p.selected AND p.seat AND p.visible_after <= ?", now)
		} else {
			db = db.Where("p.visibility = 'public' AND p.eligible AND (NOT p.selected OR p.visible_after <= ?)", now)
		}
		if len(query.CategoryKeys) > 0 {
			db = db.Where("p.category IN ?", query.CategoryKeys)
		} else if query.Category != nil {
			db = db.Where("p.category = ?", *query.Category)
		}
		if query.Search != nil {
			for _, term := range terms {
				pattern := "%" + term + "%"
				db = db.Where(`(p.search_text LIKE ? OR EXISTS (
					SELECT 1 FROM pool_search ps WHERE ps.article_id = p.article_id AND ps.body LIKE ? OFFSET 0))`, pattern, pattern)
			}
		}
		db = db.Where(orderBy+" >= ? AND "+orderBy+" <= ?", windowStart, now)
		if after != nil {
			db = db.Where("("+orderBy+", p.article_id) < (?, ?)", *after, afterID)
		}
		var rows []V1ItemRecord
		err := db.Order(orderBy + " DESC").Order("p.article_id DESC").Limit(query.Limit + 1).Scan(&rows).Error
		return rows, err
	}
	if len(terms) == 0 {
		return selectItems(r.db.WithContext(ctx))
	}
	var rows []V1ItemRecord
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET LOCAL plan_cache_mode = force_custom_plan").Error; err != nil {
			return err
		}
		var selectErr error
		rows, selectErr = selectItems(tx)
		return selectErr
	})
	return rows, err
}

func (s *Service) V1Items(ctx context.Context, query V1ItemsQuery, now time.Time, siteURL string) (V1ItemsResponse, error) {
	repository, ok := s.repository.(V1ItemsRepository)
	if !ok {
		return V1ItemsResponse{}, errors.New("v1 items repository is unavailable")
	}
	if query.Category != nil && len(query.CategoryKeys) == 0 {
		if members := s.publicCategoryMembers[*query.Category]; len(members) > 0 {
			query.CategoryKeys = append([]string(nil), members...)
		}
	}
	now = now.UTC().Truncate(time.Millisecond)
	window := 7 * 24 * time.Hour
	if query.Window == "24h" {
		window = 24 * time.Hour
	}
	binding := v1ItemsBinding(query)
	var afterTime *time.Time
	afterID := ""
	if query.Cursor != nil {
		cursor, err := decodeItemCursor(*query.Cursor)
		if err != nil || cursor.Binding != binding || !validJSDateMillis(cursor.At) || !itemCursorIDPattern.MatchString(cursor.ID) {
			return V1ItemsResponse{}, InvalidCursorError{}
		}
		anchor := time.UnixMilli(cursor.At).UTC()
		if anchor.Before(now.Add(-window)) {
			return V1ItemsResponse{}, InvalidCursorError{}
		}
		afterTime, afterID = &anchor, cursor.ID
	}
	var terms []string
	if query.Search != nil {
		release, err := acquirePublicSearch(ctx)
		if err != nil {
			return V1ItemsResponse{}, err
		}
		defer release()
		terms = itemSearchTerms(*query.Search)
	}
	rows, err := repository.V1Items(ctx, query, now, afterTime, afterID, terms)
	if err != nil {
		return V1ItemsResponse{}, err
	}
	page := rows
	hasMore := len(rows) > query.Limit
	if hasMore {
		page = rows[:query.Limit]
	}
	items := make([]V1Item, 0, len(page))
	for _, row := range page {
		var published *string
		if row.PublishedAt != nil {
			value := formatV1Time(*row.PublishedAt)
			published = &value
		}
		var score *int
		if row.Score != nil {
			value := int(math.Round(*row.Score))
			score = &value
		}
		reason := row.Reason
		if !row.Selected {
			reason = nil
		}
		items = append(items, V1Item{
			ID: row.ID, Title: row.Title, OriginalTitle: row.OriginalTitle, Summary: row.Summary,
			Source: V1ItemSource{Name: row.SourceName}, Links: V1ItemLinks{AIHot: strings.TrimRight(siteURL, "/") + "/items/" + row.ID, Original: row.URL},
			PublishedAt: published, DiscoveredAt: formatV1Time(row.DiscoveredAt), Category: row.Category,
			Score: score, Selected: row.Selected, Reason: reason,
		})
	}
	var nextCursor *string
	if hasMore && len(page) > 0 {
		last := page[len(page)-1]
		token := encodeItemCursor(itemCursor{At: last.SortAt.UnixMilli(), ID: last.ID, Binding: binding})
		nextCursor = &token
	}
	ordering := "timelineDesc"
	if query.By == "published" {
		ordering = "publishedAtDesc"
	}
	return V1ItemsResponse{
		SchemaVersion: 1,
		Query:         V1ItemsQueryResult{Mode: query.Mode, Category: query.Category, Window: query.Window, Search: query.Search, By: query.By, Ordering: ordering},
		Items:         items,
		Page:          V1ItemsPage{Count: len(items), HasMore: hasMore, NextCursor: nextCursor},
	}, nil
}

func itemSearchTerms(query string) []string {
	parts := strings.FieldsFunc(strings.ToLower(query), func(value rune) bool { return unicode.IsSpace(value) || value == '\uFEFF' })
	if len(parts) > 6 {
		parts = parts[:6]
	}
	for index, part := range parts {
		parts[index] = strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(part)
	}
	return parts
}

func v1ItemsBinding(query V1ItemsQuery) string {
	identity := struct {
		By       string  `json:"b"`
		Category *string `json:"c"`
		Mode     string  `json:"m"`
		Search   *string `json:"q"`
		Window   string  `json:"w"`
	}{query.By, query.Category, query.Mode, query.Search, query.Window}
	var encoded strings.Builder
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(identity)
	stable := strings.TrimSuffix(encoded.String(), "\n")
	stable = strings.ReplaceAll(stable, `\u2028`, "\u2028")
	stable = strings.ReplaceAll(stable, `\u2029`, "\u2029")
	sum := sha256.Sum256([]byte(stable))
	return hex.EncodeToString(sum[:])[:12]
}

func decodeItemCursor(cursor string) (itemCursor, error) {
	var empty itemCursor
	if !strings.HasPrefix(cursor, itemCursorPrefix+".") {
		return empty, InvalidCursorError{}
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(cursor, itemCursorPrefix+"."))
	if err != nil {
		return empty, InvalidCursorError{}
	}
	var raw itemRawCursor
	if err := json.Unmarshal(decoded, &raw); err != nil || len(raw.At) == 0 || string(raw.At) == "null" || len(raw.ID) == 0 || string(raw.ID) == "null" || len(raw.Binding) == 0 || string(raw.Binding) == "null" {
		return empty, InvalidCursorError{}
	}
	var at json.Number
	var cursorValue itemCursor
	if json.Unmarshal(raw.At, &at) != nil || json.Unmarshal(raw.ID, &cursorValue.ID) != nil || json.Unmarshal(raw.Binding, &cursorValue.Binding) != nil {
		return empty, InvalidCursorError{}
	}
	parsedAt, err := strconv.ParseFloat(string(at), 64)
	if err != nil || math.Trunc(parsedAt) != parsedAt || parsedAt < -float64(maxSafeInteger) || parsedAt > float64(maxSafeInteger) {
		return empty, InvalidCursorError{}
	}
	cursorValue.At = int64(parsedAt)
	return cursorValue, nil
}

func encodeItemCursor(cursor itemCursor) string {
	encoded, _ := json.Marshal(struct {
		At      int64  `json:"a"`
		ID      string `json:"i"`
		Binding string `json:"c"`
	}{cursor.At, cursor.ID, cursor.Binding})
	return itemCursorPrefix + "." + base64.RawURLEncoding.EncodeToString(encoded)
}

func validJSDateMillis(value int64) bool {
	const maxDateMillis = int64(8_640_000_000_000_000)
	return value >= -maxDateMillis && value <= maxDateMillis && value <= maxSafeInteger && value >= -maxSafeInteger
}

func formatV1Time(value time.Time) string {
	return value.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
}

func acquirePublicSearch(ctx context.Context) (func(), error) {
	select {
	case publicSearchSlots <- struct{}{}:
		return func() { <-publicSearchSlots }, nil
	default:
	}
	if publicSearchWaiters.Add(1) > 8 {
		publicSearchWaiters.Add(-1)
		return nil, SearchBusyError{RetryAfter: 5}
	}
	defer publicSearchWaiters.Add(-1)
	select {
	case publicSearchSlots <- struct{}{}:
		return func() { <-publicSearchSlots }, nil
	default:
	}
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case publicSearchSlots <- struct{}{}:
		return func() { <-publicSearchSlots }, nil
	case <-timer.C:
		return nil, SearchBusyError{RetryAfter: 5}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

var _ V1ItemsRepository = (*GormRepository)(nil)
