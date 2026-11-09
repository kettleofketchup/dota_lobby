// Package api is the service's HTTP API: huma operations on the standard
// mux, documented as OpenAPI 3.1. Health endpoints are open; everything
// else needs the API key when one is configured.
package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/kettleofketchup/dota_lobby/pkg/bot"
	"github.com/kettleofketchup/dota_lobby/pkg/config"
	"github.com/kettleofketchup/dota_lobby/pkg/gc"
	"github.com/kettleofketchup/dota_lobby/pkg/league"
)

// Defaults for Options.
const (
	DefaultMaxBodyBytes   = 64 << 10
	DefaultWatchdogMaxAge = 30 * time.Second
)

// Bots is what the API reads from the bot layer. *bot.Manager implements
// it.
type Bots interface {
	// Statuses returns every bot's status in config order.
	Statuses() []bot.Status
	// Stalled names the bots whose run loop has not moved for maxAge.
	Stalled(maxAge time.Duration) []string
	// Retry asks a bot in needs_attention, backoff or cooling_down to try
	// again now. It returns bot.ErrUnknownBot for no such bot and any other
	// error when the bot did not take the request before ctx ended.
	Retry(ctx context.Context, name string) error
}

// Leagues is what the API reads from the league registry.
// *league.Registry implements it.
type Leagues interface {
	// All lists every held league with its holders.
	All() []league.League
	// BotLeagues lists the leagues one bot holds, sorted.
	BotLeagues(bot string) []uint32
	// Nodes fetches a league's lobby nodes through a ready holder. It fails
	// with league.ErrUnknownLeague or league.ErrNoReadyHolder.
	Nodes(ctx context.Context, leagueID uint32) (string, []gc.LeagueNode, error)
}

// Leases reports which lobby each bot hosts. *pool.Pool implements it.
type Leases interface {
	// Leases maps a leased bot's name to its lobby ID.
	Leases() map[string]string
}

// Options configures a Server.
type Options struct {
	// Addr is the listen address for ListenAndServe, e.g. "0.0.0.0:8080".
	Addr string
	// APIKey guards /v1/*; empty leaves them open.
	APIKey config.Secret
	Bots   Bots
	// Leagues is the league registry; nil lists no leagues.
	Leagues Leagues
	// Leases is the bot pool; nil reports no lobbies.
	Leases Leases
	// Lobbies is the lobby manager; nil answers 503 on /v1/lobbies.
	Lobbies Lobbies
	// GCRequestTimeout bounds a synchronous GC call made for a request,
	// queue wait included (gc.request_timeout). 0 means
	// bot.DefaultRequestTimeout; it is capped at MaxSyncGCWait.
	GCRequestTimeout time.Duration
	// MinReadyBots is how many bots must be gc_ready for /readyz.
	MinReadyBots int
	// MaxBodyBytes caps request bodies; 0 means DefaultMaxBodyBytes.
	MaxBodyBytes int64
	// WatchdogMaxAge is how long a bot's loop may stall before /livez
	// fails; 0 means DefaultWatchdogMaxAge.
	WatchdogMaxAge time.Duration
	Logger         *slog.Logger
	// Version is reported by /health.
	Version string
	// Now defaults to time.Now.
	Now func() time.Time
}

// MaxSyncGCWait caps GCRequestTimeout so a synchronous GC call answers
// 504 before the server's 15 s write timeout drops the connection.
const MaxSyncGCWait = 12 * time.Second

// Server serves the API. Create it with New.
type Server struct {
	opts     Options
	log      *slog.Logger
	http     *http.Server
	handler  http.Handler
	api      huma.API
	started  time.Time
	draining atomic.Bool
}

// New builds a server; nothing listens until Serve or ListenAndServe.
func New(opts Options) *Server {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if opts.WatchdogMaxAge <= 0 {
		opts.WatchdogMaxAge = DefaultWatchdogMaxAge
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.GCRequestTimeout <= 0 {
		opts.GCRequestTimeout = bot.DefaultRequestTimeout
	}
	opts.GCRequestTimeout = min(opts.GCRequestTimeout, MaxSyncGCWait)
	if opts.Bots == nil {
		opts.Bots = noBots{}
	}
	if opts.Leagues == nil {
		opts.Leagues = noLeagues{}
	}
	if opts.Leases == nil {
		opts.Leases = noLeases{}
	}
	if opts.Lobbies == nil {
		opts.Lobbies = noLobbies{}
	}
	s := &Server{opts: opts, log: opts.Logger, started: opts.Now()}
	s.handler = s.routes()
	s.http = &http.Server{
		Addr:              opts.Addr,
		Handler:           s.handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(opts.Logger.Handler(), slog.LevelWarn),
	}
	return s
}

// Handler returns the routed handler, for tests and embedding.
func (s *Server) Handler() http.Handler { return s.handler }

// ListenAndServe listens on Options.Addr and serves until Shutdown, which
// makes it return nil.
func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.opts.Addr)
	if err != nil {
		return err
	}
	return s.Serve(ln)
}

// Serve serves on ln until Shutdown, which makes it return nil.
func (s *Server) Serve(ln net.Listener) error {
	if !s.opts.APIKey.IsSet() {
		s.log.Warn("API key authentication disabled; /v1 endpoints are public")
	}
	s.log.Info("API listening", "addr", ln.Addr().String())
	if err := s.http.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// SetDraining makes /readyz fail so load balancers stop sending work, the
// first step of shutdown.
func (s *Server) SetDraining() { s.draining.Store(true) }

// Shutdown drains, then stops the listener and waits for in-flight
// requests until ctx ends.
func (s *Server) Shutdown(ctx context.Context) error {
	s.SetDraining()
	return s.http.Shutdown(ctx)
}

func (s *Server) routes() http.Handler {
	r := &router{mux: http.NewServeMux(), auth: s.authed}

	r.handle("GET /livez", http.HandlerFunc(s.handleLivez))
	r.handle("GET /readyz", http.HandlerFunc(s.handleReadyz))
	r.handle("GET /health", http.HandlerFunc(s.handleHealth))

	s.api = humago.New(r.mux, apiConfig())
	// Middleware must be in place before the first Register: huma captures
	// the chain per operation.
	s.api.UseMiddleware(s.withLogger, s.requireKey)
	s.registerBots(r)
	s.registerLeagues(r)
	s.registerLobbies(r)
	documentWebhook(s.api.OpenAPI())
	for _, p := range []string{"/openapi.json", "/openapi.yaml", "/openapi-3.0.json", "/openapi-3.0.yaml"} {
		r.learn(http.MethodGet, p, false)
	}

	// The pre-v1 placeholders; nothing depends on them.
	r.handle("/bots", s.authed(gone("/v1/bots")))
	r.handle("/lobby/create", s.authed(gone("/v1/lobbies")))
	r.handle("/lobby/info", s.authed(gone("/v1/lobbies")))

	r.mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeProblem(w, http.StatusNotFound, "not_found", "no such endpoint")
	})
	r.addMethodFallbacks()
	return s.wrap(r.mux)
}

// router remembers the method patterns registered on the mux, the plain
// ones and huma's, so a wrong method gets a 405 problem instead of falling
// through to the 404 catch-all.
type router struct {
	mux       *http.ServeMux
	auth      func(http.HandlerFunc) http.Handler
	allowed   map[string][]string // path -> methods
	protected map[string]bool     // paths whose 405 needs the API key too
	order     []string
}

// register adds a huma operation and learns its route. An operation with
// Security is protected, and so is its path's 405 fallback, so a caller
// without the key cannot learn which methods a protected path takes.
func register[I, O any](s *Server, r *router, op huma.Operation, h func(context.Context, *I) (*O, error)) {
	if op.MaxBodyBytes == 0 {
		op.MaxBodyBytes = s.opts.MaxBodyBytes
	}
	huma.Register(s.api, op, h)
	r.learn(op.Method, op.Path, len(op.Security) > 0)
}

func (r *router) handle(pattern string, h http.Handler) {
	r.mux.Handle(pattern, h)
	if method, path, ok := strings.Cut(pattern, " "); ok {
		r.learn(method, path, false)
	}
}

func (r *router) learn(method, path string, protected bool) {
	if r.allowed == nil {
		r.allowed = map[string][]string{}
		r.protected = map[string]bool{}
	}
	if _, seen := r.allowed[path]; !seen {
		r.order = append(r.order, path)
	}
	r.allowed[path] = append(r.allowed[path], method)
	if protected {
		r.protected[path] = true
	}
}

func (r *router) addMethodFallbacks() {
	for _, path := range r.order {
		methods := r.allowed[path]
		if slices.Contains(methods, http.MethodGet) {
			methods = append(methods, http.MethodHead)
		}
		allow := strings.Join(methods, ", ")
		var h http.HandlerFunc = func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Allow", allow)
			writeProblem(w, http.StatusMethodNotAllowed, "method_not_allowed", "allowed: "+allow)
		}
		if r.protected[path] {
			r.mux.Handle(path, r.auth(h))
			continue
		}
		r.mux.Handle(path, h)
	}
}

// wrap applies what every response shares: the body limit and no caching.
func (s *Server) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, s.opts.MaxBodyBytes)
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// withLogger makes the server's logger available to the error hook.
func (s *Server) withLogger(ctx huma.Context, next func(huma.Context)) {
	next(huma.WithValue(ctx, loggerKey{}, s.log))
}

// requireKey enforces the API key on operations that declare Security,
// when a key is configured.
func (s *Server) requireKey(ctx huma.Context, next func(huma.Context)) {
	if !s.opts.APIKey.IsSet() || len(ctx.Operation().Security) == 0 {
		next(ctx)
		return
	}
	if !s.validKey(presentedKey(ctx.Header)) {
		ctx.SetHeader("WWW-Authenticate", authChallenge)
		_ = huma.WriteErr(s.api, ctx, http.StatusUnauthorized, "missing or invalid API key")
		return
	}
	next(ctx)
}

const authChallenge = `Bearer realm="dota_lobby"`

// authed is requireKey for plain handlers: the removed endpoints and the
// 405 fallbacks of protected paths.
func (s *Server) authed(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.opts.APIKey.IsSet() && !s.validKey(presentedKey(r.Header.Get)) {
			w.Header().Set("WWW-Authenticate", authChallenge)
			writeProblem(w, http.StatusUnauthorized, "unauthorized", "missing or invalid API key")
			return
		}
		next(w, r)
	})
}

// presentedKey is the key from X-API-Key or "Authorization: Bearer" (the
// scheme matched case-insensitively).
func presentedKey(header func(string) string) string {
	if k := header("X-API-Key"); k != "" {
		return k
	}
	scheme, token, ok := strings.Cut(header("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

// validKey compares digests in constant time, so neither the key's
// contents nor its length leak through timing.
func (s *Server) validKey(got string) bool {
	if got == "" {
		return false
	}
	a := sha256.Sum256([]byte(got))
	b := sha256.Sum256([]byte(s.opts.APIKey.Reveal()))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	// The status line is out; a failed write only means the client left.
	_ = json.NewEncoder(w).Encode(v)
}

func gone(moved string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeProblem(w, http.StatusGone, "gone", "this endpoint was removed; use "+moved)
	}
}

// noBots stands in when no bot source is wired, e.g. in tests.
type noBots struct{}

func (noBots) Statuses() []bot.Status         { return nil }
func (noBots) Stalled(time.Duration) []string { return nil }
func (noBots) Retry(context.Context, string) error {
	return bot.ErrUnknownBot
}

// noLeagues stands in when no registry is wired.
type noLeagues struct{}

func (noLeagues) All() []league.League       { return nil }
func (noLeagues) BotLeagues(string) []uint32 { return nil }
func (noLeagues) Nodes(_ context.Context, id uint32) (string, []gc.LeagueNode, error) {
	return "", nil, fmt.Errorf("league %d: %w", id, league.ErrUnknownLeague)
}

// noLeases stands in when no pool is wired.
type noLeases struct{}

func (noLeases) Leases() map[string]string { return nil }
