package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"aihot/backend-go/internal/admindeliveries"
)

func (s *Server) adminDeliveryResolve(w http.ResponseWriter, r *http.Request) {
	payload, parseError := readDeliveryResolution(w, r)
	if parseError.status != 0 {
		writeAdminProblem(w, parseError.status, "invalid_request", parseError.detail)
		return
	}
	admin, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	outcome, note, validation := validateDeliveryResolution(payload)
	if validation.status != 0 {
		writeAdminProblem(w, validation.status, "invalid_request", validation.detail)
		return
	}
	if s.AdminDeliveries == nil {
		writeAdminProblem(w, http.StatusServiceUnavailable, "temporarily_unavailable", "Temporarily unavailable.")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeAdminProblem(w, http.StatusNotFound, "not_found", "Not found.")
		return
	}
	result, found, err := s.AdminDeliveries.Resolve(r.Context(), id, outcome, note, adminActor(admin))
	if err != nil {
		switch {
		case errors.Is(err, admindeliveries.ErrInvalidRequest):
			writeAdminProblem(w, http.StatusBadRequest, "invalid_request", err.Error())
		case errors.Is(err, admindeliveries.ErrNotResolvable), errors.Is(err, admindeliveries.ErrChanged),
			errors.Is(err, admindeliveries.ErrTargetDisabled), errors.Is(err, admindeliveries.ErrContentNotEligible):
			writeAdminProblem(w, http.StatusConflict, "conflict", err.Error())
		default:
			writeAdminProblem(w, http.StatusInternalServerError, "internal_error", err.Error())
		}
		return
	}
	if !found || result == nil {
		writeAdminProblem(w, http.StatusNotFound, "not_found", "Not found.")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type deliveryRequestError struct {
	status int
	detail string
}

func readDeliveryResolution(w http.ResponseWriter, r *http.Request) (any, deliveryRequestError) {
	var payload any = map[string]any{}
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, deliveryRequestError{status: http.StatusRequestEntityTooLarge, detail: "The request could not be processed."}
		}
		return nil, deliveryRequestError{status: http.StatusBadRequest, detail: "The request could not be processed."}
	}
	if len(body) == 0 {
		return payload, deliveryRequestError{}
	}
	contentType := strings.TrimSpace(r.Header.Get("Content-Type"))
	if contentType == "" {
		return nil, deliveryRequestError{status: http.StatusBadRequest, detail: "The request could not be processed."}
	}
	mediaType, _, typeErr := mime.ParseMediaType(contentType)
	if typeErr != nil {
		return nil, deliveryRequestError{status: http.StatusUnsupportedMediaType, detail: "The request could not be processed."}
	}
	switch mediaType {
	case "application/json":
		decoder := json.NewDecoder(strings.NewReader(string(body)))
		decoder.UseNumber()
		if err := decoder.Decode(&payload); err != nil {
			return nil, deliveryRequestError{status: http.StatusBadRequest, detail: "The request could not be processed."}
		}
		if decoder.Decode(new(any)) != io.EOF {
			return nil, deliveryRequestError{status: http.StatusBadRequest, detail: "The request could not be processed."}
		}
	case "text/plain":
		payload = string(body)
	default:
		return nil, deliveryRequestError{status: http.StatusUnsupportedMediaType, detail: "The request could not be processed."}
	}
	return payload, deliveryRequestError{}
}

func validateDeliveryResolution(payload any) (string, string, deliveryRequestError) {
	fields, ok := payload.(map[string]any)
	if !ok {
		typeName := "object"
		switch payload.(type) {
		case nil:
			typeName = "null"
		case []any:
			typeName = "array"
		case string:
			typeName = "string"
		case json.Number:
			typeName = "number"
		case bool:
			typeName = "boolean"
		}
		return "", "", deliveryRequestError{status: http.StatusBadRequest, detail: "Invalid input: expected object, received " + typeName}
	}
	issues := make([]string, 0, 2)
	outcome, _ := fields["outcome"].(string)
	if outcome != "sent" && outcome != "drop" && outcome != "resend" {
		issues = append(issues, `outcome: Invalid option: expected one of "sent"|"drop"|"resend"`)
	}
	noteValue, exists := fields["note"]
	note, isString := noteValue.(string)
	if !exists {
		issues = append(issues, "note: Invalid input: expected string, received undefined")
	} else if !isString {
		typeName := "object"
		switch noteValue.(type) {
		case nil:
			typeName = "null"
		case []any:
			typeName = "array"
		case json.Number:
			typeName = "number"
		case bool:
			typeName = "boolean"
		}
		issues = append(issues, "note: Invalid input: expected string, received "+typeName)
	} else if trimJavaScriptWhitespace(note) == "" {
		issues = append(issues, "note: Too small: expected string to have >=1 characters")
	}
	if len(issues) > 0 {
		detail := strings.Join(issues, "; ")
		if len(detail) > 300 {
			detail = detail[:300]
		}
		return "", "", deliveryRequestError{status: http.StatusBadRequest, detail: detail}
	}
	return outcome, note, deliveryRequestError{}
}

func trimJavaScriptWhitespace(value string) string {
	return strings.TrimFunc(value, func(char rune) bool {
		switch char {
		case '\t', '\n', '\v', '\f', '\r', ' ', '\u00a0', '\u1680', '\u2028', '\u2029', '\u202f', '\u205f', '\u3000', '\ufeff':
			return true
		default:
			return char >= '\u2000' && char <= '\u200a'
		}
	})
}
