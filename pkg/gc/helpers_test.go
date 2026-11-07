package gc

import (
	"context"
	"sync"
	"testing"
	"time"

	protobuf "github.com/aperturerobotics/protobuf-go-lite"
	dota2 "github.com/paralin/go-dota2"
	"github.com/paralin/go-steam"
	sprotocol "github.com/paralin/go-steam/protocol"
	"github.com/paralin/go-steam/protocol/gamecoordinator"
	"github.com/paralin/go-steam/steamid"
)

func mustMarshal(t *testing.T, m protobuf.Message) []byte {
	t.Helper()
	b, err := m.MarshalVT()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// packet builds an incoming Dota GC packet the way go-steam parses one.
func packet(t *testing.T, msgType uint32, targetJob uint64, body protobuf.Message) *gamecoordinator.GCPacket {
	t.Helper()
	return &gamecoordinator.GCPacket{
		AppId:       dota2.AppID,
		MsgType:     msgType,
		IsProto:     true,
		Body:        mustMarshal(t, body),
		TargetJobId: sprotocol.JobId(targetJob),
	}
}

// fakeTransport stands in for a steam.Client: it records writes and logons,
// and the test plays the Steam reader by calling deliver and steamEvent.
type fakeTransport struct {
	steamID uint64
	dialErr error

	events chan any
	writes chan gamecoordinator.IGCMsg
	logons chan *steam.LogOnDetails

	mu       sync.Mutex
	handlers []steam.GCPacketHandler
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{
		steamID: 76561198000000001,
		events:  make(chan any, 30),
		writes:  make(chan gamecoordinator.IGCMsg, 256),
		logons:  make(chan *steam.LogOnDetails, 4),
	}
}

func (f *fakeTransport) SteamID() steamid.SteamId { return steamid.SteamId(f.steamID) }

func (f *fakeTransport) Write(m gamecoordinator.IGCMsg) { f.writes <- m }

func (f *fakeTransport) SetGamesPlayed(...uint64) {}

func (f *fakeTransport) RegisterPacketHandler(h steam.GCPacketHandler) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers = append(f.handlers, h)
}

func (f *fakeTransport) Events() <-chan any { return f.events }

// Dial behaves like go-steam: Connected after the handshake, Disconnected
// once the connection context ends.
func (f *fakeTransport) Dial(ctx context.Context) error {
	if f.dialErr != nil {
		return f.dialErr
	}
	go func() {
		<-ctx.Done()
		f.events <- &steam.DisconnectedEvent{}
	}()
	f.events <- &steam.ConnectedEvent{}
	return nil
}

func (f *fakeTransport) LogOn(_ context.Context, d *steam.LogOnDetails) error {
	f.logons <- d
	return nil
}

// deliver hands a GC packet to every handler, as go-steam's reader does.
func (f *fakeTransport) deliver(p *gamecoordinator.GCPacket) {
	f.mu.Lock()
	hs := append([]steam.GCPacketHandler(nil), f.handlers...)
	f.mu.Unlock()
	for _, h := range hs {
		h.HandleGCPacket(p)
	}
}

// waitWrite returns the next outgoing GC message of msgType, skipping others.
func (f *fakeTransport) waitWrite(t *testing.T, msgType uint32) *gamecoordinator.GCMsgProtobuf {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case m := <-f.writes:
			if m.GetMsgType() == msgType {
				return m.(*gamecoordinator.GCMsgProtobuf)
			}
		case <-timeout:
			t.Fatalf("no GC message %d written", msgType)
			return nil
		}
	}
}

// nextEvent returns the next adapter event or fails.
func nextEvent(t *testing.T, a *Adapter) Event {
	t.Helper()
	select {
	case ev := <-a.Events():
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("no event")
		return nil
	}
}

// waitEvent skips events until one of type T arrives.
func waitEvent[T Event](t *testing.T, a *Adapter) T {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case ev := <-a.Events():
			if v, ok := ev.(T); ok {
				return v
			}
		case <-timeout:
			var zero T
			t.Fatalf("no %T event", zero)
			return zero
		}
	}
}
