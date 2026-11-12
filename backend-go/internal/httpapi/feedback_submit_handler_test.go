package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"aihot/backend-go/internal/feedback"
)

type submitFeedbackRepository struct {
	nextID  int64
	created feedback.Submission
	banned  bool
}

func (*submitFeedbackRepository) List(context.Context, string, string, int) (feedback.ListResult, error) {
	return feedback.ListResult{}, nil
}
func (*submitFeedbackRepository) ScreenshotKey(context.Context, int64) (*string, error) {
	return nil, nil
}
func (*submitFeedbackRepository) Update(context.Context, int64, feedback.Patch, string) (map[string]any, bool, error) {
	return nil, false, nil
}
func (*submitFeedbackRepository) Erase(context.Context, int64, string, string) (bool, error) {
	return false, nil
}
func (*submitFeedbackRepository) Ban(context.Context, string, string, string) error { return nil }
func (*submitFeedbackRepository) Unban(context.Context, string, string) error       { return nil }
func (r *submitFeedbackRepository) Banned(context.Context, []string) (bool, error) {
	return r.banned, nil
}
func (r *submitFeedbackRepository) Create(_ context.Context, input feedback.Submission) (int64, error) {
	r.nextID++
	r.created = input
	return r.nextID, nil
}
func (*submitFeedbackRepository) ForwardRecord(context.Context, int64) (*feedback.ForwardRecord, error) {
	return nil, nil
}
func (*submitFeedbackRepository) PendingForwardIDs(context.Context, time.Time, time.Time, int) ([]int64, error) {
	return []int64{}, nil
}
func (*submitFeedbackRepository) SetScreenshotKey(context.Context, int64, string) error { return nil }
func (*submitFeedbackRepository) SetForwardError(context.Context, int64, string) error  { return nil }
func (*submitFeedbackRepository) MarkForwarded(context.Context, int64, time.Time) error { return nil }

func TestFeedbackSubmitIsPublicAndReturnsCreatedContract(t *testing.T) {
	repo := &submitFeedbackRepository{}
	server := &Server{Feedback: feedback.NewService(repo, feedback.Config{})}
	body, contentType := feedbackForm(t, map[string]string{"content": "  A helpful report  ", "email": "reader@example.invalid", "pageUrl": "https://example.invalid/page"}, nil)
	request := httptest.NewRequest(http.MethodPost, "/api/site/feedback", body)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("X-Real-IP", "192.0.2.20")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("anonymous feedback should be accepted: status=%d body=%s", response.Code, response.Body.String())
	}
	var result map[string]int64
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result["id"] != 1 {
		t.Fatalf("response contract changed: result=%#v err=%v", result, err)
	}
	if repo.created.Content != "A helpful report" || repo.created.Email == nil || *repo.created.Email != "reader@example.invalid" || repo.created.PageURL == nil || *repo.created.PageURL != "https://example.invalid/page" {
		t.Fatalf("multipart values were not bound: %#v", repo.created)
	}
	if response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("Surrogate-Control") != "no-store" {
		t.Fatalf("feedback response was cacheable: %#v", response.Header())
	}
}

func TestFeedbackSubmitMapsFormValidationAndRequestSize(t *testing.T) {
	server := &Server{Feedback: feedback.NewService(&submitFeedbackRepository{}, feedback.Config{})}
	badText, badTextType := feedbackForm(t, map[string]string{"content": "valid", "screenshot": "not-a-file"}, nil)
	request := httptest.NewRequest(http.MethodPost, "/api/site/feedback", badText)
	request.Header.Set("Content-Type", badTextType)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !bytes.Contains(response.Body.Bytes(), []byte("截图上传格式不正确")) {
		t.Fatalf("screenshot text should be rejected: status=%d body=%s", response.Code, response.Body.String())
	}

	tooLarge := bytes.NewReader(bytes.Repeat([]byte("x"), feedbackRequestLimit+1))
	request = httptest.NewRequest(http.MethodPost, "/api/site/feedback", tooLarge)
	request.Header.Set("Content-Type", "multipart/form-data; boundary=fixture")
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge || !bytes.Contains(response.Body.Bytes(), []byte(`"code":"invalid_request"`)) {
		t.Fatalf("oversized feedback did not preserve the problem contract: status=%d body=%s", response.Code, response.Body.String())
	}
}

func feedbackForm(t *testing.T, fields map[string]string, file []byte) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if file != nil {
		part, err := writer.CreateFormFile("screenshot", "fixture.png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(file); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, writer.FormDataContentType()
}
