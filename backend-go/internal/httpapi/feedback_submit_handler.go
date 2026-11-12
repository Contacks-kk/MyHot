package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"aihot/backend-go/internal/feedback"
)

const (
	feedbackRequestLimit    = 12 * 1024 * 1024
	feedbackScreenshotLimit = 8 * 1024 * 1024
)

func (s *Server) submitFeedback(w http.ResponseWriter, r *http.Request) {
	setFeedbackHeaders(w)
	if s.Feedback == nil {
		writeFeedbackProblem(w, 503, "temporarily_unavailable", "暂时无法提交，请稍后再试。", 30)
		return
	}
	if r.ContentLength > feedbackRequestLimit {
		writeFeedbackProblem(w, 413, "invalid_request", "The request could not be processed.", 0)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, feedbackRequestLimit)
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "multipart/form-data" {
		writeFeedbackProblem(w, 415, "invalid_request", "The request could not be processed.", 0)
		return
	}
	_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	fields, parseErr := readFeedbackMultipart(r, params["boundary"])
	if parseErr != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(parseErr, &tooLarge) {
			writeFeedbackProblem(w, 413, "invalid_request", "The request could not be processed.", 0)
			return
		}
		writeFeedbackProblem(w, 400, "invalid_request", "截图上传格式不正确，请重新提交。", 0)
		return
	}
	contentField := fields["content"]
	if contentField.isFile {
		writeFeedbackProblem(w, 400, "invalid_request", "反馈文字格式不正确。", 0)
		return
	}
	emailField := fields["email"]
	if emailField.isFile {
		writeFeedbackProblem(w, 400, "invalid_request", "反馈文字格式不正确。", 0)
		return
	}
	pageURLField := fields["pageUrl"]
	if pageURLField.isFile {
		writeFeedbackProblem(w, 400, "invalid_request", "反馈文字格式不正确。", 0)
		return
	}
	screenshotField := fields["screenshot"]
	if screenshotField.present && !screenshotField.isFile {
		writeFeedbackProblem(w, 400, "invalid_request", "截图上传格式不正确，请重新提交。", 0)
		return
	}
	ip := strings.TrimSpace(r.Header.Get("X-Real-IP"))
	if ip == "" {
		ip = clientKey(r, s.Config.TrustProxy)
	}
	result, err := s.Feedback.Submit(r.Context(), feedback.SubmitInput{
		Content: contentField.value, Email: emailField.value, PageURL: pageURLField.value,
		Screenshot: screenshotField.file, HasScreenshot: screenshotField.present && screenshotField.isFile,
		IP: ip, UserAgent: r.UserAgent(),
	})
	if err != nil {
		var rejected *feedback.Rejected
		if errors.As(err, &rejected) {
			writeFeedbackProblem(w, rejected.Status, rejected.Code, rejected.Message, rejected.RetryAfter)
			return
		}
		writeFeedbackProblem(w, 503, "temporarily_unavailable", "暂时无法提交，请稍后再试。", 30)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(result)
}

type feedbackFormField struct {
	present bool
	isFile  bool
	value   string
	file    []byte
}

func readFeedbackMultipart(r *http.Request, boundary string) (map[string]feedbackFormField, error) {
	if boundary == "" {
		return nil, errors.New("multipart boundary is missing")
	}
	reader := multipart.NewReader(r.Body, boundary)
	fields := make(map[string]feedbackFormField, 4)
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			return fields, nil
		}
		if err != nil {
			return nil, err
		}
		name := part.FormName()
		if name != "content" && name != "email" && name != "pageUrl" && name != "screenshot" {
			_, copyErr := io.Copy(io.Discard, part)
			closeErr := part.Close()
			if copyErr != nil {
				return nil, copyErr
			}
			if closeErr != nil {
				return nil, closeErr
			}
			continue
		}
		if _, exists := fields[name]; exists {
			_, copyErr := io.Copy(io.Discard, part)
			closeErr := part.Close()
			if copyErr != nil {
				return nil, copyErr
			}
			if closeErr != nil {
				return nil, closeErr
			}
			continue
		}
		_, disposition, dispositionErr := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		if dispositionErr != nil {
			_ = part.Close()
			return nil, dispositionErr
		}
		_, isFile := disposition["filename"]
		field := feedbackFormField{present: true, isFile: isFile}
		limit := int64(feedbackRequestLimit) + 1
		if name == "screenshot" && isFile {
			limit = feedbackScreenshotLimit + 1
		}
		data, readErr := io.ReadAll(io.LimitReader(part, limit))
		closeErr := part.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if isFile {
			field.file = data
		} else {
			field.value = string(data)
		}
		fields[name] = field
	}
}

func setFeedbackHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Surrogate-Control", "no-store")
}

func writeFeedbackProblem(w http.ResponseWriter, status int, code, detail string, retryAfter int) {
	requestID := siteRequestID()
	title := map[int]string{400: "Bad request", 403: "Forbidden", 413: "Error", 415: "Error", 429: "Too many requests", 503: "Temporarily unavailable"}[status]
	if title == "" {
		title = "Error"
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("X-Request-Id", requestID)
	setFeedbackHeaders(w)
	body := map[string]any{
		"type": "/problems/" + strings.ReplaceAll(code, "_", "-"), "title": title, "status": status,
		"detail": detail, "code": code, "requestId": requestID,
	}
	if retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		body["retryAfter"] = retryAfter
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
