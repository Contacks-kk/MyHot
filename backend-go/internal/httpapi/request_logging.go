package httpapi

import (
	"bufio"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
)

type failureLoggingWriter struct {
	http.ResponseWriter
	request    *http.Request
	logger     *slog.Logger
	status     int
	requestID  string
	stack      []byte
	writeError string
	logged     bool
}

/** logServerFailures records server failures without retaining request secrets. */
func logServerFailures(logger *slog.Logger, next http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := &failureLoggingWriter{
			ResponseWriter: w,
			request:        r,
			logger:         logger,
		}

		next.ServeHTTP(response, r)
		if response.status >= http.StatusInternalServerError {
			response.logFailure()
		}
	})
}

/** WriteHeader captures failure context before a handler returns its response. */
func (w *failureLoggingWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	if status >= http.StatusInternalServerError {
		w.requestID = w.Header().Get("X-Request-Id")
		if w.requestID == "" {
			w.requestID = siteRequestID()
			w.Header().Set("X-Request-Id", w.requestID)
		}
		w.stack = debug.Stack()
	}
	w.ResponseWriter.WriteHeader(status)
}

/** Write records implicit success status and captures transport write failures. */
func (w *failureLoggingWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}

	written, err := w.ResponseWriter.Write(body)
	if err != nil {
		if w.stack == nil {
			w.stack = debug.Stack()
		}
		w.writeError = errorType(err)
		w.logFailure()
	}
	return written, err
}

/** Flush forwards streaming responses while recording their implicit success status. */
func (w *failureLoggingWriter) Flush() {
	flusher, ok := w.ResponseWriter.(http.Flusher)
	if !ok {
		return
	}
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	flusher.Flush()
}

/** Unwrap exposes the underlying writer to HTTP response controllers. */
func (w *failureLoggingWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

/** Hijack forwards connection hijacking for WebSocket upgrades. */
func (w *failureLoggingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hijacker.Hijack()
}

/** logFailure emits one request record with no query, header, body, or raw error values. */
func (w *failureLoggingWriter) logFailure() {
	if w.logged {
		return
	}
	w.logged = true
	if w.status == 0 {
		w.status = http.StatusInternalServerError
	}
	if w.requestID == "" {
		w.requestID = w.Header().Get("X-Request-Id")
	}
	if w.requestID == "" {
		w.requestID = siteRequestID()
	}
	if w.stack == nil {
		w.stack = debug.Stack()
	}

	fields := []any{
		"method", w.request.Method,
		"path", w.request.URL.EscapedPath(),
		"status", w.status,
		"request_id", w.requestID,
		"stack", string(w.stack),
	}
	if w.writeError != "" {
		fields = append(fields, "write_error_type", w.writeError)
	}
	w.logger.Error("http request failed", fields...)
}

/** errorType returns only the Go type so transport errors cannot leak their messages. */
func errorType(err error) string {
	return fmt.Sprintf("%T", err)
}
