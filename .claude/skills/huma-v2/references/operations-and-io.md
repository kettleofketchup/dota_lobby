# Operations, inputs and outputs

Source: `huma.go` (`Register`, `processInputType`, `processOutputType`), `openapi.go` (`Operation`).

## huma.Operation fields worth setting

| Field | Use |
|-------|-----|
| `OperationID` | Stable kebab-case ID. Required by convention; duplicates panic in `AddOperation`. Spaces become `-`. |
| `Method`, `Path` | `http.MethodPost`, `"/v1/lobbies/{id}"`. Both required (panic otherwise). |
| `Summary`, `Description`, `Tags` | Docs. `Description` takes CommonMark. One tag per resource (`Lobbies`, `Bots`). |
| `DefaultStatus` | Success status. Default 200 with `Body`, 204 without (HEAD: 200). |
| `Errors` | Error statuses to document; huma also adds 422 (if params/body) and 500. Omit to get one `default` error response. |
| `Security` | `[]map[string][]string`; alternatives are OR-ed. Empty means public. |
| `MaxBodyBytes` | Default 1 MiB for JSON/raw bodies, `-1` unlimited; multipart has no default. Over limit gives 413. |
| `BodyReadTimeout` | Default 5s for bodies, `-1` disables the server deadline; timeout gives 408. |
| `Middlewares` | `huma.Middlewares{fn}` run after API-level middleware, for this op only. |
| `Hidden` | Served but omitted from the spec (and its sole-use schemas). Use sparingly. |
| `Metadata` | `map[string]any` read by middleware/modifiers (e.g. `"autopatch": false`). |
| `SkipValidateParams`, `SkipValidateBody` | Turn off validation; avoid. |
| `RejectUnknownQueryParameters` | 422 on unknown `?foo=`; also settable API-wide in `Config`. |
| `Deprecated`, `Extensions` | Spec only (`Extensions` keys are emitted verbatim, e.g. `x-...`). |

`huma.Get(api, path, h, opts...)` and friends build the Operation for you; the generated ID
is `GenerateOperationID` (`GET` with a slice body becomes `list-...`). Override inline:

```go
huma.Get(api, "/v1/bots", h, func(o *huma.Operation) {
	o.OperationID = "list-bots"
	o.Tags = []string{"Bots"}
	o.Security = keyAuth
})
```

`huma.OperationTags("Bots")` is a ready-made option for tags only.

## Input struct

```go
type ListLobbiesInput struct {
	Limit  int      `query:"limit" default:"50" minimum:"1" maximum:"200" doc:"Page size"`
	State  []string `query:"state" enum:"open,launched,closed"`  // ?state=open,closed
	After  int64    `query:"after" doc:"Event cursor"`
	Trace  string   `header:"X-Request-Id" maxLength:"64"`
	Region string   `cookie:"region"`
}
```

| Tag | Meaning |
|-----|---------|
| `path:"id"` | Path wildcard; always required (a `required:"false"` is ignored). |
| `query:"name"` | Optional unless `required:"true"`. Slices are comma-separated; add `,explode` (`query:"tag,explode"`) for `?tag=a&tag=b`; `,deepObject` for `?filter[x]=1` into a struct/map. |
| `header:"Name"` | Optional unless `required:"true"`. `time.Time` uses HTTP-date format. |
| `cookie:"name"` | Optional unless `required:"true"`. Type `http.Cookie` receives the whole cookie. |
| `default:"50"` | Used when the param is absent/empty. |
| `timeFormat:"2006-01-02"` | Parse layout for `time.Time` params. |
| `hidden:"true"` | Parsed but undocumented. |
| `doc`, `example`, `enum`, `minimum`, `maxLength`, `pattern`, `format`... | Schema + validation; see `validation-and-schemas.md`. |

Supported param types: `bool`, ints, uints, floats, `string`, `time.Time`, slices of those,
types implementing `encoding.TextUnmarshaler`. **Pointer fields panic** at registration; use
the zero value plus `default`, or wrap in a custom type implementing `huma.ParamWrapper`.

An embedded struct shares params across inputs (e.g. `conditional.Params`, a pagination struct).

### Bodies

```go
type CreateLobbyInput struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Body CreateLobbyBody                    // JSON; required unless pointer/interface or required:"false"
	RawBody []byte                          // optional: the exact bytes Body was parsed from
}
```

- `Body` tags: `contentType:"application/merge-patch+json"`, `nameHint:"CreateLobbyRequest"`
  (schema name), `required:"true"` (force a pointer body to be required).
- Missing required body gives **400** `request body is required`; schema failures give **422**.
- `RawBody []byte` **without** `Body` skips parsing/validation; set `contentType:"text/plain"`
  (default `application/octet-stream`). Default size/time limits still apply.
- Multipart: `RawBody multipart.Form` (manual) or typed:

```go
RawBody huma.MultipartFormFiles[struct {
	Replay huma.FormFile `form:"replay" contentType:"application/octet-stream" required:"true"`
	Note   string        `form:"note"`
}]
// in the handler: f := in.RawBody.Data().Replay; f.Filename, f.Size, f.IsSet; read f.File
```

Struct/map form fields need `contentType:"application/json"` (panic otherwise). Multipart
has no default size limit: set `MaxBodyBytes` explicitly.

## Output struct

```go
type CreateLobbyOutput struct {
	Location string      `header:"Location"`
	Links    []string    `header:"Link"`           // slice: one appended header per item
	Modified *time.Time  `header:"Last-Modified"`  // nil skipped; a zero time.Time is NOT
	Cookie   http.Cookie `header:"Set-Cookie"`
	Body     LobbyView
}
```

- **Every exported field other than `Body` and `Status` is a response header**, named by the
  field name when there is no `header` tag. Keep internal data out of output structs.
- Only empty strings and nil pointers are skipped. A zero `time.Time` is written as
  `0001-01-01 00:00:00 +0000 UTC`, and ints/bools always appear (`X-N: 0`): use pointers
  for optional headers.
- `Status int` overrides `DefaultStatus` per response (only `DefaultStatus` is documented).
- `Body []byte` writes raw bytes; add ``ContentType string `header:"Content-Type"` ``.
- `Body func(huma.Context)` streams: write via `ctx.BodyWriter()`. Or return
  `*huma.StreamResponse{Body: func(ctx huma.Context) {...}}` as the whole output.
- Returning `nil, nil` writes just `DefaultStatus`.
- Response headers are documented on the `DefaultStatus` response only.

## Register many handlers from one type

`huma.AutoRegister(api, srv)` calls every `srv.RegisterXxx(api huma.API)` method. Fine for
grouping, but explicit calls in one `routes()` keep registration order obvious (middleware
must already be attached).
