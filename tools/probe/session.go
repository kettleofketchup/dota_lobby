package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	protobuf "github.com/aperturerobotics/protobuf-go-lite"
	dota2 "github.com/paralin/go-dota2"
	"github.com/paralin/go-dota2/cso"
	devents "github.com/paralin/go-dota2/events"
	"github.com/paralin/go-dota2/protocol"
	"github.com/paralin/go-steam"
	sprotocol "github.com/paralin/go-steam/protocol"
	"github.com/paralin/go-steam/protocol/gamecoordinator"
	"github.com/paralin/go-steam/protocol/steamlang"
	"github.com/sirupsen/logrus"
)

// probeJobBase keeps our hand-made GC job IDs far away from the ones
// go-dota2's MakeRequest allocates, so the two can never collide.
const probeJobBase = 1 << 40

// session is one logged-in bot with a GC tap that records every packet.
type session struct {
	cfg *config
	rep *reporter
	le  *logrus.Logger
	sc  *steam.Client
	d   *dota2.Dota2

	welcomed chan struct{}
	welcome  sync.Once
	failed   chan error
	popups   chan *protocol.CMsgDOTAPopup
	// details is the last full set of lobby details sent, for merged updates.
	details *protocol.CMsgPracticeLobbySetDetails
	// chatChannel is the joined lobby chat channel ID, 0 until joined.
	chatChannel atomic.Uint64
	captureNo   atomic.Uint64
	nextJob     atomic.Uint64

	mu      sync.Mutex
	waiters []*packetWaiter
}

// packetWaiter receives every incoming GC packet of one message type.
type packetWaiter struct {
	msgType uint32
	ch      chan *gamecoordinator.GCPacket
}

// newSession builds a fresh steam+dota2 pair; they are never reused across
// connections.
func newSession(cfg *config, rep *reporter) *session {
	le := logrus.New()
	if cfg.verbose {
		le.SetLevel(logrus.DebugLevel)
	}
	s := &session{
		cfg:      cfg,
		rep:      rep,
		le:       le,
		sc:       steam.NewClient(),
		welcomed: make(chan struct{}),
		failed:   make(chan error, 1),
		popups:   make(chan *protocol.CMsgDOTAPopup, 16),
	}
	s.nextJob.Store(probeJobBase)
	// The tap must be registered before dota2.New and before Connect.
	s.sc.GC.RegisterPacketHandler(s)
	s.d = dota2.New(s.sc, le)
	return s
}

// HandleGCPacket records each incoming GC packet and fans it out to waiters.
func (s *session) HandleGCPacket(p *gamecoordinator.GCPacket) {
	if p.AppId != dota2.AppID {
		return
	}
	file := s.capture("in", p.MsgType, p.Body)
	s.rep.emit("", "gc_in", map[string]any{
		"msg":        p.MsgType,
		"name":       msgName(p.MsgType),
		"target_job": uint64(p.TargetJobId),
		"len":        len(p.Body),
		"capture":    file,
	})
	s.mu.Lock()
	for _, w := range s.waiters {
		if w.msgType == p.MsgType {
			select {
			case w.ch <- p:
			default:
			}
		}
	}
	s.mu.Unlock()
}

// watch returns a channel of incoming packets of msgType until cancel is called.
func (s *session) watch(msgType protocol.EDOTAGCMsg) (<-chan *gamecoordinator.GCPacket, func()) {
	w := &packetWaiter{msgType: uint32(msgType), ch: make(chan *gamecoordinator.GCPacket, 16)}
	s.mu.Lock()
	s.waiters = append(s.waiters, w)
	s.mu.Unlock()
	return w.ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for i, x := range s.waiters {
			if x == w {
				s.waiters = append(s.waiters[:i], s.waiters[i+1:]...)
				return
			}
		}
	}
}

// send writes a GC message ourselves. With withJob it sets a source job ID so
// we can see whether the GC echoes it (Q7, Q18). Returns the job ID (0 if none).
func (s *session) send(msgType protocol.EDOTAGCMsg, body protobuf.Message, withJob bool) uint64 {
	msg := gamecoordinator.NewGCMsgProtobuf(dota2.AppID, uint32(msgType), body)
	var job uint64
	if withJob {
		job = s.nextJob.Add(1)
		msg.SetSourceJobId(sprotocol.JobId(job))
	}
	raw, _ := body.MarshalVT()
	file := s.capture("out", uint32(msgType), raw)
	s.rep.emit("", "gc_out", map[string]any{
		"msg": uint32(msgType), "name": msgName(uint32(msgType)), "source_job": job, "capture": file,
	})
	s.sc.GC.Write(msg)
	return job
}

// capture stores a raw body under <state>/captures for golden-file tests.
func (s *session) capture(dir string, msgType uint32, body []byte) string {
	n := s.captureNo.Add(1)
	name := fmt.Sprintf("%04d-%s-%d-%s.bin", n, dir, msgType, msgName(msgType))
	path := filepath.Join(s.cfg.stateDir, "captures", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		s.le.WithError(err).Warn("create capture dir")
		return ""
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		s.le.WithError(err).Warn("write capture")
		return ""
	}
	return name
}

func msgName(t uint32) string { return protocol.EDOTAGCMsg(t).String() }

// connect logs in with the stored refresh token and waits for the GC welcome.
func (s *session) connect(ctx context.Context) error {
	token, err := loadToken(s.cfg)
	if err != nil {
		return err
	}
	s.rep.emit("Q1", "token_claims", claimsSummary(token))

	go s.eventLoop(ctx, token)
	start := time.Now()
	if _, err := s.sc.ConnectContext(ctx); err != nil {
		return fmt.Errorf("connect to Steam CM: %w", err)
	}

	timeout := time.NewTimer(s.cfg.welcomeTimeout)
	defer timeout.Stop()
	select {
	case <-s.welcomed:
		s.rep.emit("Q1", "gc_welcomed", map[string]any{
			"elapsed_ms": time.Since(start).Milliseconds(),
			"steam_id":   s.sc.SteamId().ToUint64(),
			"account_id": s.sc.SteamId().GetAccountId(),
		})
		return nil
	case err := <-s.failed:
		return err
	case <-timeout.C:
		return fmt.Errorf("no GC welcome within %s", s.cfg.welcomeTimeout)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *session) fail(err error) {
	select {
	case s.failed <- err:
	default:
	}
}

// eventLoop drains Steam events. It must never block on a GC request, since
// go-steam's Emit blocks once the 30-slot channel is full.
func (s *session) eventLoop(ctx context.Context, token string) {
	var helloStop chan struct{}
	for {
		var ev any
		select {
		case <-ctx.Done():
			return
		case ev = <-s.sc.Events():
		}
		switch e := ev.(type) {
		case *steam.ConnectedEvent:
			s.rep.emit("Q1", "steam_connected", nil)
			go func() {
				err := s.sc.Auth.LogOn(ctx, &steam.LogOnDetails{
					Username:               s.cfg.user,
					AccessToken:            token,
					ShouldRememberPassword: true,
				})
				if err != nil {
					s.fail(fmt.Errorf("logon: %w", err))
				}
			}()
		case *steam.LoggedOnEvent:
			s.rep.emit("Q1", "steam_logged_on", map[string]any{"result": e.Result.String(), "extended": e.ExtendedResult.String()})
			s.d.SetPlaying(true)
			helloStop = make(chan struct{})
			go s.helloLoop(helloStop)
		case *steam.LogOnFailedEvent:
			fields := map[string]any{"result": e.Result.String(), "result_code": int32(e.Result)}
			if e.Err != nil {
				fields["error"] = e.Err.Error()
			}
			s.rep.emit("Q1", "steam_logon_failed", fields)
			s.fail(fmt.Errorf("logon failed: %v", e.Result))
		case *steam.LoggedOffEvent:
			s.rep.emit("Q1", "steam_logged_off", map[string]any{"result": e.Result.String()})
			if e.Result == steamlang.EResult_LoggedInElsewhere {
				s.fail(errors.New("logged in elsewhere"))
			}
		case *steam.PlayingSessionStateEvent:
			s.rep.emit("Q1", "playing_session_state", map[string]any{"blocked": e.PlayingBlocked, "app": e.PlayingApp})
		case *steam.DisconnectedEvent:
			s.rep.emit("", "steam_disconnected", nil)
			s.fail(errors.New("disconnected from Steam"))
		case steam.FatalErrorEvent:
			s.rep.emit("", "steam_fatal", map[string]any{"error": error(e).Error()})
			s.fail(fmt.Errorf("steam fatal: %w", error(e)))
		case *devents.ClientWelcomed:
			s.welcome.Do(func() {
				if helloStop != nil {
					close(helloStop)
				}
				close(s.welcomed)
			})
		case *devents.GCConnectionStatusChanged:
			s.rep.emit("", "gc_status", map[string]any{"old": e.OldState.String(), "new": e.NewState.String()})
		case *devents.JoinedChatChannel:
			s.rep.emit("chat", "chat_joined_event", chatJoinFields(&e.CMsgDOTAJoinChatChannelResponse))
		case *devents.ChatMessage:
			go s.onChat(&e.CMsgDOTAChatMessage)
		case *devents.Popup:
			s.rep.emit("Q4", "popup", map[string]any{
				"id": e.GetId().String(), "id_code": int32(e.GetId()), "custom_text": e.GetCustomText(), "int_data": e.GetIntData(),
			})
			select {
			case s.popups <- &e.CMsgDOTAPopup:
			default:
			}
		}
	}
}

// helloLoop repeats the GC hello until welcomed; the first one is often lost.
func (s *session) helloLoop(stop <-chan struct{}) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	s.d.SayHello()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			s.d.SayHello()
		}
	}
}

// close leaves the GC politely. It does not call LogOff, which would revoke
// nothing but is not needed (04 §2.2) and makes restart tests less realistic.
func (s *session) close() {
	s.d.SetPlaying(false)
	s.d.Close()
	s.sc.Disconnect()
}

// currentLobby returns the lobby in the SO cache, or nil.
func (s *session) currentLobby() *protocol.CSODOTALobby {
	c, err := s.d.GetCache().GetContainerForTypeID(uint32(cso.Lobby))
	if err != nil {
		return nil
	}
	if o := c.GetOne(); o != nil {
		return o.(*protocol.CSODOTALobby)
	}
	return nil
}

// isLeagueAdmin reads the GameAccountClient flag from the SO cache (02 §7.2).
func (s *session) isLeagueAdmin() (bool, bool) {
	c, err := s.d.GetCache().GetContainerForTypeID(uint32(cso.GameAccountClient))
	if err != nil {
		return false, false
	}
	o := c.GetOne()
	if o == nil {
		return false, false
	}
	return o.(*protocol.CSODOTAGameAccountClient).GetIsLeagueAdmin(), true
}
