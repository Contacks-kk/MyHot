package publiccontent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type selectedSyncRepo struct {
	epoch        string
	watermark    int64
	snapshotRows []SelectedSnapshotRecord
	changeRows   []SelectedChangeRecord
	snapshotArgs []any
	changeArgs   []any
	err          error
}

func (r *selectedSyncRepo) Availability(context.Context, []string) ([]Record, error) { return nil, nil }
func (r *selectedSyncRepo) StoryFollowups(context.Context, string, time.Time) ([]FollowupMember, error) {
	return nil, nil
}
func (r *selectedSyncRepo) GroupReports(context.Context, GroupReportsQuery, time.Time) ([]GroupReportRow, error) {
	return nil, nil
}
func (r *selectedSyncRepo) LedgerEpoch(context.Context) (string, error) { return r.epoch, r.err }
func (r *selectedSyncRepo) LedgerWatermark(context.Context) (int64, error) {
	return r.watermark, r.err
}
func (r *selectedSyncRepo) SelectedSnapshot(_ context.Context, limit int, watermark int64, after string, minimal bool) ([]SelectedSnapshotRecord, error) {
	r.snapshotArgs = []any{limit, watermark, after, minimal}
	return r.snapshotRows, r.err
}
func (r *selectedSyncRepo) SelectedChanges(_ context.Context, limit int, after, watermark int64, now time.Time, minimal bool) ([]SelectedChangeRecord, error) {
	r.changeArgs = []any{limit, after, watermark, now, minimal}
	return r.changeRows, r.err
}

func TestSelectedSnapshotPinsWatermarkAndBuildsContinuationWithProjection(t *testing.T) {
	now := time.Date(2026, 10, 7, 3, 4, 5, 678_901_000, time.FixedZone("local", 8*60*60))
	repository := &selectedSyncRepo{
		epoch: "epoch-1", watermark: 12,
		snapshotRows: []SelectedSnapshotRecord{
			{ArticleID: "article_1", Payload: json.RawMessage(`{"id":"article_1","title":"one"}`)},
			{ArticleID: "article_2", Payload: json.RawMessage(`{"id":"article_2","title":"two"}`)},
		},
	}
	service := NewService(repository)
	response, err := service.SelectedSnapshot(context.Background(), SelectedSnapshotQuery{Limit: 1}, now)
	if err != nil {
		t.Fatal(err)
	}
	if response.SchemaVersion != 1 || response.Fields != "default" || response.Count != 1 || !response.HasMore || response.NextPage == nil || response.AsOf != "2026-10-06T19:04:05.678Z" {
		t.Fatalf("snapshot = %#v", response)
	}
	if got, want := repository.snapshotArgs, []any{1, int64(12), "", false}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first-page repository args = %#v, want %#v", got, want)
	}
	page, err := decodeSyncCursor(*response.NextPage)
	if err != nil {
		t.Fatal(err)
	}
	if page.Kind != "page" || page.Epoch != "epoch-1" || page.Mark != 12 || page.Fields != "default" || page.After != "article_1" || page.AsOf != response.AsOf {
		t.Fatalf("next page cursor = %#v", page)
	}

	repository.snapshotRows = []SelectedSnapshotRecord{{ArticleID: "article_2", Payload: json.RawMessage(`{"id":"article_2","title":"two","source":{"name":"source"},"publishedAt":null,"discoveredAt":"2026-10-07T03:00:00.000Z","category":null,"score":null,"selected":true,"links":{"aihot":"/items/article_2","original":"https://origin.example"},"summary":"sensitive","unexpected":true}`)}}
	continued, err := service.SelectedSnapshot(context.Background(), SelectedSnapshotQuery{Limit: 5, Page: response.NextPage}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	_ = continued
	minimal := "minimal"
	if _, err := service.SelectedSnapshot(context.Background(), SelectedSnapshotQuery{Fields: &minimal, Limit: 5, Page: response.NextPage}, now); err == nil {
		t.Fatal("expected the page cursor projection mismatch")
	} else {
		var invalid InvalidCursorError
		if !errors.As(err, &invalid) {
			t.Fatalf("projection mismatch = %T %v, want InvalidCursorError", err, err)
		}
	}
}

func TestSelectedSnapshotRejectsBrokenAndFuturePageCursors(t *testing.T) {
	repository := &selectedSyncRepo{epoch: "epoch-1", watermark: 5}
	service := NewService(repository)
	for _, page := range []string{"bad", encodeSyncCursor(syncCursor{Kind: "page", Epoch: "epoch-1", Mark: 6, Fields: "default", After: "article_1", AsOf: "2026-10-07T00:00:00Z"})} {
		if _, err := service.SelectedSnapshot(context.Background(), SelectedSnapshotQuery{Limit: 5, Page: &page}, time.Now()); err == nil {
			t.Fatalf("page %q should require a fresh snapshot", page)
		} else {
			var required SnapshotRequiredError
			if !errors.As(err, &required) {
				t.Fatalf("page error = %T %v, want SnapshotRequiredError", err, err)
			}
		}
	}
	for _, payload := range []string{
		`{"k":"sync","e":"epoch-1","w":null,"f":"default"}`,
		`{"k":"sync","e":"epoch-1","w":1.5,"f":"default"}`,
	} {
		encoded := syncCursorPrefix + "." + base64.RawURLEncoding.EncodeToString([]byte(payload))
		if _, err := decodeSyncCursor(encoded); err == nil {
			t.Fatalf("cursor payload %s should be invalid", payload)
		}
	}
}

func TestSelectedChangesPreserveRemovalAndProjectMinimalUpserts(t *testing.T) {
	now := time.Date(2026, 10, 7, 3, 4, 5, 678_900_000, time.UTC)
	input := encodeSyncCursor(syncCursor{Kind: "sync", Epoch: "epoch-1", Mark: 4, Fields: "minimal"})
	repository := &selectedSyncRepo{epoch: "epoch-1", watermark: 9, changeRows: []SelectedChangeRecord{
		{Sequence: 5, ArticleID: "article_1", Operation: "upsert", ChangedAt: now, Payload: json.RawMessage(`{"id":"article_1","title":"one","source":{"name":"wire","extra":true},"publishedAt":null,"discoveredAt":"2026-10-07T03:00:00.000Z","category":null,"score":4,"selected":true,"links":{"aihot":"/items/article_1","original":"https://origin.example"},"summary":"private","reason":"private","unexpected":true}`)},
		{Sequence: 6, ArticleID: "article_2", Operation: "remove", ChangedAt: now.Add(time.Second)},
		{Sequence: 7, ArticleID: "article_3", Operation: "upsert", ChangedAt: now.Add(2 * time.Second), Payload: json.RawMessage(`{"id":"article_3","title":"three","source":{"name":"wire"},"publishedAt":null,"discoveredAt":"2026-10-07T03:00:00.000Z","category":null,"score":null,"selected":false,"links":{"aihot":"/items/article_3","original":"https://origin.example"}}`)},
	}}
	response, err := NewService(repository).SelectedChanges(context.Background(), input, 2, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if response.Count != 2 || !response.HasMore || len(response.Changes) != 2 || response.Changes[0].Operation != "upsert" || response.Changes[1].Operation != "remove" || response.Changes[1].ID != "article_2" {
		t.Fatalf("changes = %#v", response)
	}
	var projected map[string]json.RawMessage
	if err := json.Unmarshal(response.Changes[0].Item, &projected); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(response.Changes[0].Item), "summary") || strings.Contains(string(response.Changes[0].Item), "unexpected") || strings.Contains(string(response.Changes[0].Item), "original") {
		t.Fatalf("minimal item leaked fields: %s", response.Changes[0].Item)
	}
	if string(projected["id"]) != `"article_1"` || string(projected["links"]) != `{"aihot":"/items/article_1"}` {
		t.Fatalf("minimal item projection = %s", response.Changes[0].Item)
	}
	if got, want := repository.changeArgs, []any{2, int64(4), int64(9), now.Add(time.Minute), true}; !reflect.DeepEqual(got, want) {
		t.Fatalf("changes repository args = %#v, want %#v", got, want)
	}
	updated, err := decodeSyncCursor(response.Cursor)
	if err != nil || updated.Mark != 6 || updated.Fields != "minimal" {
		t.Fatalf("advanced cursor = %#v, err=%v", updated, err)
	}
	if _, err := NewService(&selectedSyncRepo{epoch: "epoch-1", watermark: 2}).SelectedChanges(context.Background(), input, 2, now); err == nil {
		t.Fatal("expected watermark-ahead error")
	}
}

func TestSelectedSyncPropagatesRepositoryErrorAndRequiresCapability(t *testing.T) {
	if _, err := NewService(&fakeRepository{}).SelectedSnapshot(context.Background(), SelectedSnapshotQuery{Limit: 1}, time.Now()); err == nil {
		t.Fatal("expected missing repository capability")
	}
	repository := &selectedSyncRepo{err: errors.New("database unavailable")}
	if _, err := NewService(repository).SelectedSnapshot(context.Background(), SelectedSnapshotQuery{Limit: 1}, time.Now()); err == nil {
		t.Fatal("expected repository error")
	}
}
