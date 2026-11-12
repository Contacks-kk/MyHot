package heatsnapshot

import (
	"context"
	"time"
)

const (
	windowHours   = 48
	halfLifeHours = 24
)

type CommunityFeeds struct {
	Dev []string
	HN  []string
}

type SourceClock struct {
	ID           string
	LastOKAt     *time.Time
	IntervalMins int
}

type HeatRow struct {
	StoryID                int64      `gorm:"column:story_id"`
	PublicID               string     `gorm:"column:public_id"`
	Title                  string     `gorm:"column:title"`
	FirstReportAt          *time.Time `gorm:"column:first_report_at"`
	LatestAt               *time.Time `gorm:"column:latest_at"`
	Participants           int        `gorm:"column:participants"`
	Heat                   float64    `gorm:"column:heat"`
	HeatPrevious           float64    `gorm:"column:heat_prev"`
	HeatComparable         float64    `gorm:"column:heat_obs"`
	HeatPreviousComparable float64    `gorm:"column:heat_prev_obs"`
	BehindParticipants     int        `gorm:"column:behind_participants"`
	Uncomparable           int        `gorm:"column:uncomparable"`
	Recent6Hours           int        `gorm:"column:recent6h"`
	EditorialParticipants  int        `gorm:"column:editorial_participants"`
	SignalParticipants     int        `gorm:"column:signal_participants"`
}

type Participant struct {
	Name string
	Kind string
	Tier string
	At   time.Time
}

type StaleHour struct {
	Hour     time.Time
	StoryIDs []int64
}

type Repository interface {
	SourceClocks(context.Context) ([]SourceClock, error)
	HeatRows(context.Context, time.Time, []string, []int64, CommunityFeeds) ([]HeatRow, error)
	Participants(context.Context, int64, time.Time, CommunityFeeds) ([]Participant, error)
	StaleHours(context.Context, time.Time, time.Time) ([]StaleHour, error)
	SaveHeat(context.Context, time.Time, []HeatRow) error
}

type Result struct {
	Stories  int `json:"stories"`
	Repaired int `json:"repaired"`
}

type Service struct {
	repository Repository
	feeds      CommunityFeeds
}

func NewService(repository Repository, feeds CommunityFeeds) *Service {
	if feeds.Dev == nil {
		feeds.Dev = []string{}
	}
	if feeds.HN == nil {
		feeds.HN = []string{}
	}
	return &Service{repository: repository, feeds: feeds}
}

func (s *Service) Snapshot(ctx context.Context, at time.Time) (Result, error) {
	hour := at.UTC().Truncate(time.Hour)
	clocks, err := s.repository.SourceClocks(ctx)
	if err != nil {
		return Result{}, err
	}
	behind := behindSources(clocks, hour, true)
	rows, err := s.repository.HeatRows(ctx, hour, behind, nil, s.feeds)
	if err != nil {
		return Result{}, err
	}
	if err := s.repository.SaveHeat(ctx, hour, rows); err != nil {
		return Result{}, err
	}
	result := Result{Stories: len(rows)}
	staleHours, err := s.repository.StaleHours(ctx, hour.Add(-time.Duration(windowHours)*time.Hour), hour)
	if err != nil {
		return Result{}, err
	}
	for _, stale := range staleHours {
		repairedRows, err := s.repository.HeatRows(ctx, stale.Hour, behindSources(clocks, stale.Hour, false), stale.StoryIDs, s.feeds)
		if err != nil {
			return Result{}, err
		}
		complete := make([]HeatRow, 0, len(repairedRows))
		for _, row := range repairedRows {
			if row.BehindParticipants == 0 {
				complete = append(complete, row)
			}
		}
		if err := s.repository.SaveHeat(ctx, stale.Hour, complete); err != nil {
			return Result{}, err
		}
		result.Repaired += len(complete)
	}
	return result, nil
}

func (s *Service) HeatAt(ctx context.Context, at time.Time) ([]HeatRow, error) {
	at = at.UTC()
	clocks, err := s.repository.SourceClocks(ctx)
	if err != nil {
		return nil, err
	}
	return s.repository.HeatRows(ctx, at, behindSources(clocks, at, true), nil, s.feeds)
}

func (s *Service) Participants(ctx context.Context, storyID int64, at time.Time) ([]Participant, error) {
	return s.repository.Participants(ctx, storyID, at, s.feeds)
}

func behindSources(clocks []SourceClock, at time.Time, grace bool) []string {
	behind := make([]string, 0)
	for _, clock := range clocks {
		if clock.LastOKAt == nil {
			behind = append(behind, clock.ID)
			continue
		}
		threshold := at
		if grace {
			graceMinutes := clock.IntervalMins * 3
			if graceMinutes < 90 {
				graceMinutes = 90
			}
			threshold = threshold.Add(-time.Duration(graceMinutes) * time.Minute)
		}
		if clock.LastOKAt.UnixMilli() < threshold.UnixMilli() {
			behind = append(behind, clock.ID)
		}
	}
	return behind
}

func heatIndex(heat float64) float64 {
	return float64(int64(heat*100+0.5)) / 10
}
