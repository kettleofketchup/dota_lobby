# Middleware, security and groups

Source: `api.go` (`Context`, `UseMiddleware`, `WithValue`), `chain.go`, `group.go`,
`adapters/humago`, docs `features/middleware.md`, `features/groups.md`, `how-to/oauth2-jwt.md`.

## Two layers

| Layer | Signature | Runs for | Use for |
|-------|-----------|----------|---------|
| Router (net/http) | `func(http.Handler) http.Handler` wrapping the mux | every request: 404s, spec routes, legacy handlers | request IDs, access logs, panic recovery, global body cap, CORS |
| Huma | `func(ctx huma.Context, next func(huma.Context))` | only matched huma operations, before param parsing/validation | auth from `ctx.Operation()`, per-op policies, context values |

```go
handler := withRequestID(withRecover(mux)) // router layer: wrap the mux you serve
api.UseMiddleware(requireKey(api, key))    // huma layer
```

Order rules (verified in `huma.go` `register`):
- `api.UseMiddleware` must be called **before** `huma.Register`; the chain is captured per
  operation at registration. Middleware added later silently does nothing for earlier ops.
- Execution: API middlewares in order added, then group middlewares, then
  `Operation.Middlewares`, then parsing/validation/resolvers, then the handler.
- To stop the chain, write a response and do not call `next`.

## API-key enforcement (dota_lobby)

Document once, require per operation, enforce in one middleware:

```go
var keyAuth = []map[string][]string{{"apiKey": {}}, {"bearer": {}}} // either satisfies

func requireKey(api huma.API, key config.Secret) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		if !key.IsSet() || len(ctx.Operation().Security) == 0 { // public op or auth disabled
			next(ctx)
			return
		}
		if !validKey(key, presentedKey(ctx)) { // sha256 both + subtle.ConstantTimeCompare
			ctx.SetHeader("WWW-Authenticate", `Bearer realm="dota_lobby"`)
			huma.WriteErr(api, ctx, http.StatusUnauthorized, "missing or invalid API key")
			return
		}
		next(ctx)
	}
}

func presentedKey(ctx huma.Context) string {
	if k := ctx.Header("X-API-Key"); k != "" {
		return k
	}
	scheme, tok, ok := strings.Cut(ctx.Header("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(tok)
}
```

- `Security` on the operation is both the docs and the switch: an op without it is public.
  Health endpoints (`/livez`, `/readyz`, `/health`) must never get it.
- Add a test asserting every operation except the health ones has `Security` set, by
  walking `api.OpenAPI().Paths`, so a forgotten `Security` fails CI rather than ships open.
- Never log the presented or configured key; `config.Secret` redacts itself.
- Spec routes (`/openapi.*`) bypass huma middleware: they are public unless wrapped at the
  router layer or disabled.

## Writing errors from middleware

`huma.WriteErr(api, ctx, status, msg, errs...)` marshals the configured error model
(problem+json with `code` in dota_lobby) using content negotiation. For a non-default code:

```go
e := coded(http.StatusForbidden, "key_revoked", "API key revoked")
ctx.SetHeader("Content-Type", "application/problem+json")
ctx.SetStatus(e.GetStatus())
_ = api.Marshal(ctx.BodyWriter(), "application/json", e)
```

Set headers before `SetStatus` (it calls `WriteHeader` on the underlying writer).

## Per-operation middleware and metadata

```go
huma.Register(api, huma.Operation{
	OperationID: "launch-lobby", Method: http.MethodPost, Path: "/v1/lobbies/{id}/launch",
	Security:    keyAuth,
	Metadata:    map[string]any{"rateLimit": "launch"},
	Middlewares: huma.Middlewares{auditLog},
}, launchHandler)
```

Global middleware can branch on `ctx.Operation().Metadata["rateLimit"]` instead of paths.

## Context values

```go
type ctxKey struct{}

func withCaller(ctx huma.Context, next func(huma.Context)) {
	next(huma.WithValue(ctx, ctxKey{}, callerFrom(ctx)))
}
// in the handler: caller, _ := ctx.Value(ctxKey{}).(Caller)
```

`huma.WithValue` / `huma.WithContext` propagate to the `*http.Request` context, so router
code that unwraps sees them. Use unexported key types, never strings.

Raw access when unavoidable: `r, w := humago.Unwrap(ctx)` (panics on a non-humago context,
e.g. inside `humatest.New`). Cookies: `huma.ReadCookie(ctx, "name")`, `huma.ReadCookies(ctx)`.

## Groups

```go
v1 := huma.NewGroup(api, "/v1")               // paths below are relative to /v1
v1.UseMiddleware(requireKey(api, key))        // group-only middleware, still before Register
v1.UseSimpleModifier(func(o *huma.Operation) { o.Security = keyAuth })

huma.Register(v1, huma.Operation{
	OperationID: "list-bots", Method: http.MethodGet, Path: "/bots", Tags: []string{"Bots"},
}, listBots) // served and documented as GET /v1/bots
```

- `UseModifier(func(o *huma.Operation, next func(*huma.Operation)))` can skip (`next` not
  called) or fan out an operation; `UseSimpleModifier` just edits it.
- `UseTransformer` adds response transformers for the group only.
- `NewGroup(api, "/v1", "/v2")` registers every op under both prefixes **and rewrites
  operation IDs/tags** (`v1-list-bots`, `v2-list-bots`). Avoid multi-prefix groups: IDs
  must stay stable.
- Groups nest: `huma.NewGroup(v1, "/admin")`.
- Generated IDs from `huma.Get(...)` are recomputed with the prefix; explicit IDs are kept.
