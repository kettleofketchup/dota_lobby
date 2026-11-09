# Testing huma endpoints

Source: `humatest/humatest.go`, docs `features/test-utilities.md`. CI runs
`go test -race -count=1 ./...`; run `make test` locally.

## Pick the harness

| Need | Harness |
|------|---------|
| Handler logic, validation, error mapping | `humatest.New(t)` + register the op(s) |
| Real `ServeMux` routing, huma middleware, real config | `humatest.Wrap(t, humago.New(mux, cfg))` |
| Router-level middleware, legacy routes, body cap, full server | `httptest.NewRecorder` + `srv.Handler().ServeHTTP`, or `httptest.NewServer` |

`humatest.New(t, cfgs...)` returns `(http.Handler, humatest.TestAPI)`. It is built on the
**humaflow** router (not `ServeMux`) with a bare JSON config: no `DefaultConfig` hooks, no
`/openapi` routes unless the passed config sets them. A custom config must set `OpenAPI`
(non-nil) or it panics. `huma.NewError` overrides still apply (package global).
`humago.Unwrap` panics inside `humatest.New`; use `Wrap` around humago for such code.

## Requests

`TestAPI` has `Get/Post/Put/Patch/Delete(path, args...)`, `Do(method, path, args...)`
and `...Ctx(ctx, ...)` variants, all returning `*httptest.ResponseRecorder`. Args:

- `"Header-Name: value"` strings set headers (`"X-API-Key: test"`),
- an `io.Reader` is sent as the raw body,
- a struct, map or slice is JSON-encoded with `Content-Type: application/json`,
- anything else panics.

Each request/response is dumped with `t.Log` (visible with `-v` or on failure).

## Table test over handler logic

```go
func TestGetLobby(t *testing.T) {
	_, api := humatest.New(t)
	s := &Server{lobbies: fakeLobbies{"lb_1": {ID: "lb_1"}}}
	s.registerLobbies(api) // same func the real server calls

	cases := []struct {
		name   string
		path   string
		status int
		code   string
	}{
		{"found", "/v1/lobbies/lb_1", http.StatusOK, ""},
		{"missing", "/v1/lobbies/lb_9", http.StatusNotFound, "lobby_not_found"},
		{"bad id", "/v1/lobbies/LB!", http.StatusUnprocessableEntity, "validation_failed"}, // pattern tag
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := api.Get(tc.path)
			if resp.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", resp.Code, tc.status, resp.Body)
			}
			if tc.code == "" {
				return
			}
			var p struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(resp.Body.Bytes(), &p); err != nil || p.Code != tc.code {
				t.Fatalf("code %q (err %v), want %q", p.Code, err, tc.code)
			}
		})
	}
}
```

Assert `status` and `code`; also check `Content-Type` is `application/problem+json` once.
Do not assert `detail` text or huma's validation messages (they are library-owned).

## Full server through the real mux

```go
func TestAuthMatrix(t *testing.T) {
	srv := New(Options{APIKey: "k"})
	h := srv.Handler()
	for _, tc := range []struct {
		method, path, key string
		want              int
	}{
		{"GET", "/livez", "", 200},             // health stays public
		{"GET", "/v1/bots", "", 401},
		{"GET", "/v1/bots", "k", 200},
		{"PUT", "/v1/bots", "k", 405},          // from the mux/legacy fallback, not huma
		{"GET", "/openapi.json", "", 200},      // only while OpenAPIPath is set
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		if tc.key != "" {
			req.Header.Set("X-API-Key", tc.key)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s %s key=%q: got %d want %d", tc.method, tc.path, tc.key, rec.Code, tc.want)
		}
	}
}
```

Also cover `Authorization: Bearer k` (scheme case-insensitive) and an oversized body (413).

## Spec-level tests (cheap, no HTTP)

```go
func TestProtectedOperations(t *testing.T) {
	public := map[string]bool{"get-livez": true, "get-readyz": true, "get-health": true}
	for path, item := range New(Options{}).OpenAPI().Paths {
		for _, op := range []*huma.Operation{item.Get, item.Post, item.Put, item.Patch, item.Delete} {
			if op == nil || public[op.OperationID] {
				continue
			}
			if len(op.Security) == 0 {
				t.Errorf("%s %s (%s) has no Security", op.Method, path, op.OperationID)
			}
		}
	}
}
```

Pair it with the golden-spec drift test in `openapi-and-docs.md`.

## Tips

- Build the API per test (or per subtest group); registration is cheap and avoids shared
  state. Never register the same op twice on one API (duplicate ID panics).
- Registration panics (bad tags, pointer params, duplicate pattern) surface as test panics:
  a test that just constructs the server catches them early.
- For SSE, `api.Get` returns once the handler finishes; read `resp.Body.String()` and
  assert on `event:`/`data:` lines.
- Keep `-race` clean: handlers must not mutate shared input structs after returning.
