package feedback

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type submissionRepoFixture struct {
	rows       map[int64]ForwardRecord
	forwardErr map[int64]string
	banned     bool
	nextID     int64
	created    Submission
	banKeys    []string
	forwarded  map[int64]time.Time
}

func newSubmissionRepoFixture() *submissionRepoFixture {
	return &submissionRepoFixture{rows: map[int64]ForwardRecord{}, forwardErr: map[int64]string{}, forwarded: map[int64]time.Time{}}
}

func (r *submissionRepoFixture) List(context.Context, string, string, int) (ListResult, error) {
	return ListResult{}, nil
}
func (r *submissionRepoFixture) ScreenshotKey(context.Context, int64) (*string, error) {
	return nil, nil
}
func (r *submissionRepoFixture) Update(context.Context, int64, Patch, string) (map[string]any, bool, error) {
	return nil, false, nil
}
func (r *submissionRepoFixture) Erase(context.Context, int64, string, string) (bool, error) {
	return false, nil
}
func (r *submissionRepoFixture) Ban(context.Context, string, string, string) error { return nil }
func (r *submissionRepoFixture) Unban(context.Context, string, string) error       { return nil }

func (r *submissionRepoFixture) Banned(_ context.Context, hashes []string) (bool, error) {
	r.banKeys = append([]string(nil), hashes...)
	return r.banned, nil
}

func (r *submissionRepoFixture) Create(_ context.Context, input Submission) (int64, error) {
	r.nextID++
	r.created = input
	r.rows[r.nextID] = ForwardRecord{ID: r.nextID, Content: input.Content, Email: input.Email, PageURL: input.PageURL, ScreenshotKey: input.ScreenshotKey, CreatedAt: time.Now()}
	r.forwardErr[r.nextID] = "pending"
	return r.nextID, nil
}

func (r *submissionRepoFixture) ForwardRecord(_ context.Context, id int64) (*ForwardRecord, error) {
	row, ok := r.rows[id]
	if !ok {
		return nil, nil
	}
	if _, ok := r.forwarded[id]; ok {
		return nil, nil
	}
	return &row, nil
}

func (r *submissionRepoFixture) PendingForwardIDs(_ context.Context, olderThan, newerThan time.Time, limit int) ([]int64, error) {
	ids := make([]int64, 0)
	for id, row := range r.rows {
		if _, done := r.forwarded[id]; done || r.forwardErr[id] == "" || !row.CreatedAt.Before(olderThan) || !row.CreatedAt.After(newerThan) {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) > limit {
		ids = ids[:limit]
	}
	return ids, nil
}

func (r *submissionRepoFixture) SetScreenshotKey(_ context.Context, id int64, key string) error {
	row := r.rows[id]
	row.ScreenshotKey = stringPtr(key)
	r.rows[id] = row
	return nil
}

func (r *submissionRepoFixture) SetForwardError(_ context.Context, id int64, message string) error {
	r.forwardErr[id] = message
	return nil
}

func (r *submissionRepoFixture) MarkForwarded(_ context.Context, id int64, at time.Time) error {
	r.forwarded[id] = at
	r.forwardErr[id] = ""
	return nil
}

type forwarderFixture struct {
	imageKey   string
	imageBytes []byte
	filename   string
	uploadErr  error
	sendErr    error
	sent       []any
}

func (f *forwarderFixture) UploadImage(_ context.Context, data []byte, filename string) (string, error) {
	f.imageBytes, f.filename = append([]byte(nil), data...), filename
	return f.imageKey, f.uploadErr
}

func (f *forwarderFixture) SendFeedback(_ context.Context, chat string, id int64, content, pageURL, email, note string, imageKey, imageNote *string) error {
	f.sent = []any{chat, id, content, pageURL, email, note, imageKey, imageNote}
	return f.sendErr
}

func TestSubmitValidatesNormalizesAndQueuesOnlyEnabledForwarding(t *testing.T) {
	repo := newSubmissionRepoFixture()
	service := NewService(repo, Config{SessionSecret: "stable-secret", FeishuInternalEnabled: true, FeishuInternalChatID: "chat"})
	enqueued := int64(0)
	service.SetForwardEnqueuer(func(_ context.Context, id int64) error { enqueued = id; return nil })
	result, err := service.Submit(context.Background(), SubmitInput{Content: "  A useful note  ", Email: " reader@example.invalid ", PageURL: " https://example.invalid/article ", IP: "192.0.2.10", UserAgent: "Chrome/123"})
	if err != nil || result.ID != 1 || enqueued != 1 {
		t.Fatalf("submission failed: result=%#v enqueued=%d err=%v", result, enqueued, err)
	}
	if repo.created.Content != "A useful note" || repo.created.Email == nil || *repo.created.Email != "reader@example.invalid" || repo.created.PageURL == nil || *repo.created.PageURL != "https://example.invalid/article" {
		t.Fatalf("submitted fields were not normalized: %#v", repo.created)
	}
	if len(repo.banKeys) != 1 || len(repo.banKeys[0]) != 24 || repo.created.SourceHash != repo.banKeys[0] {
		t.Fatalf("sender key changed during submit: ban=%#v created=%#v", repo.banKeys, repo.created)
	}

	if _, err := service.Submit(context.Background(), SubmitInput{Content: "x", IP: "192.0.2.11"}); rejectedStatus(err) != 400 {
		t.Fatalf("short feedback was accepted: %v", err)
	}
	if _, err := service.Submit(context.Background(), SubmitInput{Content: "valid", Email: "not-an-email", IP: "192.0.2.12"}); rejectedStatus(err) != 400 {
		t.Fatalf("invalid email was accepted: %v", err)
	}
	repo.banned = true
	if _, err := service.Submit(context.Background(), SubmitInput{Content: "valid", IP: "192.0.2.13"}); rejectedStatus(err) != 403 {
		t.Fatalf("banned sender was accepted: %v", err)
	}
}

func TestSubmitRateLimitAndScreenshotValidation(t *testing.T) {
	repo := newSubmissionRepoFixture()
	service := NewService(repo, Config{DataDir: t.TempDir()})
	for attempt := 0; attempt < 5; attempt++ {
		if _, err := service.Submit(context.Background(), SubmitInput{Content: "valid", IP: "198.51.100.1", UserAgent: "Safari/18"}); err != nil {
			t.Fatalf("allowed feedback attempt %d failed: %v", attempt+1, err)
		}
	}
	if _, err := service.Submit(context.Background(), SubmitInput{Content: "valid", IP: "198.51.100.1", UserAgent: "Safari/18"}); rejectedStatus(err) != 429 {
		t.Fatalf("sixth feedback was not rate limited: %v", err)
	}

	for _, test := range []struct {
		name string
		data []byte
	}{
		{name: "invalid signature", data: []byte("not an image")},
		{name: "oversized", data: bytes.Repeat([]byte{1}, feedbackMaxScreenshot+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			isolated := NewService(newSubmissionRepoFixture(), Config{DataDir: t.TempDir()})
			_, err := isolated.Submit(context.Background(), SubmitInput{Content: "valid", Screenshot: test.data, HasScreenshot: true, IP: test.name})
			if rejectedStatus(err) != 400 {
				t.Fatalf("invalid screenshot was accepted: %v", err)
			}
		})
	}
}

func TestSubmitStoresDecodedScreenshotAndForwardRemovesItAfterUpload(t *testing.T) {
	root := t.TempDir()
	repo := newSubmissionRepoFixture()
	forwarder := &forwarderFixture{imageKey: "img_fixture"}
	service := NewService(repo, Config{DataDir: root, SessionSecret: "secret", FeishuInternalEnabled: true, FeishuInternalChatID: "feedback-chat"}, forwarder)
	pngData := pngFixture(t)
	result, err := service.Submit(context.Background(), SubmitInput{Content: "screenshot feedback", Screenshot: pngData, HasScreenshot: true, IP: "203.0.113.10"})
	if err != nil {
		t.Fatal(err)
	}
	if repo.created.ScreenshotKey == nil || !stringsHasPrefix(*repo.created.ScreenshotKey, "local:") {
		t.Fatalf("valid screenshot was not stored locally: %#v", repo.created.ScreenshotKey)
	}
	path := filepath.Join(root, "feedback-screenshots", (*repo.created.ScreenshotKey)[len("local:"):])
	if stored, err := os.ReadFile(path); err != nil || !bytes.Equal(stored, pngData) {
		t.Fatalf("screenshot bytes changed: err=%v", err)
	}
	if err := service.Forward(context.Background(), result.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("uploaded screenshot file should be removed, stat err=%v", err)
	}
	row := repo.rows[result.ID]
	if row.ScreenshotKey == nil || *row.ScreenshotKey != "feishu:img_fixture" || repo.forwarded[result.ID].IsZero() {
		t.Fatalf("forwarded screenshot state was not saved: row=%#v forwarded=%#v", row, repo.forwarded)
	}
	if len(forwarder.sent) != 8 || forwarder.sent[0] != "feedback-chat" || forwarder.sent[6] == nil || *(forwarder.sent[6].(*string)) != "img_fixture" {
		t.Fatalf("feedback was not forwarded with its uploaded image: %#v", forwarder.sent)
	}
}

func TestForwardTransientUploadFailureLeavesScreenshotForRetry(t *testing.T) {
	root := t.TempDir()
	repo := newSubmissionRepoFixture()
	forwarder := &forwarderFixture{uploadErr: errors.New("temporarily unavailable")}
	service := NewService(repo, Config{DataDir: root, FeishuInternalEnabled: true, FeishuInternalChatID: "chat"}, forwarder)
	pngData := pngFixture(t)
	result, err := service.Submit(context.Background(), SubmitInput{Content: "retry", Screenshot: pngData, HasScreenshot: true, IP: "192.0.2.44"})
	if err != nil {
		t.Fatal(err)
	}
	key := *repo.rows[result.ID].ScreenshotKey
	path := filepath.Join(root, "feedback-screenshots", key[len("local:"):])
	if err := service.Forward(context.Background(), result.ID); err == nil {
		t.Fatal("transient Feishu failure unexpectedly succeeded")
	}
	if repo.forwardErr[result.ID] == "" || repo.rows[result.ID].ScreenshotKey == nil || *repo.rows[result.ID].ScreenshotKey != key {
		t.Fatalf("retry state was not retained: err=%q key=%#v", repo.forwardErr[result.ID], repo.rows[result.ID].ScreenshotKey)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("screenshot was removed before a successful upload: %v", err)
	}
}

func TestSourceHashUsesOnlyTheFirstFourRecognizedUserAgentFamilies(t *testing.T) {
	first := sourceHash("secret", "192.0.2.8", "Chrome Firefox Safari Edg Android iPhone")
	second := sourceHash("secret", "192.0.2.8", "Chrome Firefox Safari Edg Windows")
	if first != second {
		t.Fatal("browser family hash included more than the first four matched families")
	}
}

func rejectedStatus(err error) int {
	var rejected *Rejected
	if errors.As(err, &rejected) {
		return rejected.Status
	}
	return 0
}

func pngFixture(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func stringsHasPrefix(value, prefix string) bool {
	return len(value) >= len(prefix) && value[:len(prefix)] == prefix
}
