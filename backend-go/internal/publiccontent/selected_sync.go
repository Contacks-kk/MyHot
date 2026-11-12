package publiccontent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const syncCursorPrefix = "ax1"
const maxSafeInteger = int64(1<<53 - 1)

var syncArticleIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)

type InvalidCursorError struct{}

func (InvalidCursorError) Error() string { return "invalid cursor" }

type SnapshotRequiredError struct{}

func (SnapshotRequiredError) Error() string { return "snapshot required" }

type SelectedSnapshotQuery struct {
	Fields *string
	Limit  int
	Page   *string
}

type SelectedSnapshotResponse struct {
	SchemaVersion int               `json:"schemaVersion"`
	AsOf          string            `json:"asOf"`
	Fields        string            `json:"fields"`
	Cursor        string            `json:"cursor"`
	Count         int               `json:"count"`
	HasMore       bool              `json:"hasMore"`
	NextPage      *string           `json:"nextPage"`
	Items         []json.RawMessage `json:"items"`
}

type SelectedSnapshotRecord struct {
	ArticleID string          `gorm:"column:article_id"`
	Payload   json.RawMessage `gorm:"column:payload"`
}

type SelectedChangeRecord struct {
	Sequence  int64           `gorm:"column:seq"`
	ArticleID string          `gorm:"column:article_id"`
	Operation string          `gorm:"column:op"`
	ChangedAt time.Time       `gorm:"column:changed_at"`
	Payload   json.RawMessage `gorm:"column:payload"`
}

type SelectedChange struct {
	Operation string          `json:"op"`
	ChangedAt string          `json:"changedAt"`
	ID        string          `json:"id,omitempty"`
	Item      json.RawMessage `json:"item,omitempty"`
}

type SelectedChangesResponse struct {
	SchemaVersion int              `json:"schemaVersion"`
	Fields        string           `json:"fields"`
	Cursor        string           `json:"cursor"`
	Count         int              `json:"count"`
	HasMore       bool             `json:"hasMore"`
	Changes       []SelectedChange `json:"changes"`
}

type minimalSelectedItem struct {
	ID           json.RawMessage `json:"id"`
	Title        json.RawMessage `json:"title"`
	Source       json.RawMessage `json:"source"`
	PublishedAt  json.RawMessage `json:"publishedAt"`
	DiscoveredAt json.RawMessage `json:"discoveredAt"`
	Category     json.RawMessage `json:"category"`
	Score        json.RawMessage `json:"score"`
	Selected     json.RawMessage `json:"selected"`
	Links        struct {
		AIHot json.RawMessage `json:"aihot"`
	} `json:"links"`
}

type syncCursor struct {
	Kind   string
	Epoch  string
	Mark   int64
	Fields string
	After  string
	AsOf   string
}

type rawCursor struct {
	Kind   json.RawMessage `json:"k"`
	Epoch  json.RawMessage `json:"e"`
	Mark   json.RawMessage `json:"w"`
	Fields json.RawMessage `json:"f"`
	After  json.RawMessage `json:"a"`
	AsOf   json.RawMessage `json:"t"`
}

func (r *GormRepository) LedgerEpoch(ctx context.Context) (string, error) {
	var value []byte
	result := r.db.WithContext(ctx).Raw("SELECT value FROM settings WHERE key = ?", "selected_ledger_epoch").Scan(&value)
	if result.Error != nil {
		return "", result.Error
	}
	if result.RowsAffected == 0 {
		var nonce [6]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return "", err
		}
		encoded, err := json.Marshal(map[string]string{"epoch": base64.RawURLEncoding.EncodeToString(nonce[:])})
		if err != nil {
			return "", err
		}
		if err := r.db.WithContext(ctx).Exec("INSERT INTO settings(key,value) VALUES(?,?::jsonb) ON CONFLICT (key) DO NOTHING", "selected_ledger_epoch", string(encoded)).Error; err != nil {
			return "", err
		}
		result = r.db.WithContext(ctx).Raw("SELECT value FROM settings WHERE key = ?", "selected_ledger_epoch").Scan(&value)
		if result.Error != nil {
			return "", result.Error
		}
	}
	var setting struct {
		Epoch string `json:"epoch"`
	}
	if err := json.Unmarshal(value, &setting); err != nil || setting.Epoch == "" {
		return "", errors.New("selected ledger epoch is invalid")
	}
	return setting.Epoch, nil
}

func (r *GormRepository) LedgerWatermark(ctx context.Context) (int64, error) {
	var watermark int64
	err := r.db.WithContext(ctx).Raw("SELECT coalesce(max(seq),0) FROM selected_ledger").Scan(&watermark).Error
	return watermark, err
}

func (r *GormRepository) SelectedSnapshot(ctx context.Context, limit int, watermark int64, after string, minimal bool) ([]SelectedSnapshotRecord, error) {
	payload := "latest.payload"
	if minimal {
		payload = "(latest.payload - ARRAY['originalTitle','summary','reason','attribution']::text[]) #- '{links,original}'"
	}
	query := fmt.Sprintf(`SELECT latest.article_id, %s AS payload FROM (
		SELECT DISTINCT ON (article_id) article_id, op, payload FROM selected_ledger
		WHERE seq <= ? AND article_id > ? ORDER BY article_id, seq DESC
	) latest JOIN selected_state st ON st.article_id = latest.article_id AND st.in_set
	WHERE latest.op = 'upsert' ORDER BY latest.article_id LIMIT ?`, payload)
	var rows []SelectedSnapshotRecord
	err := r.db.WithContext(ctx).Raw(query, watermark, after, limit+1).Scan(&rows).Error
	return rows, err
}

func (r *GormRepository) SelectedChanges(ctx context.Context, limit int, after, watermark int64, now time.Time, minimal bool) ([]SelectedChangeRecord, error) {
	payload := "l.payload"
	if minimal {
		payload = "(l.payload - ARRAY['originalTitle','summary','reason','attribution']::text[]) #- '{links,original}'"
	}
	query := fmt.Sprintf(`SELECT l.seq, l.article_id,
		CASE WHEN p.article_id IS NULL THEN 'remove' ELSE l.op END AS op, l.changed_at,
		CASE WHEN p.article_id IS NOT NULL THEN %s END AS payload
	FROM selected_ledger l
	LEFT JOIN publications p ON p.article_id = l.article_id
		AND p.visibility = 'public' AND p.eligible AND (NOT p.selected OR p.visible_after <= ?)
	WHERE l.seq > ? AND l.seq <= ? ORDER BY l.seq LIMIT ?`, payload)
	var rows []SelectedChangeRecord
	err := r.db.WithContext(ctx).Raw(query, now, after, watermark, limit+1).Scan(&rows).Error
	return rows, err
}

func (s *Service) SelectedSnapshot(ctx context.Context, query SelectedSnapshotQuery, now time.Time) (SelectedSnapshotResponse, error) {
	repository, ok := s.repository.(SelectedSyncRepository)
	if !ok {
		return SelectedSnapshotResponse{}, errors.New("selected sync repository is unavailable")
	}
	epoch, err := repository.LedgerEpoch(ctx)
	if err != nil {
		return SelectedSnapshotResponse{}, err
	}
	fields := "default"
	if query.Fields != nil {
		fields = *query.Fields
	}
	var watermark int64
	after := ""
	asOf := now.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
	if query.Page != nil {
		page, err := decodeSyncCursor(*query.Page)
		if err != nil || page.Kind != "page" || page.Epoch != epoch || (page.Fields != "default" && page.Fields != "minimal") || !validWatermark(page.Mark) || !syncArticleIDPattern.MatchString(page.After) || !validDateString(page.AsOf) {
			return SelectedSnapshotResponse{}, SnapshotRequiredError{}
		}
		latest, err := repository.LedgerWatermark(ctx)
		if err != nil {
			return SelectedSnapshotResponse{}, err
		}
		if page.Mark > latest {
			return SelectedSnapshotResponse{}, SnapshotRequiredError{}
		}
		if query.Fields != nil && page.Fields != *query.Fields {
			return SelectedSnapshotResponse{}, InvalidCursorError{}
		}
		fields, watermark, after, asOf = page.Fields, page.Mark, page.After, page.AsOf
	} else {
		watermark, err = repository.LedgerWatermark(ctx)
		if err != nil {
			return SelectedSnapshotResponse{}, err
		}
	}
	rows, err := repository.SelectedSnapshot(ctx, query.Limit, watermark, after, fields == "minimal")
	if err != nil {
		return SelectedSnapshotResponse{}, err
	}
	page := rows
	hasMore := len(rows) > query.Limit
	if hasMore {
		page = rows[:query.Limit]
	}
	items := make([]json.RawMessage, 0, len(page))
	for _, row := range page {
		item := row.Payload
		if fields == "minimal" {
			item, err = minimalSelectedPayload(row.Payload)
			if err != nil {
				return SelectedSnapshotResponse{}, err
			}
		}
		items = append(items, item)
	}
	cursor := encodeSyncCursor(syncCursor{Kind: "sync", Epoch: epoch, Mark: watermark, Fields: fields})
	var nextPage *string
	if hasMore && len(page) > 0 {
		token := encodeSyncCursor(syncCursor{Kind: "page", Epoch: epoch, Mark: watermark, Fields: fields, After: page[len(page)-1].ArticleID, AsOf: asOf})
		nextPage = &token
	}
	return SelectedSnapshotResponse{SchemaVersion: 1, AsOf: asOf, Fields: fields, Cursor: cursor, Count: len(items), HasMore: hasMore, NextPage: nextPage, Items: items}, nil
}

func (s *Service) SelectedChanges(ctx context.Context, cursor string, limit int, now time.Time) (SelectedChangesResponse, error) {
	repository, ok := s.repository.(SelectedSyncRepository)
	if !ok {
		return SelectedChangesResponse{}, errors.New("selected sync repository is unavailable")
	}
	epoch, err := repository.LedgerEpoch(ctx)
	if err != nil {
		return SelectedChangesResponse{}, err
	}
	mark, err := repository.LedgerWatermark(ctx)
	if err != nil {
		return SelectedChangesResponse{}, err
	}
	current, err := decodeSyncCursor(cursor)
	if err != nil || current.Kind != "sync" || current.Epoch != epoch || !validWatermark(current.Mark) || (current.Fields != "default" && current.Fields != "minimal") || current.Mark > mark {
		return SelectedChangesResponse{}, SnapshotRequiredError{}
	}
	rows, err := repository.SelectedChanges(ctx, limit, current.Mark, mark, now, current.Fields == "minimal")
	if err != nil {
		return SelectedChangesResponse{}, err
	}
	page := rows
	hasMore := len(rows) > limit
	if hasMore {
		page = rows[:limit]
	}
	changes := make([]SelectedChange, 0, len(page))
	for _, row := range page {
		changedAt := row.ChangedAt.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
		if row.Operation == "remove" {
			changes = append(changes, SelectedChange{Operation: "remove", ChangedAt: changedAt, ID: row.ArticleID})
		} else {
			item := row.Payload
			if current.Fields == "minimal" {
				item, err = minimalSelectedPayload(row.Payload)
				if err != nil {
					return SelectedChangesResponse{}, err
				}
			}
			changes = append(changes, SelectedChange{Operation: "upsert", ChangedAt: changedAt, Item: item})
		}
	}
	next := current.Mark
	if len(page) > 0 {
		next = page[len(page)-1].Sequence
	}
	return SelectedChangesResponse{
		SchemaVersion: 1, Fields: current.Fields,
		Cursor: encodeSyncCursor(syncCursor{Kind: "sync", Epoch: epoch, Mark: next, Fields: current.Fields}),
		Count:  len(changes), HasMore: hasMore, Changes: changes,
	}, nil
}

func decodeSyncCursor(cursor string) (syncCursor, error) {
	var empty syncCursor
	if !strings.HasPrefix(cursor, syncCursorPrefix+".") {
		return empty, InvalidCursorError{}
	}
	encoded := strings.TrimPrefix(cursor, syncCursorPrefix+".")
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return empty, InvalidCursorError{}
	}
	var raw rawCursor
	if err := json.Unmarshal(decoded, &raw); err != nil {
		return empty, InvalidCursorError{}
	}
	if len(raw.Kind) == 0 || string(raw.Kind) == "null" || len(raw.Epoch) == 0 || string(raw.Epoch) == "null" ||
		len(raw.Fields) == 0 || string(raw.Fields) == "null" || len(raw.Mark) == 0 || string(raw.Mark) == "null" {
		return empty, InvalidCursorError{}
	}
	var value syncCursor
	if err := json.Unmarshal(raw.Kind, &value.Kind); err != nil {
		return empty, InvalidCursorError{}
	}
	if err := json.Unmarshal(raw.Epoch, &value.Epoch); err != nil {
		return empty, InvalidCursorError{}
	}
	if err := json.Unmarshal(raw.Fields, &value.Fields); err != nil {
		return empty, InvalidCursorError{}
	}
	var mark json.Number
	if err := json.Unmarshal(raw.Mark, &mark); err != nil {
		return empty, InvalidCursorError{}
	}
	parsedMark, err := strconv.ParseFloat(string(mark), 64)
	if err != nil || math.Trunc(parsedMark) != parsedMark || parsedMark < 0 || parsedMark > float64(maxSafeInteger) {
		return empty, InvalidCursorError{}
	}
	value.Mark = int64(parsedMark)
	if len(raw.After) > 0 {
		if err := json.Unmarshal(raw.After, &value.After); err != nil {
			return empty, InvalidCursorError{}
		}
	}
	if len(raw.AsOf) > 0 {
		if err := json.Unmarshal(raw.AsOf, &value.AsOf); err != nil {
			return empty, InvalidCursorError{}
		}
	}
	return value, nil
}

func encodeSyncCursor(cursor syncCursor) string {
	var encoded []byte
	switch cursor.Kind {
	case "sync":
		encoded, _ = json.Marshal(struct {
			Kind   string `json:"k"`
			Epoch  string `json:"e"`
			Mark   int64  `json:"w"`
			Fields string `json:"f"`
		}{cursor.Kind, cursor.Epoch, cursor.Mark, cursor.Fields})
	case "page":
		encoded, _ = json.Marshal(struct {
			Kind   string `json:"k"`
			Epoch  string `json:"e"`
			Mark   int64  `json:"w"`
			Fields string `json:"f"`
			After  string `json:"a"`
			AsOf   string `json:"t"`
		}{cursor.Kind, cursor.Epoch, cursor.Mark, cursor.Fields, cursor.After, cursor.AsOf})
	}
	return syncCursorPrefix + "." + base64.RawURLEncoding.EncodeToString(encoded)
}

func validWatermark(value int64) bool { return value >= 0 && value <= maxSafeInteger }

func validDateString(value string) bool {
	if value == "" {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func minimalSelectedPayload(payload json.RawMessage) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return nil, err
	}
	var links map[string]json.RawMessage
	if err := json.Unmarshal(fields["links"], &links); err != nil {
		return nil, err
	}
	var item minimalSelectedItem
	item.ID = fields["id"]
	item.Title = fields["title"]
	item.Source = fields["source"]
	item.PublishedAt = fields["publishedAt"]
	item.DiscoveredAt = fields["discoveredAt"]
	item.Category = fields["category"]
	item.Score = fields["score"]
	item.Selected = fields["selected"]
	item.Links.AIHot = links["aihot"]
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(item); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(output.Bytes(), []byte("\n")), nil
}
