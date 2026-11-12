package heatsnapshot

import (
	"context"
	"reflect"
	"testing"
	"time"
)

type repositoryStub struct {
	clocks     []SourceClock
	rows       []HeatRow
	stale      []StaleHour
	clockCalls int
	rowCalls   []rowCall
	saved      []savedRows
}

type rowCall struct {
	at       time.Time
	behind   []string
	storyIDs []int64
	feeds    CommunityFeeds
}

type savedRows struct {
	hour time.Time
	rows []HeatRow
}

func (s *repositoryStub) SourceClocks(context.Context) ([]SourceClock, error) {
	s.clockCalls++
	return s.clocks, nil
}

func (s *repositoryStub) HeatRows(_ context.Context, at time.Time, behind []string, storyIDs []int64, feeds CommunityFeeds) ([]HeatRow, error) {
	s.rowCalls = append(s.rowCalls, rowCall{at: at, behind: append([]string(nil), behind...), storyIDs: append([]int64(nil), storyIDs...), feeds: feeds})
	if len(storyIDs) > 0 {
		return []HeatRow{{StoryID: storyIDs[0], Heat: 1.234, Participants: 3, EditorialParticipants: 2, BehindParticipants: 0}, {StoryID: storyIDs[1], Heat: .5, Participants: 2, BehindParticipants: 1}}, nil
	}
	return s.rows, nil
}

func (s *repositoryStub) Participants(context.Context, int64, time.Time, CommunityFeeds) ([]Participant, error) {
	return []Participant{}, nil
}

func (s *repositoryStub) StaleHours(context.Context, time.Time, time.Time) ([]StaleHour, error) {
	return s.stale, nil
}

func (s *repositoryStub) SaveHeat(_ context.Context, hour time.Time, rows []HeatRow) error {
	s.saved = append(s.saved, savedRows{hour: hour, rows: append([]HeatRow(nil), rows...)})
	return nil
}

func TestSnapshotFloorsHourAndRepairsOnlyCaughtUpRows(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 8, 14, 2, 30, 0, loc)
	oldHour := now.UTC().Truncate(time.Hour).Add(-time.Hour)
	lastOK := now.Add(-2 * time.Hour)
	repository := &repositoryStub{
		clocks: []SourceClock{{ID: "late", LastOKAt: &lastOK, IntervalMins: 30}},
		rows:   []HeatRow{{StoryID: 11, Heat: 1.234, Participants: 3, EditorialParticipants: 2, BehindParticipants: 1}},
		stale:  []StaleHour{{Hour: oldHour, StoryIDs: []int64{21, 22}}},
	}
	feeds := CommunityFeeds{Dev: []string{"dev-feed"}, HN: []string{"hn-feed"}}
	service := NewService(repository, feeds)

	got, err := service.Snapshot(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stories != 1 || got.Repaired != 1 {
		t.Fatalf("snapshot result = %#v", got)
	}
	currentHour := now.UTC().Truncate(time.Hour)
	if repository.clockCalls != 1 || len(repository.rowCalls) != 2 || !repository.rowCalls[0].at.Equal(currentHour) {
		t.Fatalf("snapshot query calls = %#v", repository.rowCalls)
	}
	if !reflect.DeepEqual(repository.rowCalls[0].behind, []string{"late"}) {
		t.Fatalf("current behind sources = %#v", repository.rowCalls[0].behind)
	}
	if repository.rowCalls[1].at != oldHour || !reflect.DeepEqual(repository.rowCalls[1].storyIDs, []int64{21, 22}) || !reflect.DeepEqual(repository.rowCalls[1].behind, []string{"late"}) {
		t.Fatalf("repair query = %#v", repository.rowCalls[1])
	}
	if repository.rowCalls[0].feeds.Dev[0] != "dev-feed" || repository.rowCalls[0].feeds.HN[0] != "hn-feed" {
		t.Fatalf("community feeds were not passed through: %#v", repository.rowCalls[0].feeds)
	}
	if len(repository.saved) != 2 || len(repository.saved[0].rows) != 1 || len(repository.saved[1].rows) != 1 || repository.saved[1].rows[0].StoryID != 21 {
		t.Fatalf("saved heat rows = %#v", repository.saved)
	}
}

func TestBehindSourceGraceAndHeatIndex(t *testing.T) {
	at := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	lastOK := at.Add(-100 * time.Minute)
	clocks := []SourceClock{
		{ID: "recent", LastOKAt: &lastOK, IntervalMins: 60},
		{ID: "never", IntervalMins: 30},
	}
	if got := behindSources(clocks, at, true); !reflect.DeepEqual(got, []string{"never"}) {
		t.Fatalf("behind with grace = %#v", got)
	}
	if got := behindSources(clocks, at, false); !reflect.DeepEqual(got, []string{"recent", "never"}) {
		t.Fatalf("behind historical hour = %#v", got)
	}
	if got := heatIndex(1.234); got != 12.3 {
		t.Fatalf("heat index = %v", got)
	}
}
