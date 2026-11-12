package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"aihot/backend-go/internal/materials"
)

type fakeRepository struct {
	sourceID, sourceName string
	received, created    int
	err                  error
}

func (r *fakeRepository) EnsureSource(_ context.Context, id, name string) error {
	r.sourceID, r.sourceName = id, name
	return r.err
}

func (r *fakeRepository) RecordSuccess(_ context.Context, id string, received, created int) error {
	r.sourceID, r.received, r.created = id, received, created
	return r.err
}

type fakeMaterials struct {
	inputs []materials.Input
}

func (m *fakeMaterials) Store(_ context.Context, input materials.Input) (materials.Result, error) {
	m.inputs = append(m.inputs, input)
	return materials.Result{Created: true}, nil
}

func TestIngestDeduplicatesCanonicalURLsAndPreservesSourcePayload(t *testing.T) {
	repository := &fakeRepository{}
	materialWriter := &fakeMaterials{}
	service := NewService(repository, materialWriter)
	body := json.RawMessage(`{"sourceId":" external-reporter ","sourceName":" Reporter ","items":[
		{"title":"  First item  ","url":"http://www.example.com/story/?utm_source=feed","publishedAt":"2026-10-07T10:00:00Z","author":" Reporter ","raw":{"_aihot":{"backfill":true},"externalId":"x-1"}},
		{"title":"Duplicate","url":"https://example.com/story"},
		{"title":"Bad URL","url":"javascript:alert(1)"},
		{"title":"Missing URL"},null
	]}`)
	result, err := service.Ingest(context.Background(), body)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Created != 1 || len(materialWriter.inputs) != 1 {
		t.Fatalf("ingest result=%#v inputs=%d", result, len(materialWriter.inputs))
	}
	if repository.sourceID != "external-reporter" || repository.sourceName != "Reporter" || repository.received != 5 || repository.created != 1 {
		t.Fatalf("recorded request: %#v", repository)
	}
	input := materialWriter.inputs[0]
	if input.SourceID != "external-reporter" || input.URL != "http://www.example.com/story/?utm_source=feed" || input.IdentityKey != "url:https://example.com/story" || input.Via != "ingest" || input.Backfill != "reported-backfill" {
		t.Fatalf("material identity/input changed: %#v", input)
	}
	if input.Author != " Reporter " || input.PublishedAt == nil || input.PublishedAt.UTC().Format("2006-01-02T15:04:05Z") != "2026-10-07T10:00:00Z" || !strings.Contains(string(input.Raw), `"externalId":"x-1"`) {
		t.Fatalf("material metadata changed: %#v raw=%s", input, input.Raw)
	}
}

func TestIngestValidatesRequestAndReportsPausedSources(t *testing.T) {
	service := NewService(&fakeRepository{}, &fakeMaterials{})
	for _, test := range []struct {
		body    string
		status  int
		message string
	}{
		{`{}`, 400, ErrRequired.Error()},
		{`{"sourceId":"a","items":[]}`, 400, ErrRequired.Error()},
	} {
		_, err := service.Ingest(context.Background(), json.RawMessage(test.body))
		var requestError *RequestError
		if !errors.As(err, &requestError) || requestError.Status != test.status || requestError.Message != test.message {
			t.Fatalf("body %s error = %#v", test.body, err)
		}
	}
	emptyRepository := &fakeRepository{}
	emptyResult, err := NewService(emptyRepository, &fakeMaterials{}).Ingest(context.Background(), json.RawMessage(`{"sourceId":"a","items":[{}]}`))
	if err != nil || !emptyResult.OK || emptyResult.Created != 0 || emptyRepository.received != 1 {
		t.Fatalf("non-material item should be accepted and skipped: result=%#v repository=%#v err=%v", emptyResult, emptyRepository, err)
	}
	items := make([]map[string]string, MaxItems+1)
	for i := range items {
		items[i] = map[string]string{"title": "story", "url": "https://example.com/" + string(rune('a'+i))}
	}
	tooMany, _ := json.Marshal(map[string]any{"sourceId": "a", "items": items})
	_, err = service.Ingest(context.Background(), tooMany)
	var requestError *RequestError
	if !errors.As(err, &requestError) || requestError.Status != 413 || requestError.Message != "items[] exceeds max 50 per request" {
		t.Fatalf("oversized batch error = %#v", err)
	}

	paused := NewService(&fakeRepository{err: ErrSourcePaused}, &fakeMaterials{})
	_, err = paused.Ingest(context.Background(), json.RawMessage(`{"sourceId":"a","items":[{}]}`))
	if !errors.Is(err, ErrSourcePaused) {
		t.Fatalf("paused source error = %v", err)
	}
}
