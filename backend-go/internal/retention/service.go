package retention

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const JobName = "ops.retention"
const StuckRunGrace = 3 * time.Minute

var ErrInvalidRunDate = errors.New("invalid retention run date")

type Config struct {
	DataDir              string
	ScreenshotsForwarded bool
}

type Counts struct {
	DeletedLeases      int64 `json:"deletedLeases"`
	DeletedJobRuns     int64 `json:"deletedJobRuns"`
	DeletedScreenshots int   `json:"deletedScreenshots"`
	PrunedCache        int   `json:"prunedCache"`
}

type Repository interface {
	HasRun(context.Context, string, time.Time, time.Time) (bool, error)
	BeginRun(context.Context, string, time.Time, time.Time) (int64, bool, error)
	CleanupDatabase(context.Context, time.Time) (int64, int64, error)
	CompleteRun(context.Context, int64, json.RawMessage) error
	FailRun(context.Context, int64, string) error
}

type Service struct {
	repository Repository
	cfg        Config
	location   *time.Location
}

func NewService(repository Repository, cfg Config) *Service {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		location = time.FixedZone("Asia/Shanghai", 8*60*60)
	}
	return &Service{repository: repository, cfg: cfg, location: location}
}

func (s *Service) Due(ctx context.Context, now time.Time) (string, bool, error) {
	local := now.In(s.location)
	if local.Hour() < 3 || (local.Hour() == 3 && local.Minute() < 30) {
		return "", false, nil
	}
	date := local.Format("2006-01-02")
	start := dayStart(local)
	done, err := s.repository.HasRun(ctx, JobName, start, start.AddDate(0, 0, 1))
	return date, !done, err
}

// Run performs one River attempt and records it for the admin runs page; River may retry it once after failure.
func (s *Service) Run(ctx context.Context, runDate string, now time.Time) error {
	start, err := time.ParseInLocation("2006-01-02", runDate, s.location)
	if err != nil || start.Format("2006-01-02") != runDate {
		return ErrInvalidRunDate
	}
	runID, started, err := s.repository.BeginRun(ctx, JobName, start, start.AddDate(0, 0, 1))
	if err != nil || !started {
		return err
	}
	deletedLeases, deletedRuns, runErr := s.repository.CleanupDatabase(ctx, now)
	if runErr == nil {
		counts := Counts{DeletedLeases: deletedLeases, DeletedJobRuns: deletedRuns}
		if s.cfg.ScreenshotsForwarded {
			counts.DeletedScreenshots = removeOlderThan(filepath.Join(s.cfg.DataDir, "feedback-screenshots"), 8*24*time.Hour, now)
		}
		counts.PrunedCache = removeOlderThan(filepath.Join(s.cfg.DataDir, "imgcache"), 30*24*time.Hour, now) +
			removeOlderThan(filepath.Join(s.cfg.DataDir, "ogcache"), 30*24*time.Hour, now)
		detail, err := json.Marshal(counts)
		if err != nil {
			return err
		}
		if err := s.repository.CompleteRun(ctx, runID, detail); err != nil {
			return err
		}
		return nil
	}
	if recordErr := s.repository.FailRun(ctx, runID, runErr.Error()); recordErr != nil {
		return fmt.Errorf("retention failed: %v; record failure: %w", runErr, recordErr)
	}
	return runErr
}

func dayStart(local time.Time) time.Time {
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
}

func removeOlderThan(root string, maxAge time.Duration, now time.Time) int {
	removed := 0
	var walk func(string)
	walk = func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, entry := range entries {
			file := filepath.Join(dir, entry.Name())
			if entry.IsDir() {
				walk(file)
				continue
			}
			info, err := os.Stat(file)
			if err != nil || now.Sub(info.ModTime()) <= maxAge {
				continue
			}
			_ = os.Remove(file)
			removed++
		}
	}
	walk(root)
	return removed
}

func truncateError(err error) string {
	message := []rune(err.Error())
	if len(message) > 4000 {
		return string(message[:4000])
	}
	return string(message)
}
