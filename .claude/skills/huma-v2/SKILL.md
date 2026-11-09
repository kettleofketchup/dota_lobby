---
name: huma-v2
description: "Build dota_lobby REST endpoints with huma v2.40 on net/http (humago): operations, input/output structs, validation tags, OpenAPI spec, problem+json errors, API-key auth, humatest."
---

# huma v2 for dota_lobby

Huma (`github.com/danielgtaylor/huma/v2`, use v2.40.0: `go get github.com/danielgtaylor/huma/v2@v2.40.0`) turns typed Go handlers into
HTTP operations, validates requests against JSON Schema generated from struct tags, and
emits an OpenAPI 3.1 document. Use this skill whenever adding, converting or reviewing an
endpoint in `pkg/api`, touching the OpenAPI spec, or writing API tests.

Read the module source when in doubt: `$(go env GOMODCACHE)/github.com/danielgtaylor/huma/v2@v2.40.0`
(file names in the references are relative to it; official docs under `docs/docs/`).
Never guess a tag or field; grep `schema.go` / `huma.go`.

## Core pattern

```go
mux := http.NewServeMux()                       // stdlib mux, Go 1.22 "METHOD /path" patterns
cfg := huma.DefaultConfig("dota_lobby", "1.0.0") // OpenAPI 3.1, JSON, /openapi, /docs, /schemas
cfg.CreateHooks = nil                           // no "$schema" field or Link header in bodies
api := humago.New(mux, cfg)                     // adapters/humago; registers "GET /v1/..." on mux
api.UseMiddleware(requireKey(api, key))         // BEFORE any Register: captured at registration

huma.Register(api, huma.Operation{
	OperationID: "get-lobby",                      // stable, kebab-case, unique (dup = panic)
	Method:      http.MethodGet,
	Path:        "/v1/lobbies/{id}",
	Summary:     "Get a lobby",
	Tags:        []string{"Lobbies"},
	Security:    keyAuth,                          // []map[string][]string{{"apiKey": {}}, {"bearer": {}}}
	Errors:      []int{http.StatusNotFound},       // documents 404 + auto 422/500
}, func(ctx context.Context, in *GetLobbyInput) (*LobbyOutput, error) {
	l, err := svc.Get(ctx, in.ID)
	if errors.Is(err, lobby.ErrNotFound) {
		return nil, coded(http.StatusNotFound, "lobby_not_found", "no such lobby")
	}
	if err != nil {
		return nil, fmt.Errorf("get lobby %s: %w", in.ID, err) // becomes a redacted 500
	}
	return &LobbyOutput{Body: view(l)}, nil
})

type GetLobbyInput struct {
	ID string `path:"id" doc:"Lobby ID"`              // path params are always required
}
type LobbyOutput struct {
	ETag string `header:"ETag"`                       // every non-Body/Status field is a header
	Body LobbyView                                   // JSON response body
}
```

- Handler signature is always `func(context.Context, *I) (*O, error)`; `I` and `O` must be structs.
- Input fields: `path`, `query`, `header`, `cookie` tags, plus `Body` (JSON) or `RawBody` (bytes/multipart).
- Output fields: `Body`, optional `Status int`, everything else is a response header.
- Status: 200 with a Body, 204 without; set `DefaultStatus: http.StatusCreated` for creates.
- Return `nil, nil` for a bodiless success (204 / `DefaultStatus`).
- Prefer `huma.Register` with an explicit `OperationID`. `huma.Get/Post/Put/Patch/Delete`
  generate IDs from the path (`GET /v1/lobbies/{id}` becomes `get-v1-lobbies-by-id`); if used,
  pass `func(o *huma.Operation) { o.OperationID = "..." }` as the last argument.

## dota_lobby conventions

1. **Router**: one `*http.ServeMux`; huma mounts on it via `humago.New`. Legacy handlers stay
   on the same mux until converted. Never register the same `METHOD /path` twice: the mux panics.
2. **Paths**: Go 1.22 wildcards `{name}`; all versioned routes live under `/v1/`.
3. **Operation IDs**: kebab-case `verb-noun` (`create-lobby`, `list-lobby-events`). Treat them as
   public API: client generators key on them. Rename only with a changelog `upgrading` note.
4. **Auth**: document `apiKey` (header `X-API-Key`) and `bearer` (HTTP bearer) security schemes
   once in config; set `Security: keyAuth` on protected operations; a huma middleware enforces
   it by reading `ctx.Operation().Security` and comparing in constant time. Key comes from
   `config.Secret`; never log it. See `references/middleware-and-security.md`.
5. **Health stays public**: `/livez`, `/readyz`, `/health` have no `Security` and must keep
   working when the bot manager is down. They may stay plain handlers.
6. **Errors**: RFC 9457 `application/problem+json` via `huma.ErrorModel`, extended with a stable
   machine-readable `code` (`lobby_not_found`, `validation_failed`). Override `huma.NewError`
   once (package `init`), redact 5xx details, log them. See `references/errors.md`.
7. **Spec in git**: commit the generated `openapi.yaml`; a test regenerates it and fails on
   drift, CI runs it via `go test ./...`. See `references/openapi-and-docs.md`.
8. **JSON casing**: wire names are snake_case via explicit `json:"..."` tags on every field.
9. **Optional fields**: huma makes every body field required unless tagged `omitempty`,
   `omitzero` or `required:"false"`; pointers do NOT make a field optional.
10. **No docs UI from a CDN in production**: set `cfg.DocsPath = ""` unless explicitly wanted.

## Workflow: add or convert an endpoint

1. Define `XInput`/`XOutput` structs next to the handler in `pkg/api` with `json`, `doc`,
   validation and `example` tags.
2. Register with `huma.Register` in the server's route setup; keep the old path and method.
3. Delete the legacy `mux.HandleFunc` line for that pattern in the same change.
4. Map domain errors to `coded(status, code, msg)`; return wrapped errors for 5xx.
5. Add a table test (humatest for handler logic; real mux + httptest for auth/routing).
6. Run `go test ./... -run TestOpenAPI -update`, review the `openapi.yaml` diff, commit it.
7. Run `make test` and `make lint`; add a changelog fragment if the wire format changed.

## Quick task index

| Task | Reference |
|------|-----------|
| Operation fields, param/body tags, RawBody, multipart, headers, status, streaming | `references/operations-and-io.md` |
| Validation tags, required/optional rules, Resolver, SchemaProvider, strict bodies | `references/validation-and-schemas.md` |
| problem+json, `code` field, `huma.ErrorXXX`, `NewError` override, 5xx redaction | `references/errors.md` |
| Config paths, security schemes, 3.0 downgrade, write spec to file, CI drift check | `references/openapi-and-docs.md` |
| Router vs huma middleware, API-key enforcement, `WriteErr`, groups, context values | `references/middleware-and-security.md` |
| humatest, real-mux httptest, table tests, asserting problem bodies | `references/testing.md` |
| SSE, ETags, autopatch, CBOR, body limits/timeouts, transformers, incremental migration | `references/advanced.md` |

## Gotchas that bite on conversion

- Validation failures are **422** (not 400); a missing required body is 400. Clients and
  tests expecting 400 must change, or the API docs must say so.
- Unknown JSON body fields are rejected (`additionalProperties: false`), echoing the whole
  object as `value`. Opt out per struct with ``_ struct{} `json:"-" additionalProperties:"true"` ``.
- Pointer types are not allowed for `path`/`query`/`header`/`cookie` fields (panic at start).
- Huma middleware runs only for matched huma operations; mux 404/405 stay plain text.
- `huma.NewError` is a package global: overriding it affects every API in the process, tests too.
- A plain `error` from a handler becomes 500 with `err.Error()` in `errors[].message`
  unless `NewError` redacts it (the dota_lobby override does).
