package api

import (
	"encoding/json"
	"math"
	"net/http"
	"reflect"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/kettleofketchup/dota_lobby/pkg/lobby"
	"github.com/kettleofketchup/dota_lobby/pkg/webhook"
)

// APIVersion is the version of the /v1 API in the OpenAPI document. It is
// fixed here, not taken from the build, so the committed spec matches
// every build.
const APIVersion = "1.0.0"

// keyAuth is the security requirement of every /v1 operation: the API key
// in X-API-Key, or the same key as a bearer token.
var keyAuth = []map[string][]string{{"apiKey": {}}, {"bearer": {}}}

// apiConfig is the huma configuration. It does not depend on Options, so
// the generated spec is the same whatever the runtime config.
func apiConfig() huma.Config {
	cfg := huma.DefaultConfig("dota_lobby", APIVersion)
	cfg.Info.Description = "Dota 2 lobby management through Steam bot accounts. " +
		"Errors are RFC 9457 problem details (application/problem+json) with a stable `code`. " +
		"The /v1 operations need the API key when the service has one configured; " +
		"/livez, /readyz and /health are always open and not part of this document."
	// No "$schema" field in bodies and no Link header.
	cfg.CreateHooks = nil
	// The docs UI loads its script from a CDN; the spec is served instead.
	cfg.DocsPath = ""
	cfg.SchemasPath = ""
	// The server serves the spec itself (see specHandler).
	cfg.OpenAPIPath = ""
	cfg.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"apiKey": {Type: "apiKey", In: "header", Name: "X-API-Key", Description: "The API key (secrets.api_key)."},
		"bearer": {Type: "http", Scheme: "bearer", Description: "The same API key as a bearer token."},
	}
	cfg.Tags = []*huma.Tag{
		{Name: "Bots", Description: "The Steam bot accounts and their state."},
		{Name: "Leagues", Description: "The leagues the bots may host."},
		{Name: "Lobbies", Description: "Create and drive lobbies."},
	}
	return cfg
}

// documentWebhook adds the event delivery to the spec's webhooks section,
// so a receiver can generate its types from the same document.
func documentWebhook(oapi *huma.OpenAPI) {
	ref := oapi.Components.Schemas.Schema(reflect.TypeFor[LobbyEvent](), true, "")
	str := &huma.Schema{Type: huma.TypeString}
	header := func(name, doc string) *huma.Param {
		return &huma.Param{Name: name, In: "header", Required: true, Description: doc, Schema: str}
	}
	oapi.Webhooks = map[string]*huma.PathItem{
		"lobby-event": {Post: &huma.Operation{
			OperationID: "lobby-event",
			Summary:     "A lobby event",
			Description: "One event, POSTed to every configured webhook URL, in order per lobby, " +
				"retried until the receiver answers 2xx. The body is the same envelope " +
				"GET /v1/lobbies/{id}/events lists. Verify the signature over the raw body.",
			Tags: []string{"Lobbies"},
			Parameters: []*huma.Param{
				header(webhook.HeaderEventID, "The event ID; the same on every retry."),
				header(webhook.HeaderEventType, "The event type, as in the body."),
				header(webhook.HeaderTimestamp, "When this attempt was signed, in Unix seconds."),
				header(webhook.HeaderSignature, `"v1=" and the hex HMAC-SHA256 of "<timestamp>.<body>" under the webhook secret; several comma-separated during a secret rotation.`),
			},
			RequestBody: &huma.RequestBody{
				Required: true,
				Content:  map[string]*huma.MediaType{"application/json": {Schema: ref}},
			},
			Responses: map[string]*huma.Response{
				"2XX":     {Description: "Delivered."},
				"default": {Description: "Retried with backoff (408, 425, 429, 5xx, no answer) or given up (3xx, other 4xx)."},
			},
		}},
	}
}

// OpenAPI returns the server's OpenAPI document.
func (s *Server) OpenAPI() *huma.OpenAPI { return s.api.OpenAPI() }

// nameOrNumber is a game mode or region in a request: a configured name or
// a wire number, given as a JSON string or number.
type nameOrNumber string

func (n *nameOrNumber) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*n = nameOrNumber(s)
		return nil
	}
	var v uint32
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*n = nameOrNumber(strconv.FormatUint(uint64(v), 10))
	return nil
}

// Schema documents both forms, so validation accepts what UnmarshalJSON
// does.
func (nameOrNumber) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{
		Description: "A name configured in lobby.game_modes or lobby.regions, or the wire number.",
		OneOf:       []*huma.Schema{{Type: huma.TypeString}, uint32Schema()},
	}
}

// nameValue is a game mode or region in a response, shown as given: a
// number stays a number; empty is null.
type nameValue string

func (n nameValue) MarshalJSON() ([]byte, error) {
	if n == "" {
		return []byte("null"), nil
	}
	if _, err := strconv.ParseUint(string(n), 10, 32); err == nil {
		return []byte(n), nil
	}
	return json.Marshal(string(n))
}

func (nameValue) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{
		Description: "The name as requested, or the wire number; null when unset.",
		OneOf:       []*huma.Schema{{Type: huma.TypeString}, uint32Schema(), {Type: huma.TypeNull}},
	}
}

func uint32Schema() *huma.Schema {
	lo, hi := 0.0, float64(math.MaxUint32)
	return &huma.Schema{Type: huma.TypeInteger, Minimum: &lo, Maximum: &hi}
}

// eventData is an event's type-specific data, passed through as stored.
type eventData json.RawMessage

func (d eventData) MarshalJSON() ([]byte, error) {
	if len(d) == 0 {
		return []byte("{}"), nil
	}
	return d, nil
}

func (eventData) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{
		Type:                 huma.TypeObject,
		AdditionalProperties: true,
		Description:          "Depends on the event type (see the webhooks documentation). Never the password.",
	}
}

// LobbySummary is the lobby part of an event: enough for a receiver to
// find its own record. It never holds the password.
type LobbySummary struct {
	ID          string            `json:"id" doc:"The lobby ID" example:"lob_01JC9Z7Q8W5X2M3N4P5R6S7T8V"`
	ExternalRef string            `json:"external_ref" doc:"The caller's reference from the create request"`
	State       lobby.State       `json:"state" doc:"The state the lobby moved to with this event"`
	Bot         *string           `json:"bot" nullable:"true" doc:"The hosting bot, null until assigned"`
	GCLobbyID   *string           `json:"gc_lobby_id" nullable:"true" doc:"The game coordinator's lobby ID (64-bit, as a string), null until created"`
	LeagueID    *uint32           `json:"league_id" nullable:"true" doc:"The league, null for none"`
	Metadata    map[string]string `json:"metadata" doc:"The caller's metadata from the create request"`
}

// LobbyEvent is one lobby event: what a webhook delivers and what the
// events endpoint lists.
type LobbyEvent struct {
	ID         string       `json:"id" doc:"Unique per event; the same on every delivery attempt" example:"evt_01JC9ZB2J4K6M8N0P2Q4R6S8T0"`
	Type       string       `json:"type" doc:"The event type, e.g. lobby.state_changed, lobby.player_joined, match.started, match.ended" example:"lobby.state_changed"`
	Seq        int64        `json:"seq" doc:"The event's number within its lobby: starts at 1, grows by one, no gaps"`
	OccurredAt time.Time    `json:"occurred_at" doc:"When the change happened (UTC)"`
	Lobby      LobbySummary `json:"lobby" doc:"The lobby right after the change"`
	Data       eventData    `json:"data"`
}

func newLobbyEvent(e lobby.Envelope) LobbyEvent {
	s := e.Lobby
	if s.Metadata == nil {
		s.Metadata = map[string]string{}
	}
	return LobbyEvent{
		ID: e.ID, Type: e.Type, Seq: e.Seq, OccurredAt: e.OccurredAt,
		Lobby: LobbySummary{
			ID: s.ID, ExternalRef: s.ExternalRef, State: s.State, Bot: s.Bot,
			GCLobbyID: s.GCLobbyID, LeagueID: s.LeagueID, Metadata: s.Metadata,
		},
		Data: eventData(e.Data),
	}
}

// alsoAnswers documents a second success status of an operation with the
// default status's body, e.g. the 200 of a create that was a replay.
func alsoAnswers(api huma.API, method, path string, status int, doc string) {
	item := api.OpenAPI().Paths[path]
	var op *huma.Operation
	switch method {
	case http.MethodPost:
		op = item.Post
	case http.MethodDelete:
		op = item.Delete
	default:
		panic("alsoAnswers: unsupported method " + method)
	}
	def := op.Responses[strconv.Itoa(op.DefaultStatus)]
	op.Responses[strconv.Itoa(status)] = &huma.Response{Description: doc, Headers: def.Headers, Content: def.Content}
}
