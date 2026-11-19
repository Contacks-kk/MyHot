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
	"strings"
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
	erased     []int64
}

func newSubmissionRepoFixture() *submissionRepoFixture {
	return &submissionRepoFixture{rows: map[int64]ForwardRecord{}, forwardErr: map[int64]string{}, forwarded: map[int64]time.Time{}}
}

func (r *submissionRepoFixture) List(context.Context, string, string, int) (ListResult, error) {
	return ListResult{}, nil
}

/** Looks up the attachment key associated with one feedback row. */
func (r *submissionRepoFixture) ScreenshotKey(_ context.Context, id int64) (*string, error) {
	row, ok := r.rows[id]
	if !ok {
		return nil, nil
	}
	return row.ScreenshotKey, nil
}
func (r *submissionRepoFixture) Update(context.Context, int64, Patch, string) (map[string]any, bool, error) {
	return nil, false, nil
}

/** Records the feedback row whose private data was erased. */
func (r *submissionRepoFixture) Erase(_ context.Context, id int64, _, _ string) (bool, error) {
	r.erased = append(r.erased, id)
	return true, nil
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
	uploads    int
	sends      int
	sent       []any
}

func (f *forwarderFixture) UploadImage(_ context.Context, data []byte, filename string) (string, error) {
	f.uploads++
	f.imageBytes, f.filename = append([]byte(nil), data...), filename
	return f.imageKey, f.uploadErr
}

func (f *forwarderFixture) SendFeedback(_ context.Context, chat string, id int64, content, pageURL, email, note string, imageKey, imageNote *string) error {
	f.sends++
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

/** Erasing a feedback removes only its own copy of an identical screenshot. */
func TestErasingOneSubmissionPreservesIdenticalScreenshotOwnedByAnother(t *testing.T) {
	root := t.TempDir()
	repo := newSubmissionRepoFixture()
	service := NewService(repo, Config{DataDir: root})
	screenshot := pngFixture(t)
	first, err := service.Submit(context.Background(), SubmitInput{Content: "first copy", Screenshot: screenshot, HasScreenshot: true, IP: "203.0.113.21"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Submit(context.Background(), SubmitInput{Content: "second copy", Screenshot: screenshot, HasScreenshot: true, IP: "203.0.113.22"})
	if err != nil {
		t.Fatal(err)
	}
	firstKey := repo.rows[first.ID].ScreenshotKey
	secondKey := repo.rows[second.ID].ScreenshotKey
	if firstKey == nil || secondKey == nil || *firstKey == *secondKey {
		t.Fatalf("identical submissions must own distinct attachment paths: first=%#v second=%#v", firstKey, secondKey)
	}
	firstPath := filepath.Join(root, "feedback-screenshots", (*firstKey)[len("local:"):])
	secondPath := filepath.Join(root, "feedback-screenshots", (*secondKey)[len("local:"):])

	if erased, err := service.Erase(context.Background(), first.ID, "reader requested erasure", "test"); err != nil || !erased {
		t.Fatalf("first feedback was not erased: erased=%t err=%v", erased, err)
	}
	if _, err := os.Stat(firstPath); !os.IsNotExist(err) {
		t.Fatalf("erased feedback attachment should be removed: stat err=%v", err)
	}
	stored, err := os.ReadFile(secondPath)
	if err != nil || !bytes.Equal(stored, screenshot) {
		t.Fatalf("other feedback attachment was affected: stored=%d expected=%d err=%v", len(stored), len(screenshot), err)
	}
	if len(repo.erased) != 1 || repo.erased[0] != first.ID {
		t.Fatalf("erase reached the wrong feedback row: %v", repo.erased)
	}
}

func TestForwardTransientUploadFailureLeavesScreenshotForRetry(t *testing.T) {
	root := t.TempDir()
	repo := newSubmissionRepoFixture()
	forwarder := &forwarderFixture{imageKey: "img_fixture", uploadErr: errors.New("temporarily unavailable")}
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
	row := repo.rows[result.ID]
	row.CreatedAt = time.Now().UTC().Add(-10 * time.Minute)
	repo.rows[result.ID] = row
	forwarder.uploadErr = nil
	sent, failed, err := service.ForwardPending(context.Background(), time.Now().UTC())
	if err != nil || sent != 1 || failed != 0 {
		t.Fatalf("pending sweep did not recover the transient upload: sent=%d failed=%d err=%v", sent, failed, err)
	}
	if repo.rows[result.ID].ScreenshotKey == nil || *repo.rows[result.ID].ScreenshotKey != "feishu:img_fixture" || forwarder.uploads != 2 || forwarder.sends != 1 {
		key := "<nil>"
		if repo.rows[result.ID].ScreenshotKey != nil {
			key = *repo.rows[result.ID].ScreenshotKey
		}
		t.Fatalf("retry did not forward the recovered screenshot: key=%q uploads=%d sends=%d", key, forwarder.uploads, forwarder.sends)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("local image should be removed after successful retry: stat err=%v", err)
	}
}

/** A failed Feishu message send retries with the uploaded image key and does not upload twice. */
func TestForwardSendFailureReusesUploadedScreenshotOnRetry(t *testing.T) {
	root := t.TempDir()
	repo := newSubmissionRepoFixture()
	forwarder := &forwarderFixture{imageKey: "img_fixture", sendErr: errors.New("temporarily unavailable")}
	service := NewService(repo, Config{DataDir: root, FeishuInternalEnabled: true, FeishuInternalChatID: "chat"}, forwarder)
	pngData := pngFixture(t)
	result, err := service.Submit(context.Background(), SubmitInput{Content: "send retry", Screenshot: pngData, HasScreenshot: true, IP: "192.0.2.45"})
	if err != nil {
		t.Fatal(err)
	}
	key := *repo.rows[result.ID].ScreenshotKey
	path := filepath.Join(root, "feedback-screenshots", key[len("local:"):])
	if err := service.Forward(context.Background(), result.ID); err == nil {
		t.Fatal("transient message-send failure unexpectedly succeeded")
	}
	if repo.rows[result.ID].ScreenshotKey == nil || *repo.rows[result.ID].ScreenshotKey != "feishu:img_fixture" {
		t.Fatalf("uploaded image key was not retained after send failure: %#v", repo.rows[result.ID].ScreenshotKey)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("uploaded local image should be removed after upload: stat err=%v", err)
	}
	forwarder.sendErr = nil
	if err := service.Forward(context.Background(), result.ID); err != nil {
		t.Fatal(err)
	}
	if forwarder.uploads != 1 || forwarder.sends != 2 || forwarder.sent[6] == nil || *(forwarder.sent[6].(*string)) != "img_fixture" {
		t.Fatalf("retry did not reuse the uploaded image: uploads=%d sends=%d sent=%#v", forwarder.uploads, forwarder.sends, forwarder.sent)
	}
}

/** A refused or day-old screenshot is dropped while its feedback text is still forwarded. */
func TestForwardDropsRefusedOrExpiredScreenshotAndSendsText(t *testing.T) {
	for _, test := range []struct {
		name      string
		uploadErr error
		age       time.Duration
	}{
		{name: "permanent refusal", uploadErr: ErrImageRefused},
		{name: "expired transient failure", uploadErr: errors.New("temporarily unavailable"), age: 25 * time.Hour},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			repo := newSubmissionRepoFixture()
			forwarder := &forwarderFixture{imageKey: "img_fixture", uploadErr: test.uploadErr}
			service := NewService(repo, Config{DataDir: root, FeishuInternalEnabled: true, FeishuInternalChatID: "chat"}, forwarder)
			pngData := pngFixture(t)
			result, err := service.Submit(context.Background(), SubmitInput{Content: "text survives", Screenshot: pngData, HasScreenshot: true, IP: "192.0.2.46"})
			if err != nil {
				t.Fatal(err)
			}
			row := repo.rows[result.ID]
			row.CreatedAt = time.Now().UTC().Add(-test.age)
			repo.rows[result.ID] = row
			path := filepath.Join(root, "feedback-screenshots", (*row.ScreenshotKey)[len("local:"):])

			if err := service.Forward(context.Background(), result.ID); err != nil {
				t.Fatal(err)
			}
			if repo.rows[result.ID].ScreenshotKey == nil || *repo.rows[result.ID].ScreenshotKey != "gone:upload" || forwarder.uploads != 1 || forwarder.sends != 1 {
				t.Fatalf("failed screenshot was not dropped after forwarding text: row=%#v uploads=%d sends=%d", repo.rows[result.ID], forwarder.uploads, forwarder.sends)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("unusable screenshot should be removed: stat err=%v", err)
			}
			imageKey, imageKeyOK := forwarder.sent[6].(*string)
			imageNote, imageNoteOK := forwarder.sent[7].(*string)
			if forwarder.sent[2] != "text survives" || !imageKeyOK || imageKey != nil || !imageNoteOK || imageNote == nil || !strings.Contains(*imageNote, "截图未能上传") {
				t.Fatalf("text fallback did not describe the missing screenshot: %#v", forwarder.sent)
			}
		})
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
