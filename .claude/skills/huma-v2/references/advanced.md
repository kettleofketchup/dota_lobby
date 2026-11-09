# Advanced: streaming, conditional requests, formats, limits, migration

Source: `sse/sse.go`, `conditional/params.go`, `autopatch/autopatch.go`, `formats/cbor`,
`humacli/humacli.go`, `transforms.go`, `api.go`, docs `features/server-sent-events-sse.md`,
`features/conditional-requests.md`, `features/request-limits.md`, `features/response-transformers.md`.

## Server-Sent Events (`sse.Register`)

```go
type LobbyEvent struct { Seq int64 `json:"seq"`; Kind string `json:"kind"` }
type LobbyClosed LobbyEvent // each event name needs its own Go type

sse.Register(api, huma.Operation{
	OperationID: "stream-lobby-events", Method: http.MethodGet,
	Path: "/v1/lobbies/{id}/stream", Security: keyAuth, Tags: []string{"Lobbies"},
}, map[string]any{"event": LobbyEvent{}, "closed": LobbyClosed{}},
	func(ctx context.Context, in *LobbyIDInput, send sse.Sender) {
		for ev := range subscribe(ctx, in.ID) { // must stop when ctx is done (client left)
			if err := send(sse.Message{IDString: strconv.FormatInt(ev.Seq, 10), Data: ev}); err != nil {
				return
			}
		}
	})
```

- The event name comes from the Go type of `Data`; an unmapped type is logged to stderr
  and sent without `event:`. `"message"` is the default and is not transmitted.
- `send.Data(v)` for data only; `send.Comment("ping")` for heartbeats.
- Each send sets a write deadline of `sse.WriteTimeout` (5s) and flushes; humago's writer
  supports both. The `http.Server.WriteTimeout` would otherwise cut long streams.
- The SSE handler cannot return an error and the 200 is committed before it runs. Do
  existence checks (lobby exists) in an `Operation.Middlewares` entry that calls
  `huma.WriteErr(api, ctx, 404, ...)` (read the raw `ctx.Param("id")`; params are not
  parsed yet), so a missing lobby gets a real 404 problem.
- Keep the existing polling `GET /v1/lobbies/{id}/events?after=` as a normal operation.

## Conditional requests (ETag)

Embed `conditional.Params` in the input to get `If-Match`, `If-None-Match`,
`If-Modified-Since`, `If-Unmodified-Since` headers:

```go
type PatchLobbyInput struct {
	conditional.Params
	ID   string `path:"id"`
	Body PatchLobbyBody
}
// handler:
cur, _ := svc.Get(ctx, in.ID)
if in.HasConditionalParams() {
	if err := in.PreconditionFailed(cur.ETag(), cur.UpdatedAt); err != nil {
		return nil, err // 412 on writes, 304 on reads
	}
}
```

Return the ETag via an output field ``ETag string `header:"ETag"` ``. Note the 304/412 come
from `huma.NewError`, so they carry the dota_lobby `code` too.

## autopatch

`autopatch.AutoPatch(api)` (call after all registrations) adds a PATCH (merge-patch,
JSON Patch, shorthand) for every path that has GET + PUT but no PATCH. dota_lobby defines its
own `PATCH /v1/lobbies/{id}` and no PUT, so do not use it; opt out per op with
`Metadata: map[string]any{"autopatch": false}` if a PUT is ever added.

## Formats and content negotiation

- JSON only by default (`huma.DefaultFormats`). The `Accept` header picks the format;
  unknown types fall back to JSON unless `cfg.NoFormatFallback` (then 406).
- CBOR: blank-import `github.com/danielgtaylor/huma/v2/formats/cbor` (adds
  `fxamacker/cbor` to go.mod; registers into `DefaultFormats` in `init`). Not needed here.
- Request `Content-Encoding` (gzip) is not decoded by huma or humago.
- `negotiation.SelectQValue(header, allowed)` is the helper huma uses, if a handler must
  pick a representation itself.

## Limits and timeouts

- Per op: `MaxBodyBytes` (default 1 MiB, 413) and `BodyReadTimeout` (default 5s, 408).
  The existing router-level `http.MaxBytesReader` (`Options.MaxBodyBytes`) still applies on
  top; the smaller limit wins, and huma reports either as 413 problem+json.
- Server: keep `ReadHeaderTimeout`, `IdleTimeout` etc. on `http.Server`.
- Downstream (GC/Steam) deadlines: derive from the handler `ctx`
  (`context.WithTimeout(ctx, d)`), map `context.DeadlineExceeded` to 504 via `coded(...)`.
- Raise read deadlines for slow uploads in a `Resolve` with `ctx.SetReadDeadline(...)`.

## Transformers and hooks

- `huma.Transformer` (`func(ctx huma.Context, status string, v any) (any, error)`) rewrites
  response bodies before marshalling; add with `cfg.Transformers` or `group.UseTransformer`.
  Avoid for business logic; it is how `$schema` injection works.
- `cfg.OnAddOperation = append(cfg.OnAddOperation, func(o *huma.OpenAPI, op *huma.Operation) {...})`
  runs on every documented operation: good for enforcing conventions at startup (panic if
  `OperationID` is empty or not kebab-case, or a non-health op lacks `Security`).
- `huma.NewGroup` modifiers can rewrite operations before registration.
- `cfg.CreateHooks` run inside `huma.NewAPI` and may edit the final config.

## humacli

`humacli.New` wires cobra flags/env vars and start/stop hooks. dota_lobby already uses
Viper + its own `cmd/dota_lobby`, so do not adopt it. If a spec-dump command is wanted, add
an `openapi` subcommand that builds the API and prints `api.OpenAPI().YAML()`.

## Incremental migration of `pkg/api`

The server already uses one `*http.ServeMux` with Go 1.22 patterns, so huma mounts beside the
legacy handlers:

1. In `routes()`, create `api := humago.New(r.mux, cfg)` and `api.UseMiddleware(...)` first.
2. Move one resource at a time: delete its `r.handleAuthed("GET /v1/bots", ...)` lines and
   register huma operations with the same method and path. Registering both panics
   (`pattern ... conflicts`), which catches forgotten deletions at startup.
3. Keep the 405 fallbacks. The mux's catch-all `/` swallows wrong-method requests as 404
   unless a path-only pattern exists, so feed huma routes into the existing fallback table:
   walk `api.OpenAPI().Paths` after registration and record each method/path (protected
   when `op.Security` is non-empty) before `addMethodFallbacks()`.
4. Health routes (`/livez`, `/readyz`, `/health`) can stay plain handlers; if converted,
   keep them public and their status semantics (503 when not ready via `Status int`).
5. Legacy error envelope and huma problem+json coexist during migration; finish a whole
   resource per PR so one resource never mixes shapes.
6. GET operations also answer HEAD through the mux (body discarded).
7. After each step: `make test`, regenerate and commit `openapi.yaml`, changelog fragment.
