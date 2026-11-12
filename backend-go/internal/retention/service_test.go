package retention

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type memoryRepository struct {
	runs       map[string]int64
	started    bool
	leases     int64
	jobRuns    int64
	cleanupErr error
	completed  json.RawMessage
	failed     string
}

func (r *memoryRepository) HasRun(_ context.Context, job string, start, _ time.Time) (bool, error) {
	_, ok := r.runs[job+":"+start.Format("2006-01-02")]
	return ok, nil
}

func (r *memoryRepository) BeginRun(_ context.Context, job string, start, _ time.Time) (int64, bool, error) {
	if r.runs == nil {
		r.runs = map[string]int64{}
	}
	key := job + ":" + start.Format("2006-01-02")
	if _, ok := r.runs[key]; ok {
		return 0, false, nil
	}
	r.runs[key] = 1
	return 1, true, nil
}

func (r *memoryRepository) CleanupDatabase(context.Context, time.Time) (int64, int64, error) {
	return r.leases, r.jobRuns, r.cleanupErr
}

func (r *memoryRepository) CompleteRun(_ context.Context, _ int64, detail json.RawMessage) error {
	r.completed = append(json.RawMessage(nil), detail...)
	return nil
}

func (r *memoryRepository) FailRun(_ context.Context, _ int64, message string) error {
	r.failed = message
	return nil
}

func TestDueUsesBeijing0330AndRunsMissedSlotOncePerDay(t *testing.T) {
	repository := &memoryRepository{}
	service := NewService(repository, Config{})
	before := time.Date(2026, 10, 7, 3, 29, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	if _, due, err := service.Due(context.Background(), before); err != nil || due {
		t.Fatalf("before cutoff due=%v err=%v", due, err)
	}
	after := before.Add(time.Minute)
	date, due, err := service.Due(context.Background(), after)
	if err != nil || !due || date != "2026-10-07" {
		t.Fatalf("missed slot should run once after cutoff: date=%q due=%v err=%v", date, due, err)
	}
	repository.runs = map[string]int64{}
	repository.runs[JobName+":"+date] = 1
	if _, due, err := service.Due(context.Background(), after.Add(time.Hour)); err != nil || due {
		t.Fatalf("completed daily task was rescheduled: due=%v err=%v", due, err)
	}
}

func TestRunKeepsFeedbackScreenshotsWithoutConfiguredForwarding(t *testing.T) {
	now := time.Date(2026, 10, 7, 4, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	root := t.TempDir()
	dir := filepath.Join(root, "feedback-screenshots")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	shot := filepath.Join(dir, "old.png")
	if err := os.WriteFile(shot, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(shot, now.Add(-9*24*time.Hour), now.Add(-9*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	repository := &memoryRepository{}
	service := NewService(repository, Config{DataDir: root, ScreenshotsForwarded: false})
	if err := service.Run(context.Background(), "2026-10-07", now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(shot); err != nil {
		t.Fatalf("unforwarded screenshot was deleted: %v", err)
	}
	var result Counts
	if err := json.Unmarshal(repository.completed, &result); err != nil {
		t.Fatal(err)
	}
	if result.DeletedScreenshots != 0 {
		t.Fatalf("reported deleted screenshots=%d", result.DeletedScreenshots)
	}
}

func TestRunRecordsCleanupAndAppliesConfiguredFileRetention(t *testing.T) {
	now := time.Date(2026, 10, 7, 4, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	root := t.TempDir()
	for _, dir := range []string{"feedback-screenshots", "imgcache", "ogcache"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	oldScreenshot := filepath.Join(root, "feedback-screenshots", "old.png")
	oldImage := filepath.Join(root, "imgcache", "old.webp")
	newCard := filepath.Join(root, "ogcache", "new.png")
	for _, file := range []string{oldScreenshot, oldImage, newCard} {
		if err := os.WriteFile(file, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(oldScreenshot, now.Add(-9*24*time.Hour), now.Add(-9*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(oldImage, now.Add(-31*24*time.Hour), now.Add(-31*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	repository := &memoryRepository{leases: 3, jobRuns: 4}
	service := NewService(repository, Config{DataDir: root, ScreenshotsForwarded: true})
	if err := service.Run(context.Background(), "2026-10-07", now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldScreenshot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("forwarded old screenshot remains: %v", err)
	}
	if _, err := os.Stat(oldImage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired image cache remains: %v", err)
	}
	if _, err := os.Stat(newCard); err != nil {
		t.Fatalf("fresh cache was removed: %v", err)
	}
	var result Counts
	if err := json.Unmarshal(repository.completed, &result); err != nil {
		t.Fatal(err)
	}
	if result != (Counts{DeletedLeases: 3, DeletedJobRuns: 4, DeletedScreenshots: 1, PrunedCache: 1}) {
		t.Fatalf("retention result = %#v", result)
	}
}

func TestRunDoesNotDeleteUnforwardedFeedbackScreenshotsAndRecordsFailures(t *testing.T) {
	now := time.Date(2026, 10, 7, 4, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	root := t.TempDir()
	dir := filepath.Join(root, "feedback-screenshots")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	shot := filepath.Join(dir, "old.png")
	if err := os.WriteFile(shot, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(shot, now.Add(-9*24*time.Hour), now.Add(-9*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	repository := &memoryRepository{cleanupErr: errors.New("database unavailable")}
	service := NewService(repository, Config{DataDir: root, ScreenshotsForwarded: false})
	err := service.Run(context.Background(), "2026-10-07", now)
	if err == nil || repository.failed != "database unavailable" || repository.completed != nil {
		t.Fatalf("failed run was not recorded: err=%v failed=%q completed=%s", err, repository.failed, repository.completed)
	}
	if _, err := os.Stat(shot); err != nil {
		t.Fatalf("unforwarded screenshot was deleted: %v", err)
	}
}

func TestRunRejectsInvalidDate(t *testing.T) {
	service := NewService(&memoryRepository{}, Config{})
	if err := service.Run(context.Background(), "2026-02-30", time.Now()); !errors.Is(err, ErrInvalidRunDate) {
		t.Fatalf("invalid date err=%v", err)
	}
}
