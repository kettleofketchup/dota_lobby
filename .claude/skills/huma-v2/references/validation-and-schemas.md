# Validation and schemas

Source: `schema.go` (`SchemaFromField`, `schemaFromType`), `validate.go`, `registry.go`,
docs `features/request-validation.md`, `features/schema-customization.md`.

## How validation runs

1. At `Register`, huma builds a JSON Schema for every param and for `Body`, stored in
   `api.OpenAPI().Components.Schemas` (the registry). Bad tags panic at startup, not per request.
2. Per request: params are parsed and validated; the body is decoded to `any`, validated
   against the schema, then unmarshalled into the Go type (so custom `UnmarshalJSON` runs
   after validation and must agree with the schema).
3. `Resolve` methods on the input (and nested structs) run next.
4. All errors are collected (exhaustive) and returned as one 422 problem; the handler is
   not called. Responses are never validated.

## Tags (all in `SchemaFromField`)

| Tag | Example | Notes |
|-----|---------|-------|
| `json` | `json:"game_mode,omitempty"` | Wire name; `-` drops the field. Always set snake_case. |
| `doc` | `doc:"Lobby ID"` | Description (also promoted to param docs). |
| `example` | `example:"lb_123"` | JSON-parsed for non-strings. |
| `default` | `default:"50"` | Params: used when absent. Body: applied when the decoded field is **zero**, so `default:"true"` on a bool overwrites an explicit `false`. Avoid on bools/ints where zero is meaningful. |
| `enum` | `enum:"radiant,dire"` | Comma-separated; on slices applies to items. |
| `const` | `const:"v1"` | |
| `format` | `format:"date-time"` | `date`, `time`, `duration`, `email`, `hostname`, `ip`, `ipv4`, `ipv6`, `uri`, `uuid`, `regex`... |
| `timeFormat` | `timeFormat:"2006-01-02"` | `time.Time` layout; sets `format: date`/`time` for those layouts. |
| `minimum` / `maximum` | `minimum:"1"` | Also `exclusiveMinimum`, `exclusiveMaximum`, `multipleOf`. |
| `minLength` / `maxLength` | `maxLength:"64"` | Strings. |
| `pattern` | `pattern:"^[a-z0-9_]+$"` | Go regexp; pair with `patternDescription:"lowercase id"` for readable errors. |
| `minItems` / `maxItems` / `uniqueItems` | `maxItems:"10"` | Slices. |
| `minProperties` / `maxProperties` | | Maps/objects. |
| `required` | `required:"false"` | Overrides the default (below). |
| `dependentRequired` | `dependentRequired:"steam_id"` | Listed fields must exist (panic at startup if not). |
| `nullable` | `nullable:"true"` | Scalars only; on an object `$ref` it panics. |
| `readOnly` / `writeOnly` | `readOnly:"true"` | readOnly fields are not required on input; writeOnly not required on output. |
| `deprecated` / `hidden` | `hidden:"true"` | `hidden` removes the field from docs and from `required`. |
| `encoding` | `encoding:"base64"` | Sets `contentEncoding`. |

## Required vs optional

- Body/struct fields are **required by default**. Optional only with `omitempty`, `omitzero`
  or `required:"false"`. **Pointers do not make a field optional** (`*uint32` without
  `omitempty` is required; the client must send it, `null` included only if nullable).
- A pointer with `omitempty` is not nullable unless `nullable:"true"`.
- `query`/`header`/`cookie` params are optional by default; `path` always required.
- `cfg.FieldsOptionalByDefault = true` flips the body default API-wide. Do not use it in
  dota_lobby: explicit `omitempty` keeps the spec honest.
- Slices are nullable by default (`huma.DefaultArrayNullable = true`), so `[]T` documents
  as `["array","null"]`. Return `[]T{}` rather than nil if clients expect `[]`.

## Strictness

- Objects get `additionalProperties: false`: unknown body fields fail with
  `unexpected property` (422). Allow extras per struct with a blank field:

  ```go
  type WebhookPayload struct {
  	_     struct{} `json:"-" additionalProperties:"true"`
  	Event string   `json:"event"`
  }
  ```

  or API-wide with `cfg.AllowAdditionalPropertiesByDefault = true` before `humago.New`.
- Field names match case-insensitively by default (`huma.ValidateStrictCasing = false`).
- Unknown query params pass unless `RejectUnknownQueryParameters` is set.

## Custom validation: Resolver

Use only for rules tags cannot express (cross-field, lookups). Errors are `*huma.ErrorDetail`
with a `Location` like `body.teams[0].players`:

```go
func (in *CreateLobbyInput) Resolve(ctx huma.Context) []error {
	var errs []error
	if in.Body.Radiant.TeamRef == in.Body.Dire.TeamRef && in.Body.Radiant.TeamRef != "" {
		errs = append(errs, &huma.ErrorDetail{
			Location: "body.dire.team_ref", Message: "must differ from radiant", Value: in.Body.Dire.TeamRef,
		})
	}
	return errs
}

var _ huma.Resolver = (*CreateLobbyInput)(nil) // compile-time check
```

- Status is 422 unless an error implements `huma.StatusError` (last one wins), e.g.
  `huma.Error409Conflict("...")`.
- `ResolverWithPath` (`Resolve(ctx huma.Context, prefix *huma.PathBuffer) []error`) suits
  shared nested types; build locations with `prefix.With("field")`.
- Do not keep `ctx` after `Resolve` returns.

## Custom schema: SchemaProvider

When a type's JSON shape differs from its Go kind (string-or-number, custom marshal), give
the schema yourself so validation and the spec match `UnmarshalJSON`:

```go
type nameOrNumber string // accepts "cm" or 2

func (nameOrNumber) Schema(r huma.Registry) *huma.Schema {
	minLen, lo, hi := 1, 0.0, float64(math.MaxUint32)
	return &huma.Schema{
		Description: "A configured name or its wire number",
		OneOf: []*huma.Schema{
			{Type: huma.TypeString, MinLength: &minLen},
			{Type: huma.TypeInteger, Minimum: &lo, Maximum: &hi},
		},
	}
}
```

Without it, huma documents `type: string` and rejects the number form before
`UnmarshalJSON` ever runs. To tweak a generated schema instead, implement
`huma.SchemaTransformer` (`TransformSchema(r huma.Registry, s *huma.Schema) *huma.Schema`).
Types implementing `encoding.TextUnmarshaler` are documented as plain strings.

## Schema names and the registry

- `huma.DefaultSchemaNamer` uses the Go type name without package (`LobbyView`); generics
  drop brackets (`Page[LobbyView]` gives `PageLobbyView`). An inline `Body struct{...}`
  is named after its parent (`CreateLobbyInput` gives `CreateLobbyInputBody`); inside an
  anonymous input/output struct it becomes `<operationId>Request`/`Response`
  (`Put-lobby-noteRequest`), so changing an operation ID renames it. Name input/output
  structs, or use named body types, to keep schema names stable.
- Two types with the same name in different packages collide: rename one, or set
  `nameHint` on the `Body` field.
- Look up a generated schema: `api.OpenAPI().Components.Schemas.Map()["LobbyView"]`.
- Validate an arbitrary value against a type (outside a request):
  `huma.NewModelValidator().Validate(reflect.TypeOf(T{}), v)` returns `[]error`.
