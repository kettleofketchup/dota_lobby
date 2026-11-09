package api

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
)

type specOp struct {
	method, path string
	op           *huma.Operation
}

func specOps(t *testing.T, s *Server) []specOp {
	t.Helper()
	var out []specOp
	for path, item := range s.OpenAPI().Paths {
		for method, op := range map[string]*huma.Operation{
			http.MethodGet: item.Get, http.MethodPost: item.Post, http.MethodPut: item.Put,
			http.MethodPatch: item.Patch, http.MethodDelete: item.Delete,
		} {
			if op != nil {
				out = append(out, specOp{method, path, op})
			}
		}
	}
	return out
}

// Operation IDs are public API: client generators key on them.
func TestOperationIDs(t *testing.T) {
	want := []string{
		"close-lobby", "create-lobby", "flip-lobby-teams", "get-bot", "get-lobby", "invite-lobby-players",
		"kick-lobby-member", "launch-lobby", "list-bots", "list-league-nodes", "list-leagues", "list-lobbies",
		"list-lobby-events", "retry-bot", "set-lobby-roster", "shuffle-lobby-teams", "update-lobby",
	}
	var got []string
	for _, o := range specOps(t, New(Options{})) {
		got = append(got, o.op.OperationID)
		if o.op.Summary == "" || len(o.op.Tags) != 1 {
			t.Errorf("%s: summary %q, tags %v", o.op.OperationID, o.op.Summary, o.op.Tags)
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("operation IDs = %v, want %v", got, want)
	}
}

// Every documented operation requires the key, and the middleware
// enforces it: a forgotten Security fails here, not in production.
func TestEveryOperationNeedsKey(t *testing.T) {
	s := lobbyServer(t, &fakeLobbies{lobby: sampleLobby()})
	fill := strings.NewReplacer("{id}", "1", "{name}", "bot1")
	for _, o := range specOps(t, s) {
		t.Run(o.op.OperationID, func(t *testing.T) {
			if !reflect.DeepEqual(o.op.Security, keyAuth) {
				t.Errorf("security = %v", o.op.Security)
			}
			req := httptest.NewRequest(o.method, fill.Replace(o.path), nil)
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != authChallenge ||
				rec.Header().Get("Content-Type") != problemContentType || !strings.Contains(rec.Body.String(), `"code":"unauthorized"`) {
				t.Errorf("without key = %d %v %s", rec.Code, rec.Header(), rec.Body)
			}
			req = httptest.NewRequest(o.method, fill.Replace(o.path), nil)
			req.Header.Set("Authorization", "Bearer "+testKey)
			rec = httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			if rec.Code == http.StatusUnauthorized {
				t.Errorf("with key = %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestOpenAPIRoutes(t *testing.T) {
	s := newTestServer(t, testKey, 0, &fakeBots{})
	tests := []struct {
		method, path string
		code         int
		ct           string
	}{
		{"GET", "/openapi.json", 200, "application/openapi+json"},
		{"GET", "/openapi.yaml", 200, "application/openapi+yaml"},
		{"GET", "/openapi-3.0.json", 200, "application/openapi+json"},
		{"POST", "/openapi.json", 405, problemContentType},
		{"GET", "/docs", 404, problemContentType},
		{"GET", "/schemas/LobbyView.json", 404, problemContentType},
	}
	for _, tt := range tests {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
		if rec.Code != tt.code || rec.Header().Get("Content-Type") != tt.ct {
			t.Errorf("%s %s = %d %q, want %d %q", tt.method, tt.path, rec.Code, rec.Header().Get("Content-Type"), tt.code, tt.ct)
		}
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/openapi.yaml", nil))
	for _, want := range []string{"openapi: 3.1.0", "version: " + APIVersion, "X-API-Key", "lobby-event:", "LobbyEvent:"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("served spec lacks %q", want)
		}
	}
}

func TestSpecDocumentsWebhook(t *testing.T) {
	oapi := New(Options{}).OpenAPI()
	hook := oapi.Webhooks["lobby-event"]
	if hook == nil || hook.Post == nil {
		t.Fatal("no lobby-event webhook")
	}
	if ref := hook.Post.RequestBody.Content["application/json"].Schema.Ref; ref != "#/components/schemas/LobbyEvent" {
		t.Errorf("webhook body = %q", ref)
	}
	if _, ok := oapi.Components.Schemas.Map()["LobbySummary"]; !ok {
		t.Error("LobbySummary schema missing")
	}
}

func TestProblem(t *testing.T) {
	detail := &huma.ErrorDetail{Location: "body.name", Message: "too long", Value: "x"}
	tests := []struct {
		name      string
		err       huma.StatusError
		status    int
		code      string
		retryable bool
		details   int
	}{
		{"validation", huma.NewError(422, "validation failed", detail), 422, "invalid_request", false, 1},
		{"bad body", huma.NewError(400, "bad", errors.New("eof")), 400, "invalid_request", false, 1},
		{"explicit code", coded(409, "invalid_state", "in_game"), 409, "invalid_state", false, 0},
		{"unmapped status", huma.NewError(http.StatusTeapot, "x"), 418, "i'm_a_teapot", false, 0},
		{"5xx drops details", huma.NewError(500, "unexpected error occurred", errors.New("disk on fire")), 500, "internal", false, 0},
		{"503 retryable", coded(503, "unavailable", "later"), 503, "unavailable", true, 0},
		{"504 retryable", coded(504, "gc_timeout", "slow"), 504, "gc_timeout", true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, ok := tt.err.(*Problem)
			if !ok {
				t.Fatalf("%T is not a *Problem", tt.err)
			}
			if p.Status != tt.status || p.GetStatus() != tt.status || p.Code != tt.code || p.Retryable != tt.retryable ||
				len(p.Errors) != tt.details || p.Title != http.StatusText(tt.status) {
				t.Errorf("problem = %+v", p)
			}
			if p.ContentType("application/json") != problemContentType {
				t.Errorf("content type %q", p.ContentType("application/json"))
			}
		})
	}
}

// Values that are whole objects, or the raw body, are dropped from error
// details: they can hold the lobby password.
func TestErrorDetailRedaction(t *testing.T) {
	tests := []struct {
		in   *huma.ErrorDetail
		keep bool
	}{
		{&huma.ErrorDetail{Location: "body", Value: `{"password":"x"`}, false},
		{&huma.ErrorDetail{Location: "body.nmae", Value: map[string]any{"password": "x"}}, false},
		{&huma.ErrorDetail{Location: "body.admins", Value: []any{1}}, false},
		{&huma.ErrorDetail{Location: "query.limit", Value: 0}, true},
	}
	for _, tt := range tests {
		got := errorDetail(tt.in)
		if (got.Value != nil) != tt.keep || got.Location != tt.in.Location {
			t.Errorf("%s: value %v", tt.in.Location, got.Value)
		}
	}
}

// A 5xx huma makes is logged with its cause, and the cause stays out of
// the response.
func TestServerErrorsAreLogged(t *testing.T) {
	var buf bytes.Buffer
	ctx := context.WithValue(context.Background(), loggerKey{}, slog.New(slog.NewTextHandler(&buf, nil)))
	p := huma.NewErrorWithContext(fakeHumaContext{ctx: ctx}, 500, "unexpected error occurred", errors.New("disk on fire"))
	if strings.Contains(p.Error(), "disk") || len(p.(*Problem).Errors) != 0 {
		t.Errorf("cause leaked into the problem: %+v", p)
	}
	if !strings.Contains(buf.String(), "disk on fire") || !strings.Contains(buf.String(), "op=get-lobby") {
		t.Errorf("log = %q", buf.String())
	}
}

// baseContext lets fakeHumaContext embed huma.Context under another
// field name than its Context method.
type baseContext huma.Context

// fakeHumaContext is just enough of a huma.Context for the error hook;
// its other methods panic.
type fakeHumaContext struct {
	baseContext
	ctx context.Context
}

func (f fakeHumaContext) Context() context.Context { return f.ctx }
func (f fakeHumaContext) Operation() *huma.Operation {
	return &huma.Operation{OperationID: "get-lobby"}
}
