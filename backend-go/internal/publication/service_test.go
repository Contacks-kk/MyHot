package publication

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

type republishRepositoryStub struct {
	ids     []string
	status  []json.RawMessage
	changed map[string]Result
}

func (r *republishRepositoryStub) Publish(_ context.Context, id string, _ PublishOptions) (*Result, error) {
	value := r.changed[id]
	return &value, nil
}

func (r *republishRepositoryStub) CountSourcePublications(context.Context, string) (int, error) {
	return len(r.ids), nil
}

func (r *republishRepositoryStub) SourcePublicationBatch(_ context.Context, _ string, after string, limit int) ([]string, error) {
	start := 0
	for start < len(r.ids) && r.ids[start] <= after {
		start++
	}
	end := min(start+limit, len(r.ids))
	return append([]string(nil), r.ids[start:end]...), nil
}

func (r *republishRepositoryStub) SetSourceRepublishStatus(_ context.Context, _ string, value json.RawMessage) error {
	r.status = append(r.status, append(json.RawMessage(nil), value...))
	return nil
}

func (r *republishRepositoryStub) QueuedSourceRepublishes(context.Context) ([]string, error) {
	return []string{"rss-a", "rss-b"}, nil
}

func TestRepublishSourceBatchesAndRunsFinalEffectsBeforeDone(t *testing.T) {
	ids := make([]string, 501)
	changed := make(map[string]Result, len(ids))
	for i := range ids {
		ids[i] = fmt.Sprintf("article-%04d", i)
		changed[ids[i]] = Result{Changed: i%2 == 0, Reduced: i == 500}
	}
	repository := &republishRepositoryStub{ids: ids, changed: changed}
	service := NewService(repository)
	var progress [][2]int
	var beforeDone RepublishResult
	result, err := service.RepublishSource(context.Background(), "rss-demo", func(done, total int) error {
		progress = append(progress, [2]int{done, total})
		return nil
	}, func(value RepublishResult) error {
		beforeDone = value
		var status map[string]any
		if err := json.Unmarshal(repository.status[len(repository.status)-1], &status); err != nil {
			return err
		}
		if status["status"] != "running" {
			t.Fatalf("final effect ran after completion status: %#v", status)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != (RepublishResult{Total: 501, Changed: 251, Reduced: 1}) || beforeDone != result {
		t.Fatalf("republish result=%+v beforeDone=%+v", result, beforeDone)
	}
	if len(progress) != 2 || progress[0] != [2]int{500, 501} || progress[1] != [2]int{501, 501} {
		t.Fatalf("progress = %#v", progress)
	}
	var final map[string]any
	if err := json.Unmarshal(repository.status[len(repository.status)-1], &final); err != nil {
		t.Fatal(err)
	}
	if final["status"] != "done" || final["total"] != float64(501) || final["done"] != nil {
		t.Fatalf("final status does not match legacy shape: %#v", final)
	}
}

func TestQueuedSourceRepublishesUsesRepository(t *testing.T) {
	service := NewService(&republishRepositoryStub{})
	got, err := service.QueuedSourceRepublishes(context.Background())
	if err != nil || fmt.Sprint(got) != "[rss-a rss-b]" {
		t.Fatalf("queued sources=%v err=%v", got, err)
	}
}
