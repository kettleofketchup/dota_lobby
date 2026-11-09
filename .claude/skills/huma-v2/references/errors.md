# Errors: problem+json with a stable `code`

Source: `error.go`, `huma.go` (handler error path, `defineErrors`), docs `features/response-errors.md`.

## What huma does by default

- `huma.ErrorModel` is RFC 9457: `type`, `title`, `status`, `detail`, `instance`, plus
  `errors []*huma.ErrorDetail` (`message`, `location`, `value`).
- `ErrorModel.ContentType` turns `application/json` into `application/problem+json`.
- Helpers `huma.Error400BadRequest(msg, errs...)` ... `huma.Error511NetworkAuthenticationRequired`
  (every 4xx/5xx in `error.go`) and `huma.Status304NotModified()` all call `huma.NewError`.
- Handler returns an error that `errors.As` a `huma.StatusError` (`GetStatus() int` +
  `Error()`): that value **itself** is serialized with its status. Anything else becomes
  `NewErrorWithContext(ctx, 500, "unexpected error occurred", err)`, which by default
  puts `err.Error()` into `errors[0].message`: internal text leaks to clients.
- Parse/validation failures: 400 (missing/unparsable body), 413, 408, 422 (`validation failed`
  with one `ErrorDetail` per problem, `location` like `query.limit` or `body.teams[0]`).
- `huma.ErrorWithHeaders(err, http.Header{...})` adds response headers (e.g. `Retry-After`).

## dota_lobby error model

Install once, in package `api` (it replaces a package-level var; tests in the same binary
see it too):

```go
// APIError is RFC 9457 problem details plus a stable machine-readable code.
type APIError struct {
	huma.ErrorModel
	Code string `json:"code" doc:"Stable machine-readable error code" example:"lobby_not_found"`
}

var defaultCodes = map[int]string{
	http.StatusBadRequest:            "bad_request",
	http.StatusUnauthorized:          "unauthorized",
	http.StatusNotFound:              "not_found",
	http.StatusConflict:              "conflict",
	http.StatusRequestEntityTooLarge: "body_too_large",
	http.StatusUnprocessableEntity:   "validation_failed",
	http.StatusInternalServerError:   "internal",
	http.StatusServiceUnavailable:    "unavailable",
}

func init() {
	base := huma.NewError // stock constructor, captured before replacing it
	huma.NewError = func(status int, msg string, errs ...error) huma.StatusError {
		if status >= 500 {
			errs = nil // never echo internal error text to clients
		}
		em := base(status, msg, errs...).(*huma.ErrorModel)
		code := defaultCodes[status]
		if code == "" && status != 0 { // status 0: huma probing the type for the schema
			code = strings.ToLower(strings.ReplaceAll(http.StatusText(status), " ", "_"))
		}
		return &APIError{ErrorModel: *em, Code: code}
	}
	huma.NewErrorWithContext = func(ctx huma.Context, status int, msg string, errs ...error) huma.StatusError {
		if status >= 500 && len(errs) > 0 {
			slog.ErrorContext(ctx.Context(), "request failed",
				"op", ctx.Operation().OperationID, "status", status, "err", errors.Join(errs...))
		}
		return huma.NewError(status, msg, errs...)
	}
}

// coded returns a problem with an explicit stable code.
func coded(status int, code, msg string, errs ...error) huma.StatusError {
	e := huma.NewError(status, msg, errs...).(*APIError)
	e.Code = code
	return e
}
```

Why it works: embedding `huma.ErrorModel` (by value) promotes `Error`, `GetStatus`,
`ContentType` and `Add` to `*APIError`, and the schema generator merges embedded fields, so
the spec documents one `APIError` object with `code` required. `defineErrors` calls
`NewError(0, "")` at registration to find the type, so the override must tolerate status 0.

Resulting body (`Content-Type: application/problem+json`):

```json
{"title":"Not Found","status":404,"detail":"no lobby lb_9","code":"lobby_not_found"}
```

## Handler rules

1. Domain "expected" failures: `return nil, coded(http.StatusConflict, "lobby_full", "lobby is full")`.
   Keep codes snake_case, documented, never reused for a different meaning.
2. Field-level problems: pass details:
   `coded(400, "invalid_roster", "bad roster", &huma.ErrorDetail{Location: "body.teams[1]", Message: "duplicate player", Value: id})`.
3. Unexpected failures: return the wrapped error (`fmt.Errorf("launch %s: %w", id, err)`); the
   override logs it with the operation ID and the client sees a bare 500 `internal`.
   Do **not** build `huma.Error500InternalServerError("x", err)` yourself: that path skips
   `NewErrorWithContext`, so the error is redacted but never logged.
4. Do not make domain error types implement `GetStatus()`: huma would serialize the domain
   type itself instead of `APIError`. Map them in the handler (or one `toProblem(err)` helper).
5. 503/504 for GC or Steam unavailability: `coded(503, "gc_unavailable", "...")`, optionally
   wrapped with `huma.ErrorWithHeaders(e, http.Header{"Retry-After": {"5"}})`.
6. Declare non-default statuses in `Operation.Errors` (`[]int{404, 409}`) so the spec lists them.

## From middleware

Middleware cannot return errors; write one and stop the chain:

```go
huma.WriteErr(api, ctx, http.StatusUnauthorized, "missing or invalid API key")
return // do not call next
```

`WriteErr` goes through `NewErrorWithContext`, so `code` is filled from `defaultCodes`. It
cannot take a custom code; to send one, marshal a `coded(...)` value yourself (snippet in
`middleware-and-security.md`).

## Testing errors

Decode into a local struct and assert `status` and `code`, not `detail` text:

```go
var p struct{ Status int `json:"status"`; Code string `json:"code"` }
_ = json.Unmarshal(resp.Body.Bytes(), &p)
if resp.Code != 404 || p.Code != "lobby_not_found" { t.Fatalf("got %d %+v", resp.Code, p) }
```

## Migration note

The pre-huma envelope was `{"error":{"code","message","retryable"}}` with `application/json`.
Switching to problem+json changes the wire format: add a `changelog.d/upgrading/` note and
keep the old `code` strings so clients can map them.
