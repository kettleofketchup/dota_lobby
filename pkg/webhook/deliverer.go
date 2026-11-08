package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/config"
	"github.com/kettleofketchup/dota_lobby/pkg/events"
	"github.com/kettleofketchup/dota_lobby/pkg/lobby"
	"github.com/kettleofketchup/dota_lobby/pkg/store"
)

// Defaults for Options.
const (
	DefaultPoll        = time.Second
	DefaultConcurrency = 4
	DefaultTimeout     = 10 * time.Second
	DefaultMaxAge      = 24 * time.Hour
)

// MaxBodyBytes caps a delivery body; a larger event is given up.
const MaxBodyBytes = 64 << 10

// storeTimeout bounds one queue update. Updates run on their own ctx so a
// delivery that succeeded during shutdown is still recorded.
const storeTimeout = 5 * time.Second

// backoff is the wait after the first, second, ... failed attempt; after
// the last step every retry waits retryEvery. Each wait gets ±20 % jitter.
var backoff = []time.Duration{10 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute}

const retryEvery = time.Hour

// Queue is the delivery queue; *store.Deliveries implements it.
type Queue interface {
	Due(ctx context.Context, now time.Time, limit int) ([]store.Delivery, error)
	MarkDelivered(ctx context.Context, eventID, url string, attempts, status int, at time.Time) error
	MarkRetry(ctx context.Context, eventID, url string, attempts, status int, errMsg string, next time.Time) error
	MarkDead(ctx context.Context, eventID, url string, attempts, status int, errMsg string) error
}

// Options configures a Deliverer. Queue is required.
type Options struct {
	Queue    Queue
	Webhooks []config.WebhookConfig
	Secrets  config.Secrets
	Delivery config.WebhookDeliveryConfig
	// Client sends the requests; redirects are never followed, whatever
	// its CheckRedirect says. Nil uses a client on the default transport.
	Client *http.Client
	// Wake, when set, starts a queue check on every envelope; the events
	// bus provides one. Without it the queue is checked every Poll.
	Wake <-chan lobby.Envelope
	// Poll is how often the queue is checked; 0 means DefaultPoll.
	Poll time.Duration
	// Concurrency is how many deliveries may be in flight at once, each
	// for a different (URL, lobby); 0 means DefaultConcurrency.
	Concurrency int
	// Version goes in the User-Agent.
	Version string
	Logger  *slog.Logger
	// Now defaults to time.Now.
	Now func() time.Time
	// Jitter returns a number in [0, 1); it defaults to math/rand.
	Jitter func() float64
}

// Deliverer sends queued deliveries until its context ends.
type Deliverer struct {
	opts   Options
	log    *slog.Logger
	client *http.Client
	hooks  map[string]hook
	wake   chan struct{}

	mu       sync.Mutex
	inflight map[flightKey]bool
}

type hook struct {
	secret      []byte
	tokenHeader string
	token       config.Secret
	// name is the URL without query or user info, for logs.
	name string
}

type flightKey struct{ url, lobby string }

// New builds a deliverer; nothing is sent until Run.
func New(opts Options) *Deliverer {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Jitter == nil {
		opts.Jitter = rand.Float64
	}
	if opts.Poll <= 0 {
		opts.Poll = DefaultPoll
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = DefaultConcurrency
	}
	if opts.Delivery.Timeout <= 0 {
		opts.Delivery.Timeout = DefaultTimeout
	}
	if opts.Delivery.MaxAge <= 0 {
		opts.Delivery.MaxAge = DefaultMaxAge
	}
	client := &http.Client{}
	if opts.Client != nil {
		c := *opts.Client
		client = &c
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	hooks := map[string]hook{}
	for _, w := range opts.Webhooks {
		h := hook{
			secret:      []byte(opts.Secrets.WebhookSecret(w.URL).Reveal()),
			tokenHeader: w.TokenHeader,
			token:       opts.Secrets.WebhookToken(w.URL),
			name:        logName(w.URL),
		}
		hooks[strings.ToLower(w.URL)] = h
	}
	return &Deliverer{
		opts: opts, log: opts.Logger, client: client, hooks: hooks,
		wake: make(chan struct{}, 1), inflight: map[flightKey]bool{},
	}
}

// Route returns, for an event type, the URLs of the webhooks whose events
// patterns match it. It is what store.Lobbies.WithWebhooks takes; with no
// webhooks it returns nil, which queues nothing.
func Route(hooks []config.WebhookConfig) func(eventType string) []string {
	if len(hooks) == 0 {
		return nil
	}
	hooks = append([]config.WebhookConfig(nil), hooks...)
	return func(eventType string) []string {
		var out []string
		for _, h := range hooks {
			if events.Match(h.Events, eventType) {
				out = append(out, h.URL)
			}
		}
		return out
	}
}

// Run delivers until ctx ends. Attempts in flight are cancelled and their
// rows stay pending, uncounted, for the next run.
func (d *Deliverer) Run(ctx context.Context) {
	var wg sync.WaitGroup
	defer wg.Wait()
	tick := time.NewTicker(d.opts.Poll)
	defer tick.Stop()
	wake := d.opts.Wake
	for {
		d.dispatch(ctx, &wg)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-d.wake:
		case _, ok := <-wake:
			if !ok {
				wake = nil
			}
		}
	}
}

func (d *Deliverer) kick() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// dispatch starts every due delivery whose (URL, lobby) is not already in
// flight, up to Concurrency. mu is held across the query, so a delivery
// that finishes meanwhile cannot be started a second time from a stale
// read.
func (d *Deliverer) dispatch(ctx context.Context, wg *sync.WaitGroup) {
	d.mu.Lock()
	defer d.mu.Unlock()
	free := d.opts.Concurrency - len(d.inflight)
	if free <= 0 || ctx.Err() != nil {
		return
	}
	due, err := d.opts.Queue.Due(ctx, d.opts.Now(), d.opts.Concurrency*4)
	if err != nil {
		if ctx.Err() == nil {
			d.log.Error("cannot read the webhook queue", "err", err)
		}
		return
	}
	for _, dl := range due {
		k := flightKey{strings.ToLower(dl.URL), dl.Event.LobbyID}
		if d.inflight[k] {
			continue
		}
		if free == 0 {
			break
		}
		free--
		d.inflight[k] = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.attempt(ctx, dl)
			d.mu.Lock()
			delete(d.inflight, k)
			d.mu.Unlock()
			d.kick() // the lobby's next event may be due now
		}()
	}
}

// result is what one attempt came to.
type result struct {
	status     int
	err        string
	retryable  bool
	retryAfter time.Duration
}

func (d *Deliverer) attempt(ctx context.Context, dl store.Delivery) {
	ev := dl.Event
	attempts := dl.Attempts + 1
	log := d.log.With("event", ev.ID, "type", ev.Type, "lobby", ev.LobbyID, "seq", ev.Seq, "attempt", attempts)
	h, ok := d.hooks[strings.ToLower(dl.URL)]
	if !ok {
		log = log.With("webhook", logName(dl.URL))
		d.dead(log, dl, attempts, result{err: "the webhook is no longer configured"})
		return
	}
	log = log.With("webhook", h.name)
	env, err := lobby.EnvelopeFromRow(ev, nil)
	if err != nil {
		d.dead(log, dl, attempts, result{err: err.Error()})
		return
	}
	body, err := json.Marshal(env)
	if err != nil {
		d.dead(log, dl, attempts, result{err: fmt.Sprintf("encode envelope: %v", err)})
		return
	}
	if len(body) > MaxBodyBytes {
		d.dead(log, dl, attempts, result{err: fmt.Sprintf("body of %d bytes is over the %d byte limit", len(body), MaxBodyBytes)})
		return
	}
	res := d.send(ctx, dl.URL, h, env, body)
	delivered := res.status >= 200 && res.status < 300
	if !delivered && ctx.Err() != nil {
		return // shutting down: the row stays pending and this try is not counted
	}
	switch {
	case delivered:
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), storeTimeout)
		defer cancel()
		if err := d.opts.Queue.MarkDelivered(sctx, ev.ID, dl.URL, attempts, res.status, d.opts.Now()); err != nil {
			log.Error("webhook delivered but not recorded; it will be sent again", "err", err)
			return
		}
		log.Debug("webhook delivered", "status", res.status)
	case res.retryable:
		d.retry(log, dl, attempts, res)
	default:
		d.dead(log, dl, attempts, res)
	}
}

func (d *Deliverer) send(ctx context.Context, rawURL string, h hook, env lobby.Envelope, body []byte) result {
	actx, cancel := context.WithTimeout(ctx, d.opts.Delivery.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(actx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return result{err: fmt.Sprintf("build request: %v", err)}
	}
	ts := d.opts.Now().Unix()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "dota_lobby/"+d.opts.Version)
	req.Header.Set(HeaderEventID, env.ID)
	req.Header.Set(HeaderEventType, env.Type)
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(HeaderSignature, Sign(h.secret, ts, body))
	if h.tokenHeader != "" && h.token.IsSet() {
		req.Header.Set(h.tokenHeader, h.token.Reveal())
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return result{retryable: true, err: transportError(err)}
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, MaxBodyBytes))
	res := result{status: resp.StatusCode}
	switch c := resp.StatusCode; {
	case c >= 200 && c < 300:
	case c >= 300 && c < 400:
		res.err = "the receiver redirected; redirects are not followed, so configure the final URL"
	case c == http.StatusRequestTimeout, c == http.StatusTooEarly, c == http.StatusTooManyRequests, c >= 500:
		res.retryable = true
		res.err = resp.Status
		res.retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), d.opts.Now())
	default:
		res.err = resp.Status
	}
	return res
}

func (d *Deliverer) retry(log *slog.Logger, dl store.Delivery, attempts int, res result) {
	now := d.opts.Now()
	wait := res.retryAfter
	if wait <= 0 {
		wait = d.backoff(attempts)
	}
	next := now.Add(wait)
	if deadline := dl.Event.OccurredAt.Add(d.opts.Delivery.MaxAge); !next.Before(deadline) {
		res.err = fmt.Sprintf("%s; retried for webhook_delivery.max_age (%s)", res.err, d.opts.Delivery.MaxAge)
		d.dead(log, dl, attempts, res)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	if err := d.opts.Queue.MarkRetry(ctx, dl.Event.ID, dl.URL, attempts, res.status, res.err, next); err != nil {
		log.Error("cannot reschedule a webhook delivery", "err", err)
		return
	}
	log.Warn("webhook delivery failed; will retry", "status", res.status, "err", res.err, "retry_in", wait.Round(time.Second))
}

func (d *Deliverer) dead(log *slog.Logger, dl store.Delivery, attempts int, res result) {
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	if err := d.opts.Queue.MarkDead(ctx, dl.Event.ID, dl.URL, attempts, res.status, res.err); err != nil {
		log.Error("cannot give up a webhook delivery", "err", err)
		return
	}
	log.Error("webhook delivery given up; the lobby's next event goes next", "status", res.status, "err", res.err)
}

// backoff is the wait after the attempts-th failed attempt, with jitter.
func (d *Deliverer) backoff(attempts int) time.Duration {
	base := retryEvery
	if attempts >= 1 && attempts <= len(backoff) {
		base = backoff[attempts-1]
	}
	return time.Duration(float64(base) * (0.8 + 0.4*d.opts.Jitter()))
}

// parseRetryAfter reads a Retry-After of delay seconds or an HTTP date; 0
// when absent or unreadable.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}

// transportError describes a failed request without the URL, which the
// log already names without its query.
func transportError(err error) string {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		err = uerr.Err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out (webhook_delivery.timeout)"
	}
	return err.Error()
}

// logName is a URL without user info, query or fragment, which may hold
// credentials.
func logName(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(unparsable URL)"
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}).String()
}
