package publiccontent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

type hotTopicRepo struct {
	records []HotTopicRecord
	err     error
}

func (r hotTopicRepo) HotTopics(context.Context, time.Time) ([]HotTopicRecord, error) {
	return r.records, r.err
}

func (hotTopicRepo) Availability(context.Context, []string) ([]Record, error) { return nil, nil }

func (hotTopicRepo) StoryFollowups(context.Context, string, time.Time) ([]FollowupMember, error) {
	return nil, nil
}

func (hotTopicRepo) GroupReports(context.Context, GroupReportsQuery, time.Time) ([]GroupReportRow, error) {
	return nil, nil
}

func TestHotTopicsUsesCurrentPublicRepresentativeAndBuildsV1Links(t *testing.T) {
	latest := time.Date(2026, 10, 7, 3, 4, 5, 678_000_000, time.UTC)
	representativeSource := "Publisher（newsletter）"
	repository := hotTopicRepo{records: []HotTopicRecord{{
		Rank:          2,
		Entry:         json.RawMessage(`{"storyPublicId":"opaque-story","representativeItemId":"article_1","sourceCount":3,"signalCount":2,"participantCount":5,"sourceNames":["Publisher (newsletter)","X: Writer (@writer)"]}`),
		StoryPublicID: "opaque-story", StoryTitle: "Current story title", RepresentativeURL: "https://source.example/article",
		RepresentativeSource: &representativeSource, LatestAt: latest,
	}}}
	service := NewService(repository)
	response, err := service.HotTopics(context.Background(), time.Now(), "https://myhot.example/", "MyHOT")
	if err != nil {
		t.Fatal(err)
	}
	want := HotTopicsResponse{SchemaVersion: 1, Count: 1, Items: []HotTopicItem{{
		Rank: 2, ID: "article_1", Title: "Current story title", Source: HotSource{Name: "Publisher（newsletter）"},
		Links:       HotLinks{AIHOT: "https://myhot.example/items/article_1", Original: "https://source.example/article", Story: "https://myhot.example/story/opaque-story"},
		SourceCount: 3, SignalCount: 2, ParticipantCount: 5,
		SourceNames: []string{"Publisher (newsletter)", "X: Writer (@writer)"}, LatestAt: "2026-10-07T03:04:05.678Z",
	}}}
	if !reflect.DeepEqual(response, want) {
		t.Fatalf("hot topics = %#v, want %#v", response, want)
	}
}

func TestHotTopicsDropsEntriesWithoutCurrentRepresentativesAndReturnsEmptyArray(t *testing.T) {
	service := NewService(hotTopicRepo{records: []HotTopicRecord{{
		Rank: 1, Entry: json.RawMessage(`{"representativeItemId":null,"sourceNames":[]}`), StoryPublicID: "story",
	}}})
	response, err := service.HotTopics(context.Background(), time.Now(), "http://localhost:3000", "MyHOT")
	if err != nil {
		t.Fatal(err)
	}
	if response.Count != 0 || response.Items == nil {
		t.Fatalf("empty response = %#v", response)
	}
}

func TestHotTopicsPropagatesRepositoryErrorsAndRejectsMalformedSavedEntries(t *testing.T) {
	service := NewService(hotTopicRepo{err: errors.New("database unavailable")})
	if _, err := service.HotTopics(context.Background(), time.Now(), "https://myhot.example", "MyHOT"); err == nil {
		t.Fatal("expected repository error")
	}
	service = NewService(hotTopicRepo{records: []HotTopicRecord{{Entry: json.RawMessage(`{`)}}})
	if _, err := service.HotTopics(context.Background(), time.Now(), "https://myhot.example", "MyHOT"); err == nil {
		t.Fatal("expected malformed stored entry error")
	}
}

func TestHotTopicsNeedsRepositorySupport(t *testing.T) {
	service := NewService(&fakeRepository{})
	if _, err := service.HotTopics(context.Background(), time.Now(), "https://myhot.example", "MyHOT"); err == nil {
		t.Fatal("expected missing hot topics repository error")
	}
}
