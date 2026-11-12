package publiccontent

import (
	"context"
	"time"
)

const groupReportsLimit = 100

type GroupReportsQuery struct {
	FactPublicID string
	Channel      string
	Category     string
	Tag          string
}

type GroupReportRow struct {
	ID         string
	Title      string
	TimelineAt time.Time
	URL        string
	SourceName string
}

type GroupReportsResponse struct {
	FactID  string        `json:"factId"`
	Reports []GroupReport `json:"reports"`
}

type GroupReport struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Source struct {
		Name string `json:"name"`
	} `json:"source"`
	TimelineAt  string `json:"timelineAt"`
	OriginalURL string `json:"originalUrl"`
}

func (s *Service) GroupReports(ctx context.Context, query GroupReportsQuery, now time.Time) (*GroupReportsResponse, error) {
	rows, err := s.repository.GroupReports(ctx, query, now)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	reports := make([]GroupReport, 0, len(rows))
	for _, row := range rows {
		report := GroupReport{
			ID:          row.ID,
			Title:       row.Title,
			TimelineAt:  row.TimelineAt.UTC().Format("2006-01-02T15:04:05.000Z"),
			OriginalURL: row.URL,
		}
		report.Source.Name = publicSourceName(row.SourceName)
		reports = append(reports, report)
	}
	return &GroupReportsResponse{FactID: query.FactPublicID, Reports: reports}, nil
}
