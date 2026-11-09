// Package bottest provides a scriptable bot.Source for tests of the layers
// above pkg/bot. Each bot runs GC calls directly against a gc.Client,
// usually a *fake.Client.
package bottest

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/bot"
	"github.com/kettleofketchup/dota_lobby/pkg/gc"
)

// Bot is a bot.Handle whose status the test sets.
type Bot struct {
	name   string
	client gc.Client

	mu     sync.Mutex
	status bot.Status
	calls  int
}

// NewBot returns a bot that is not ready.
func NewBot(name string, client gc.Client) *Bot {
	return &Bot{name: name, client: client, status: bot.Status{Name: name, Username: name, State: bot.StateDisconnected}}
}

// Name implements bot.Handle.
func (b *Bot) Name() string { return b.name }

// Status implements bot.Handle.
func (b *Bot) Status() bot.Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.status
}

// SetReady makes the bot gc_ready with ReadySince = since, or not ready
// when since is zero.
func (b *Bot) SetReady(since time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if since.IsZero() {
		b.status.State, b.status.Ready, b.status.ReadySince = bot.StateBackoff, false, time.Time{}
		return
	}
	b.status.State, b.status.Ready, b.status.ReadySince = bot.StateGCReady, true, since
}

// SetLobbyStale sets Status.LobbyStale.
func (b *Bot) SetLobbyStale(stale bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.status.LobbyStale = stale
}

// DoCalls counts Do calls.
func (b *Bot) DoCalls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

// Do implements bot.Handle like bot.Worker.Do: it fails with
// bot.ErrNotReady unless the bot is ready, and bounds ctx by 5 s when it
// has no deadline.
func (b *Bot) Do(ctx context.Context, op string, fn func(ctx context.Context, c gc.Client) error) error {
	b.mu.Lock()
	b.calls++
	ready := b.status.Ready
	b.mu.Unlock()
	if !ready {
		return fmt.Errorf("%s: %w", op, bot.ErrNotReady)
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
	}
	return fn(ctx, b.client)
}

// CurrentLobby implements bot.Handle.
func (b *Bot) CurrentLobby() (*gc.LobbyView, bool) { return b.client.CurrentLobby() }

// SubscribeLobby implements bot.Handle.
func (b *Bot) SubscribeLobby() (<-chan gc.LobbyEvent, func()) { return b.client.SubscribeLobby() }

// SubscribeInvites implements bot.Handle.
func (b *Bot) SubscribeInvites() (<-chan gc.InvitationCreated, func()) {
	return b.client.SubscribeInvites()
}

// SetState sets the bot's state without touching Ready, e.g. to
// needs_attention after SetReady(time.Time{}).
func (b *Bot) SetState(st bot.State) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.status.State = st
}

// SetSteamID sets Status.SteamID.
func (b *Bot) SetSteamID(id uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.status.SteamID = id
}

var _ bot.Handle = (*Bot)(nil)

// Source is a bot.Source over a fixed list of bots.
type Source struct {
	bots []*Bot
}

// NewSource returns a source listing bots in order.
func NewSource(bots ...*Bot) *Source { return &Source{bots: bots} }

// Names implements bot.Source.
func (s *Source) Names() []string {
	out := make([]string, len(s.bots))
	for i, b := range s.bots {
		out[i] = b.name
	}
	return out
}

// Bot implements bot.Source.
func (s *Source) Bot(name string) (bot.Handle, bool) {
	for _, b := range s.bots {
		if strings.EqualFold(b.name, name) {
			return b, true
		}
	}
	return nil, false
}

var _ bot.Source = (*Source)(nil)
