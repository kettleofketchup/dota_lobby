# OpenAPI spec, docs UI and drift checks

Source: `api.go` (`Config`, `NewAPI`, `registerDocsRoute`), `defaults.go` (`DefaultConfig`),
`openapi.go` (`OpenAPI`, `YAML`, `Downgrade`), docs `features/openapi-generation.md`,
`features/api-docs.md`, `features/json-schema-registry.md`.

## Configure

```go
cfg := huma.DefaultConfig("dota_lobby", APIVersion) // APIVersion: a const, NOT the build version
cfg.Info.Description = "Dota 2 lobby management via Steam bot accounts."
cfg.CreateHooks = nil   // drop the SchemaLinkTransformer: no "$schema" in bodies, no Link header
cfg.DocsPath = ""       // no docs UI (it loads JS from unpkg.com); set "/docs" only if wanted
cfg.SchemasPath = ""    // no /schemas/{name} route
cfg.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
	"apiKey": {Type: "apiKey", In: "header", Name: "X-API-Key", Description: "API key"},
	"bearer": {Type: "http", Scheme: "bearer", Description: "Same API key as a bearer token"},
}
cfg.Tags = []*huma.Tag{
	{Name: "Health", Description: "Liveness and readiness probes"},
	{Name: "Lobbies", Description: "Create and drive lobbies"},
}
api := humago.New(mux, cfg)
```

`DefaultConfig` gives: OpenAPI `3.1.0`, `OpenAPIPath: "/openapi"`, `DocsPath: "/docs"`
(Stoplight Elements; `huma.DocsRendererScalar` / `huma.DocsRendererSwaggerUI` via
`DocsRenderer`), `SchemasPath: "/schemas"`, JSON format, and a create hook that adds
`$schema` + `Link` to every struct response (and a `$schema` property to body schemas).

Keep the spec independent of runtime config: always declare both security schemes (even
when no API key is configured), do not derive `Servers` or `Info.Version` from config or
ldflags, or the committed spec will drift between machines.

Other knobs on `huma.Config`: `Servers` (`[]*huma.Server{{URL: "/"}}`), `Security`
(document-wide default requirement), `Extensions` (`x-` keys), `OpenAPI.Webhooks`
(document outbound webhooks), `RejectUnknownQueryParameters`, `NoFormatFallback`.
`humago.NewWithPrefix(mux, "/api", cfg)` mounts under a prefix and adds it as a server URL.

## Served routes

With `OpenAPIPath: "/openapi"` huma registers `GET /openapi.json`, `/openapi.yaml`,
`/openapi-3.0.json`, `/openapi-3.0.yaml` (3.0.3 downgrade for older generators). They are
plain adapter routes: huma middleware (auth) does not run on them, and each is rendered once
on first request then cached, so mutate `api.OpenAPI()` only before serving. Set
`OpenAPIPath: ""` to serve nothing and rely on the committed file.

## Generate the spec without a server

Everything is built at `Register` time, so constructing the API is enough:

```go
mux := http.NewServeMux()
api := humago.New(mux, cfg)
registerRoutes(api, deps)       // same function the server uses; stub deps are fine
b, err := api.OpenAPI().YAML()  // or json.Marshal(api.OpenAPI()); 3.0: .Downgrade()/.DowngradeYAML()
```

Output is deterministic (maps marshal in key order), so byte comparison works.
Operations with `Hidden: true` and schemas only they use are left out.

## Commit the spec and check drift in CI

Expose the API from the server (e.g. `func (s *Server) OpenAPI() *huma.OpenAPI`), build it
with zero-value `Options` (the no-op bot/league/lease defaults), and compare with a golden file:

```go
var updateSpec = flag.Bool("update", false, "rewrite openapi.yaml")

func TestOpenAPISpecUpToDate(t *testing.T) {
	got, err := New(Options{}).OpenAPI().YAML()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "..", "openapi.yaml") // repo root, from pkg/api
	if *updateSpec {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v: run go test ./pkg/api -run TestOpenAPISpecUpToDate -update", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("openapi.yaml is stale: run go test ./pkg/api -run TestOpenAPISpecUpToDate -update and commit it")
	}
}
```

- CI already runs `go test -race ./...` (`.github/workflows/test.yml`), so the check needs no
  new job. Optionally add a `just` recipe wrapping the `-update` command.
- Review the YAML diff in every API PR: renamed operation IDs, schema names, newly required
  fields and removed enums are breaking changes for generated clients.
- If a separate lint step is wanted, run an OpenAPI linter against the committed file, not
  the live server.

## Customize documentation per operation

- `Summary` (short, shown in lists), `Description` (CommonMark), `Tags`, `Deprecated`.
- Field docs come from `doc` and `example` tags; an `example` on the `Body` field is copied
  to the media type for tools that ignore examples next to `$ref`.
- Full override: set `Operation.RequestBody` or `Operation.Responses[...]` yourself before
  `Register`; huma only fills what is unset.
- Extra response codes without schemas: `Operation.Errors`; per-status custom content:
  `Responses: map[string]*huma.Response{"409": {Description: "Lobby full", ...}}`.

## Docs UI when enabled

Rendered HTML pulls a pinned script from unpkg.com with a strict CSP; it fails offline or
behind egress filtering. To self-host, leave `DocsPath` empty and serve your own page on
the mux that points at `/openapi.json`. `DocsRendererConfig` passes options to Scalar or
SwaggerUI (Stoplight ignores it).
