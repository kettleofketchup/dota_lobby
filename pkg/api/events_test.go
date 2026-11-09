package api

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/kettleofketchup/dota_lobby/pkg/lobby"
)

// jsonNames lists a struct's JSON field names, sorted.
func jsonNames(t reflect.Type) []string {
	var out []string
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name != "" && name != "-" {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// The events endpoint re-maps lobby.Envelope, which webhooks send as is,
// into LobbyEvent; both must encode the same.
func TestLobbyEventMatchesEnvelope(t *testing.T) {
	for _, pair := range [][2]reflect.Type{
		{reflect.TypeFor[lobby.Envelope](), reflect.TypeFor[LobbyEvent]()},
		{reflect.TypeFor[lobby.Summary](), reflect.TypeFor[LobbySummary]()},
	} {
		if a, b := jsonNames(pair[0]), jsonNames(pair[1]); !slices.Equal(a, b) {
			t.Errorf("%s fields %v, %s fields %v", pair[0], a, pair[1], b)
		}
	}
	bot, gcID, league := "bot1", "27110133251", uint32(17929)
	tests := []struct {
		name string
		env  lobby.Envelope
	}{
		{"full", lobby.Envelope{
			ID: "evt_1", Type: "lobby.state_changed", Seq: 7, OccurredAt: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC),
			Lobby: lobby.Summary{ID: "lob_1", ExternalRef: "game-42", State: lobby.StateWaiting, Bot: &bot,
				GCLobbyID: &gcID, LeagueID: &league, Metadata: map[string]string{"k": "v"}},
			Data: json.RawMessage(`{"from":"creating","to":"waiting","reason":"created"}`),
		}},
		{"nulls", lobby.Envelope{
			ID: "evt_2", Type: "lobby.ready", Seq: 1, OccurredAt: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC),
			Lobby: lobby.Summary{ID: "lob_1", State: lobby.StateReady, Metadata: map[string]string{}},
			Data:  json.RawMessage(`{}`),
		}},
	}
	oapi := New(Options{}).OpenAPI()
	schema := oapi.Components.Schemas.Schema(reflect.TypeFor[LobbyEvent](), false, "")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hook, err := json.Marshal(tt.env)
			if err != nil {
				t.Fatal(err)
			}
			api, err := json.Marshal(newLobbyEvent(tt.env))
			if err != nil {
				t.Fatal(err)
			}
			var a, b any
			if err := json.Unmarshal(hook, &a); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(api, &b); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(a, b) {
				t.Errorf("webhook body %s\nevents entry %s", hook, api)
			}
			res := &huma.ValidateResult{}
			huma.Validate(oapi.Components.Schemas, schema, huma.NewPathBuffer(nil, 0), huma.ModeReadFromServer, a, res)
			if len(res.Errors) != 0 {
				t.Errorf("the spec rejects %s: %v", hook, res.Errors)
			}
		})
	}
}

var eventTypeRE = regexp.MustCompile(`^(lobby|match)\.[a-z_]+$`)

// eventSite is one place package lobby makes an event.
type eventSite struct {
	pos   string
	types []string
	keys  []string // nil when they could not be read
}

// lobbyEventSites finds where package lobby makes events: newEvent and
// Event literals, emit calls, and "typ, data := ..." pairs.
func lobbyEventSites(t *testing.T) []eventSite {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "lobby", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var terminal []string
	enum, _ := reflect.TypeFor[listLobbiesInput]().FieldByName("State")
	for st := range strings.SplitSeq(enum.Tag.Get("enum"), ",") {
		if lobby.State(st).Terminal() {
			terminal = append(terminal, "lobby."+st)
		}
	}
	types := func(e ast.Expr) []string {
		switch e := e.(type) {
		case *ast.BasicLit:
			if s, err := strconv.Unquote(e.Value); err == nil && eventTypeRE.MatchString(s) {
				return []string{s}
			}
		case *ast.BinaryExpr: // "lobby." + string(l.State), for a terminal state
			if lit, ok := e.X.(*ast.BasicLit); ok && lit.Value == `"lobby."` {
				return terminal
			}
		}
		return nil
	}
	var keys func(e ast.Expr) []string
	keys = func(e ast.Expr) []string {
		switch e := e.(type) {
		case *ast.CompositeLit:
			out := []string{}
			for _, el := range e.Elts {
				if kv, ok := el.(*ast.KeyValueExpr); ok {
					if lit, ok := kv.Key.(*ast.BasicLit); ok {
						s, _ := strconv.Unquote(lit.Value)
						out = append(out, s)
					}
				}
			}
			slices.Sort(out)
			return out
		case *ast.Ident:
			if e.Name == "nil" {
				return []string{}
			}
			if e.Obj == nil {
				return nil
			}
			if as, ok := e.Obj.Decl.(*ast.AssignStmt); ok && len(as.Lhs) == len(as.Rhs) {
				for i, l := range as.Lhs {
					if id, ok := l.(*ast.Ident); ok && id.Name == e.Name {
						return keys(as.Rhs[i])
					}
				}
			}
		}
		return nil
	}
	var sites []eventSite
	fset := token.NewFileSet()
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			var typ, data ast.Expr
			switch n := n.(type) {
			case *ast.CompositeLit:
				for _, el := range n.Elts {
					if kv, ok := el.(*ast.KeyValueExpr); ok {
						if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "Type" {
							typ = kv.Value
						} else if ok && id.Name == "Data" {
							data = kv.Value
						}
					}
				}
			case *ast.CallExpr:
				if sel, ok := n.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "emit" && len(n.Args) == 2 {
					typ, data = n.Args[0], n.Args[1]
				}
			case *ast.AssignStmt:
				if len(n.Lhs) == 2 && len(n.Rhs) == 2 {
					typ, data = n.Rhs[0], n.Rhs[1]
				}
			}
			if ts := types(typ); ts != nil && data != nil {
				sites = append(sites, eventSite{pos: fset.Position(n.Pos()).String(), types: ts, keys: keys(data)})
			}
			return true
		})
	}
	return sites
}

// The spec's event schemas cover every event package lobby makes, with
// exactly the data keys it sends.
func TestEventKindsMatchLobby(t *testing.T) {
	want := map[string][]string{}
	for _, k := range eventKinds {
		for _, typ := range k.types {
			if _, dup := want[typ]; dup {
				t.Errorf("%s is in two event kinds", typ)
			}
			want[typ] = jsonNames(k.data)
		}
	}
	sites := lobbyEventSites(t)
	if len(sites) < len(want) {
		t.Fatalf("found %d event sites in package lobby, fewer than the %d event types", len(sites), len(want))
	}
	seen := map[string]bool{}
	for _, s := range sites {
		for _, typ := range s.types {
			seen[typ] = true
			names, ok := want[typ]
			switch {
			case !ok:
				t.Errorf("%s: event %s has no schema in eventKinds", s.pos, typ)
			case s.keys == nil:
				t.Errorf("%s: cannot read the data keys of %s; use a map literal", s.pos, typ)
			case !slices.Equal(s.keys, names):
				t.Errorf("%s: %s data keys %v, the schema has %v", s.pos, typ, s.keys, names)
			}
		}
	}
	for _, typ := range slices.Sorted(maps.Keys(want)) {
		if !seen[typ] {
			t.Errorf("eventKinds documents %s, which package lobby never sends", typ)
		}
	}
}
