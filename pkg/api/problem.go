package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

// Problem is an RFC 9457 problem details body (application/problem+json)
// with the service's stable machine-readable error code. Every error the
// API answers has this shape.
type Problem struct {
	huma.ErrorModel
	Code      string `json:"code" doc:"Stable machine-readable error code: invalid_request, unauthorized, not_found, method_not_allowed, conflict, invalid_state, not_ready, host_not_seated, gone, payload_too_large, unsupported_media_type, league_not_held, unknown_game_mode, unknown_region, invite_limited, internal, unavailable or gc_timeout" example:"not_found"`
	Retryable bool   `json:"retryable" doc:"True when the same request may succeed later (503 and 504)"`
}

// problemContentType is the media type of every error body.
const problemContentType = "application/problem+json"

// defaultCodes are the codes of errors made without an explicit one,
// e.g. huma's own parse and validation errors.
var defaultCodes = map[int]string{
	http.StatusBadRequest:            "invalid_request",
	http.StatusUnauthorized:          "unauthorized",
	http.StatusNotFound:              "not_found",
	http.StatusMethodNotAllowed:      "method_not_allowed",
	http.StatusRequestTimeout:        "request_timeout",
	http.StatusConflict:              "conflict",
	http.StatusGone:                  "gone",
	http.StatusRequestEntityTooLarge: "payload_too_large",
	http.StatusUnsupportedMediaType:  "unsupported_media_type",
	http.StatusUnprocessableEntity:   "invalid_request",
	http.StatusInternalServerError:   "internal",
	http.StatusServiceUnavailable:    "unavailable",
}

func defaultCode(status int) string {
	if c, ok := defaultCodes[status]; ok {
		return c
	}
	if status == 0 {
		// huma builds one error with status 0 to learn the error type.
		return ""
	}
	return strings.ToLower(strings.ReplaceAll(http.StatusText(status), " ", "_"))
}

func retryableStatus(status int) bool {
	return status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

// huma.NewError is a package global: this replaces it for the whole
// process, so every error huma makes (validation, body limits, auth from
// the middleware) is a Problem too.
func init() {
	huma.NewError = newProblem
	huma.NewErrorWithContext = func(ctx huma.Context, status int, msg string, errs ...error) huma.StatusError {
		if status >= http.StatusInternalServerError && len(errs) > 0 {
			op := ""
			if o := ctx.Operation(); o != nil {
				op = o.OperationID
			}
			loggerFrom(ctx.Context()).ErrorContext(ctx.Context(), "request failed",
				"op", op, "status", status, "err", errors.Join(errs...))
		}
		return newProblem(status, msg, errs...)
	}
}

// newProblem builds a problem with the default code for status. The
// details of a 5xx are dropped: their text is internal and only logged.
func newProblem(status int, msg string, errs ...error) huma.StatusError {
	p := &Problem{
		ErrorModel: huma.ErrorModel{Status: status, Title: http.StatusText(status), Detail: msg},
		Code:       defaultCode(status),
		Retryable:  retryableStatus(status),
	}
	if status >= http.StatusInternalServerError {
		return p
	}
	for _, err := range errs {
		if err != nil {
			p.Errors = append(p.Errors, errorDetail(err))
		}
	}
	return p
}

// errorDetail converts err to a detail. huma echoes the whole body, or a
// whole object with an unexpected property, as the value; that can hold
// the lobby password, so such values are dropped.
func errorDetail(err error) *huma.ErrorDetail {
	var d huma.ErrorDetailer
	if !errors.As(err, &d) {
		return &huma.ErrorDetail{Message: err.Error()}
	}
	out := *d.ErrorDetail()
	switch out.Value.(type) {
	case map[string]any, []any:
		out.Value = nil
	}
	if out.Location == "body" {
		out.Value = nil
	}
	return &out
}

// coded is a problem with an explicit code.
func coded(status int, code, msg string, errs ...error) huma.StatusError {
	p := newProblem(status, msg, errs...).(*Problem)
	if code != "" {
		p.Code = code
	}
	return p
}

// invalid is a 422 invalid_request naming the offending field.
func invalid(location, msg string) huma.StatusError {
	return coded(http.StatusUnprocessableEntity, "invalid_request", "validation failed",
		&huma.ErrorDetail{Location: location, Message: msg})
}

// writeProblem writes a problem from a plain net/http handler.
func writeProblem(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", problemContentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	// The status line is out; a failed write only means the client left.
	_ = json.NewEncoder(w).Encode(coded(status, code, msg))
}

type loggerKey struct{}

func loggerFrom(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}
