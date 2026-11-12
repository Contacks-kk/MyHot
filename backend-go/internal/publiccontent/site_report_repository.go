package publiccontent

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type SiteReportCoverRecord struct {
	URL    string
	Width  *float64
	Height *float64
}

type SiteReportDetailRepository interface {
	V1ReportRepository
	SiteReportIssueNumber(context.Context, string, string) (int, error)
	SiteReportNeighbors(context.Context, string, string) (*string, *string, error)
	SiteReportCover(context.Context, string, time.Time) (*SiteReportCoverRecord, error)
}

func (r *GormRepository) SiteReportIssueNumber(ctx context.Context, kind, key string) (int, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&ReportModel{}).Where("kind = ? AND key <= ?", kind, key).Count(&count).Error
	return int(count), err
}

func (r *GormRepository) SiteReportNeighbors(ctx context.Context, kind, key string) (*string, *string, error) {
	var row struct {
		Previous *string `gorm:"column:previous"`
		Next     *string `gorm:"column:next"`
	}
	err := r.db.WithContext(ctx).Raw(`
		SELECT (SELECT key FROM reports WHERE kind = ? AND key < ? ORDER BY key DESC LIMIT 1) AS previous,
		       (SELECT key FROM reports WHERE kind = ? AND key > ? ORDER BY key ASC LIMIT 1) AS next`, kind, key, kind, key).Scan(&row).Error
	return row.Previous, row.Next, err
}

func (r *GormRepository) SiteReportCover(ctx context.Context, itemID string, now time.Time) (*SiteReportCoverRecord, error) {
	var row struct {
		Media json.RawMessage `gorm:"column:media"`
	}
	err := r.db.WithContext(ctx).Raw(`
		SELECT image.media
		FROM publications p
		JOIN articles a ON a.id = p.article_id
		CROSS JOIN LATERAL (
			SELECT value AS media
			FROM jsonb_array_elements(coalesce(a.media, '[]'::jsonb)) value
			WHERE value->>'kind' = 'image' AND coalesce((value->>'width')::numeric, 800) >= 480
			LIMIT 1
		) image
		WHERE (p.article_id = ? OR p.story_id = (SELECT story_id FROM publications WHERE article_id = ?))
		  AND p.visibility = 'public' AND p.eligible AND (NOT p.selected OR p.visible_after <= ?)
		  AND p.body_mode <> 'summary'
		ORDER BY (p.article_id = ?) DESC, p.first_party DESC, coalesce(p.score, 0) DESC, p.article_id
		LIMIT 1`, itemID, itemID, now, itemID).Scan(&row).Error
	if err != nil {
		return nil, err
	}
	if len(row.Media) == 0 {
		return nil, nil
	}
	var media struct {
		URL    string   `json:"url"`
		Width  *float64 `json:"width"`
		Height *float64 `json:"height"`
	}
	if err := json.Unmarshal(row.Media, &media); err != nil {
		return nil, errors.New("invalid public report cover")
	}
	if media.URL == "" {
		return nil, nil
	}
	return &SiteReportCoverRecord{URL: media.URL, Width: media.Width, Height: media.Height}, nil
}

var _ SiteReportDetailRepository = (*GormRepository)(nil)
