package gc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/config"
	devents "github.com/paralin/go-dota2/events"
	"github.com/paralin/go-dota2/protocol"
	"github.com/paralin/go-steam"
	"github.com/paralin/go-steam/protocol/gamecoordinator"
	"github.com/paralin/go-steam/protocol/protobuf/unified"
	"github.com/paralin/go-steam/protocol/steamlang"
)

var tokenCreds = Credentials{Username: "bot_a", AccessToken: "refresh.jwt.token", DeviceName: "dota_lobby"}

// newTestAdapter returns an adapter whose connections use the given fake
// transports in order.
func newTestAdapter(t *testing.T, transports ...*fakeTransport) *Adapter {
	t.Helper()
	a := New(Options{Logger: slog.New(slog.DiscardHandler), LeaveTimeout: time.Second, DestroyTimeout: 50 * time.Millisecond})
	next := 0
	a.newTransport = func(time.Duration) transport {
		if next >= len(transports) {
			t.Fatal("more connections than fake transports")
		}
		ft := transports[next]
		next++
		return ft
	}
	return a
}

// connectWelcomed connects, logs on and gets a GC welcome.
func connectWelcomed(t *testing.T, a *Adapter, ft *fakeTransport) {
	t.Helper()
	if err := a.Connect(t.Context(), tokenCreds); err != nil {
		t.Fatal(err)
	}
	waitEvent[Connected](t, a)
	<-ft.logons
	ft.events <- &steam.LoggedOnEvent{Result: steamlang.EResult_OK}
	waitEvent[LoggedOn](t, a)
	ft.deliver(packet(t, uint32(protocol.EGCBaseClientMsg_k_EMsgGCClientWelcome), noJob, &protocol.CMsgClientWelcome{}))
	waitEvent[GCWelcomed](t, a)
}

func lobbySO(t *testing.T, op string, l *protocol.CSODOTALobby) *gamecoordinator.GCPacket {
	t.Helper()
	obj := []*protocol.CMsgSOMultipleObjects_SingleObject{{TypeId: new(int32(2004)), ObjectData: mustMarshal(t, l)}}
	m := &protocol.CMsgSOMultipleObjects{OwnerSoid: &protocol.CMsgSOIDOwner{Type: new(uint32(3)), Id: new(l.GetLobbyId())}}
	switch op {
	case "add":
		m.ObjectsAdded = obj
	case "modify":
		m.ObjectsModified = obj
	case "remove":
		m.ObjectsRemoved = obj
	}
	return packet(t, uint32(protocol.ESOMsg_k_ESOMsg_UpdateMultiple), noJob, m)
}

func TestConnectLogsOnWithToken(t *testing.T) {
	ft := newFakeTransport()
	a := newTestAdapter(t, ft)
	if err := a.Connect(t.Context(), tokenCreds); err != nil {
		t.Fatal(err)
	}
	waitEvent[Connected](t, a)
	d := <-ft.logons
	if d.Username != "bot_a" || d.AccessToken != "refresh.jwt.token" || d.Password != "" || !d.ShouldRememberPassword {
		t.Errorf("logon details = %+v", d)
	}

	ft.events <- &steam.LoggedOnEvent{Result: steamlang.EResult_OK, AccountFlags: steamlang.EAccountFlags_LimitedUser | steamlang.EAccountFlags_PasswordSet}
	ev := waitEvent[LoggedOn](t, a)
	if ev.SteamID != ft.steamID || !ev.Limited() {
		t.Errorf("LoggedOn = %+v", ev)
	}
	if a.SteamID() != ft.steamID {
		t.Errorf("SteamID() = %d", a.SteamID())
	}
	if f, known := a.AccountFlags(); !known || f&AccountFlagLimitedUser == 0 {
		t.Errorf("AccountFlags() = %d, %v", f, known)
	}
}

func TestConnectNeedsCredentials(t *testing.T) {
	a := newTestAdapter(t)
	for _, c := range []Credentials{{}, {Username: "x"}, {AccessToken: "t"}} {
		if err := a.Connect(t.Context(), c); !errors.Is(err, ErrNoCredentials) {
			t.Errorf("Connect(%+v) = %v, want ErrNoCredentials", c, err)
		}
	}
}

func TestDisconnectThenReconnect(t *testing.T) {
	first, second := newFakeTransport(), newFakeTransport()
	a := newTestAdapter(t, first, second)
	if err := a.Connect(t.Context(), tokenCreds); err != nil {
		t.Fatal(err)
	}
	waitEvent[Connected](t, a)
	if err := a.Connect(t.Context(), tokenCreds); !errors.Is(err, ErrAlreadyConnected) {
		t.Fatalf("second Connect = %v, want ErrAlreadyConnected", err)
	}

	a.Disconnect()
	if d := waitEvent[Disconnected](t, a); d.Conn != 1 {
		t.Errorf("first Disconnected.Conn = %d, want 1", d.Conn)
	}
	// The pair is dropped before Disconnected is delivered, so a supervisor
	// can reconnect straight away, on a fresh transport (D11).
	if err := a.Connect(t.Context(), tokenCreds); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	waitEvent[Connected](t, a)
	<-second.logons
	a.Disconnect()
	if d := waitEvent[Disconnected](t, a); d.Conn != 2 {
		t.Errorf("second Disconnected.Conn = %d, want 2", d.Conn)
	}
}

// TestSetPlayingDoesNotBlock: go-dota2's SetPlaying(false) emits a status
// change on the caller's goroutine. With the pump stuck on a full Events()
// channel, a synchronous call would deadlock the supervisor that drains it.
func TestSetPlayingDoesNotBlock(t *testing.T) {
	ft := newFakeTransport()
	a := newTestAdapter(t, ft)
	connectWelcomed(t, a, ft)
	c := a.current()

	// Fill Events() and the dota channel behind it without reading.
	status := protocol.GCConnectionStatus_GCConnectionStatus_HAVE_SESSION
	go func() {
		for range EventBuffer + cap(c.dota) + 8 {
			select {
			case c.dota <- &devents.GCConnectionStatusChanged{OldState: status, NewState: status}:
			case <-c.stop:
				return
			}
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for len(a.events) < EventBuffer || len(c.dota) < cap(c.dota) {
		if time.Now().After(deadline) {
			t.Fatalf("buffers not full: events %d dota %d", len(a.events), len(c.dota))
		}
		time.Sleep(time.Millisecond)
	}

	done := make(chan struct{})
	go func() {
		a.SetPlaying(false)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SetPlaying(false) blocked on the event pump")
	}

	// Once the consumer drains, the NO_SESSION it caused comes through.
	timeout := time.After(3 * time.Second)
	for {
		select {
		case ev := <-a.Events():
			if s, ok := ev.(GCStatus); ok && s.New == GCNoSession {
				a.Disconnect()
				return
			}
		case <-timeout:
			t.Fatal("no NO_SESSION after SetPlaying(false)")
		}
	}
}

func TestAuthErrorEResult(t *testing.T) {
	// The messages are go-steam's authServiceCall format.
	tests := []struct {
		name string
		err  error
		want EResult
	}{
		{"totp mismatch", errors.New("UpdateAuthSessionWithSteamGuardCode failed: eresult 88 (200 OK)"), EResultTwoFactorCodeMismatch},
		{"rate limited", errors.New("BeginAuthSessionViaCredentials failed: eresult 84 (200 OK)"), EResultRateLimitExceeded},
		{"service unavailable", errors.New("GetPasswordRSAPublicKey failed: eresult 20 (Service Unavailable)"), EResult(20)},
		{"steam message", errors.New("BeginAuthSessionViaCredentials failed: eresult 5 (Invalid password)"), EResultInvalidPassword},
		{"no eresult", errors.New("begin auth session failed: something"), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := authError(&steam.AuthSessionError{State: steam.AuthSessionStateDenied, Err: tt.err})
			var ae *AuthError
			if !errors.As(err, &ae) || ae.State != "denied" || ae.Result != tt.want {
				t.Errorf("authError = %#v, want Result %d", err, tt.want)
			}
		})
	}
}

func TestDialFailure(t *testing.T) {
	bad, good := newFakeTransport(), newFakeTransport()
	bad.dialErr = errors.New("no route to CM")
	a := newTestAdapter(t, bad, good)
	if err := a.Connect(t.Context(), tokenCreds); err != nil {
		t.Fatal(err)
	}
	f, ok := nextEvent(t, a).(Fatal)
	if !ok || !strings.Contains(f.Err.Error(), "no route to CM") {
		t.Fatalf("first event = %#v, want Fatal", f)
	}
	if _, ok := nextEvent(t, a).(Disconnected); !ok {
		t.Fatal("dial failure not followed by Disconnected")
	}
	if err := a.Connect(t.Context(), tokenCreds); err != nil {
		t.Fatalf("reconnect after dial failure: %v", err)
	}
}

func TestTranslate(t *testing.T) {
	a := newTestAdapter(t)
	ft := newFakeTransport()
	c := &conn{t: ft}
	authErr := &steam.AuthSessionError{
		State:         steam.AuthSessionStateDeviceCode,
		Confirmations: []steam.SteamGuardConfirmation{{Type: unified.EAuthSessionGuardType_k_EAuthSessionGuardType_DeviceCode}},
		Err:           errors.New("steam guard confirmation required"),
	}
	queue := protocol.GCConnectionStatus_GCConnectionStatus_NO_SESSION_IN_LOGON_QUEUE
	popup := &devents.Popup{}
	popup.Id = protocol.CMsgDOTAPopup_NOT_LEAGUE_ADMIN.Enum()
	invite := &devents.InvitationCreated{}
	invite.SteamId, invite.UserOffline = new(uint64(42)), new(true)
	signed := &devents.MatchSignedOut{}
	signed.MatchId = new(uint64(9))

	tests := []struct {
		name  string
		raw   any
		want  Event
		check func(t *testing.T, ev Event)
	}{
		{name: "connected", raw: &steam.ConnectedEvent{}, want: Connected{}},
		{name: "logged off elsewhere", raw: &steam.LoggedOffEvent{Result: steamlang.EResult_LoggedInElsewhere}, want: LoggedOff{Result: EResultLoggedInElsewhere}},
		{name: "steam failure", raw: &steam.SteamFailureEvent{Result: steamlang.EResult_TryAnotherCM}, want: SteamFailure{Result: EResultTryAnotherCM}},
		{name: "playing blocked", raw: &steam.PlayingSessionStateEvent{PlayingBlocked: true, PlayingApp: 570}, want: PlayingSession{Blocked: true, AppID: 570}},
		{name: "account info", raw: &steam.AccountInfoEvent{PersonaName: "bot", AccountFlags: 4096}, want: AccountInfo{PersonaName: "bot", AccountFlags: 4096}},
		{name: "logon refused by CM", raw: &steam.LogOnFailedEvent{Result: steamlang.EResult_InvalidPassword}, want: LogOnFailed{Result: EResultInvalidPassword}},
		{
			name: "auth session failure",
			raw:  &steam.LogOnFailedEvent{Err: authErr, AuthSessionState: authErr.State},
			check: func(t *testing.T, ev Event) {
				f := ev.(LogOnFailed)
				var ae *AuthError
				if !errors.As(f.Err, &ae) || ae.State != "device_code" || !ae.NeedsGuard() || len(ae.Guards) != 1 || ae.Guards[0] != GuardDeviceCode {
					t.Errorf("got %+v", f)
				}
			},
		},
		{
			name: "fatal error",
			raw:  steam.FatalErrorEvent(errors.New("read: EOF")),
			check: func(t *testing.T, ev Event) {
				if f, ok := ev.(Fatal); !ok || f.Err.Error() != "read: EOF" {
					t.Errorf("got %#v", ev)
				}
			},
		},
		{
			name: "gc queue",
			raw: &devents.GCConnectionStatusChanged{
				OldState: protocol.GCConnectionStatus_GCConnectionStatus_NO_SESSION, NewState: queue,
				Update: &protocol.CMsgConnectionStatus{Status: &queue, QueuePosition: new(int32(3)), QueueSize: new(int32(10))},
			},
			want: GCStatus{Old: GCNoSession, New: GCNoSessionInLogonQueue, QueuePosition: 3, QueueSize: 10},
		},
		{name: "welcomed", raw: &devents.ClientWelcomed{}, want: GCWelcomed{}},
		{name: "popup", raw: popup, want: Popup{ID: 27, Name: "NOT_LEAGUE_ADMIN"}},
		{name: "invitation", raw: invite, want: InvitationCreated{SteamID: 42, Offline: true}},
		{name: "match signed out", raw: signed, want: MatchSignedOut{MatchID: 9}},
		{name: "already translated", raw: PracticeLobbyResponse{EResult: 1}, check: func(t *testing.T, ev Event) {
			if r, ok := ev.(PracticeLobbyResponse); !ok || r.EResult != 1 {
				t.Errorf("got %#v", ev)
			}
		}},
		{name: "unhandled gc packet dropped", raw: &devents.UnhandledGCPacket{}},
		{name: "cm list dropped", raw: &steam.ClientCMListEvent{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, ok := a.translate(c, tt.raw)
			switch {
			case tt.check != nil:
				if !ok {
					t.Fatal("dropped")
				}
				tt.check(t, ev)
			case tt.want == nil:
				if ok {
					t.Errorf("got %#v, want dropped", ev)
				}
			default:
				if !ok || fmt.Sprintf("%#v", ev) != fmt.Sprintf("%#v", tt.want) {
					t.Errorf("got %#v, want %#v", ev, tt.want)
				}
			}
		})
	}
}

func TestCreateLobby(t *testing.T) {
	type result struct {
		v   *LobbyView
		err error
	}
	start := func(t *testing.T, a *Adapter, d LobbyDetails) chan result {
		ch := make(chan result, 1)
		go func() {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			v, err := a.CreateLobby(ctx, d)
			ch <- result{v, err}
		}()
		return ch
	}
	get := func(t *testing.T, ch chan result) result {
		t.Helper()
		select {
		case r := <-ch:
			return r
		case <-time.After(3 * time.Second):
			t.Fatal("CreateLobby did not return")
			return result{}
		}
	}
	details := LobbyDetails{GameName: "match 1", PassKey: "pw", LeagueID: 15000, TVDelaySeconds: 120, Visibility: VisibilityUnlisted}

	t.Run("job addressed create then SO", func(t *testing.T) {
		ft := newFakeTransport()
		a := newTestAdapter(t, ft)
		connectWelcomed(t, a, ft)
		res := start(t, a, details)

		msg := ft.waitWrite(t, msgPracticeLobbyCreate)
		job := uint64(msg.GetSourceJobId())
		if job <= tapJobBase {
			t.Fatalf("source job %d not in the tap range", job)
		}
		body := msg.Body.(*protocol.CMsgPracticeLobbyCreate)
		if body.GetPassKey() != "pw" || body.GetLobbyDetails().GetLeagueid() != 15000 || int32(body.GetLobbyDetails().GetDotaTvDelay()) != 2 {
			t.Errorf("create body = %+v", body)
		}

		ft.deliver(packet(t, msgPracticeLobbyResponse, job, &protocol.CMsgGenericResult{Eresult: new(uint32(1))}))
		ft.deliver(lobbySO(t, "add", &protocol.CSODOTALobby{LobbyId: new(uint64(555)), LeaderId: new(ft.steamID), Leagueid: new(uint32(15000))}))
		r := get(t, res)
		if r.err != nil || r.v.LobbyID != 555 || r.v.LeagueID != 15000 || r.v.LeaderID != ft.steamID {
			t.Fatalf("got %+v, %v", r.v, r.err)
		}
		if ev := waitEvent[PracticeLobbyResponse](t, a); ev.TargetJobID != job || ev.EResult != 1 {
			t.Errorf("7055 event = %+v", ev)
		}
		if v, ok := a.CurrentLobby(); !ok || v.LobbyID != 555 {
			t.Errorf("CurrentLobby = %+v, %v", v, ok)
		}
	})

	t.Run("failed 7055 for our job", func(t *testing.T) {
		ft := newFakeTransport()
		a := newTestAdapter(t, ft)
		connectWelcomed(t, a, ft)
		res := start(t, a, details)
		job := uint64(ft.waitWrite(t, msgPracticeLobbyCreate).GetSourceJobId())

		// A 7055 for some other op must not decide our create.
		ft.deliver(packet(t, msgPracticeLobbyResponse, job+100, &protocol.CMsgGenericResult{Eresult: new(uint32(2))}))
		ft.deliver(packet(t, msgPracticeLobbyResponse, job, &protocol.CMsgGenericResult{Eresult: new(uint32(2)), DebugMessage: new("in a lobby")}))
		r := get(t, res)
		var ce *CreateError
		if !errors.As(r.err, &ce) || ce.Reason != CreateFailed || ce.EResult != 2 || ce.DebugMessage != "in a lobby" {
			t.Fatalf("err = %v", r.err)
		}
	})

	t.Run("not league admin popup", func(t *testing.T) {
		ft := newFakeTransport()
		a := newTestAdapter(t, ft)
		connectWelcomed(t, a, ft)
		res := start(t, a, details)
		ft.waitWrite(t, msgPracticeLobbyCreate)
		ft.deliver(packet(t, msgPopup, noJob, &protocol.CMsgDOTAPopup{Id: protocol.CMsgDOTAPopup_NOT_LEAGUE_ADMIN.Enum()}))
		r := get(t, res)
		var ce *CreateError
		if !errors.As(r.err, &ce) || ce.Reason != CreateLeagueRejected || ce.PopupID != 27 {
			t.Fatalf("err = %v", r.err)
		}
	})

	t.Run("leaves stale lobby first", func(t *testing.T) {
		ft := newFakeTransport()
		a := newTestAdapter(t, ft)
		connectWelcomed(t, a, ft)
		stale := &protocol.CSODOTALobby{LobbyId: new(uint64(1)), LeaderId: new(uint64(999))}
		ft.deliver(lobbySO(t, "add", stale))

		res := start(t, a, LobbyDetails{GameName: "x"})
		ft.waitWrite(t, uint32(protocol.EDOTAGCMsg_k_EMsgGCPracticeLobbyLeave))
		ft.deliver(lobbySO(t, "remove", stale))
		ft.waitWrite(t, msgPracticeLobbyCreate)
		ft.deliver(lobbySO(t, "add", &protocol.CSODOTALobby{LobbyId: new(uint64(2)), LeaderId: new(ft.steamID)}))
		if r := get(t, res); r.err != nil || r.v.LobbyID != 2 {
			t.Fatalf("got %+v, %v", r.v, r.err)
		}
	})

	t.Run("needs a GC session", func(t *testing.T) {
		ft := newFakeTransport()
		a := newTestAdapter(t, ft)
		if _, err := a.CreateLobby(t.Context(), details); !errors.Is(err, ErrNotConnected) {
			t.Errorf("disconnected: %v", err)
		}
		if err := a.Connect(t.Context(), tokenCreds); err != nil {
			t.Fatal(err)
		}
		waitEvent[Connected](t, a)
		if _, err := a.CreateLobby(t.Context(), details); !errors.Is(err, ErrNotReady) {
			t.Errorf("before welcome: %v", err)
		}
	})

	t.Run("bad tv delay", func(t *testing.T) {
		ft := newFakeTransport()
		a := newTestAdapter(t, ft)
		connectWelcomed(t, a, ft)
		if _, err := a.CreateLobby(t.Context(), LobbyDetails{TVDelaySeconds: 30}); err == nil {
			t.Error("accepted a 30 s tv delay")
		}
	})
}

func TestListAdminLeagues(t *testing.T) {
	list := &protocol.CMsgDOTALeagueInfoList{Infos: []*protocol.CMsgDOTALeagueInfo{{LeagueId: new(uint32(15000)), Name: new("Inhouse")}}}
	tests := []struct {
		name      string
		reply     func(job uint64) (uint64, bool) // target job to reply with, or none
		wantErr   error
		wantCount int
	}{
		{name: "GC echoes job", reply: func(job uint64) (uint64, bool) { return job, true }, wantCount: 1},
		{name: "no echo, tap fallback", reply: func(uint64) (uint64, bool) { return noJob, true }, wantCount: 1},
		{name: "no reply", reply: func(uint64) (uint64, bool) { return 0, false }, wantErr: context.DeadlineExceeded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ft := newFakeTransport()
			a := newTestAdapter(t, ft)
			connectWelcomed(t, a, ft)

			type result struct {
				l   []LeagueInfo
				err error
			}
			res := make(chan result, 1)
			go func() {
				ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
				defer cancel()
				l, err := a.ListAdminLeagues(ctx)
				res <- result{l, err}
			}()
			job := uint64(ft.waitWrite(t, msgLeagueAdminsRequest).GetSourceJobId())
			if target, ok := tt.reply(job); ok {
				ft.deliver(packet(t, msgLeagueAdminsResponse, target, list))
			}
			r := <-res
			if tt.wantErr != nil {
				if !errors.Is(r.err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", r.err, tt.wantErr)
				}
				return
			}
			if r.err != nil || len(r.l) != tt.wantCount || r.l[0].LeagueID != 15000 || r.l[0].Name != "Inhouse" {
				t.Fatalf("got %+v, %v", r.l, r.err)
			}
		})
	}

	t.Run("not ready before welcome", func(t *testing.T) {
		ft := newFakeTransport()
		a := newTestAdapter(t, ft)
		if err := a.Connect(t.Context(), tokenCreds); err != nil {
			t.Fatal(err)
		}
		waitEvent[Connected](t, a)
		if _, err := a.ListAdminLeagues(t.Context()); !errors.Is(err, ErrNotReady) {
			t.Errorf("err = %v, want ErrNotReady", err)
		}
	})
}

func TestIsLeagueAdmin(t *testing.T) {
	ft := newFakeTransport()
	a := newTestAdapter(t, ft)
	if _, known := a.IsLeagueAdmin(); known {
		t.Error("known while disconnected")
	}
	connectWelcomed(t, a, ft)
	if _, known := a.IsLeagueAdmin(); known {
		t.Error("known before the SO arrived")
	}
	obj := &protocol.CSODOTAGameAccountClient{AccountId: new(uint32(1)), IsLeagueAdmin: new(true)}
	ft.deliver(packet(t, uint32(protocol.ESOMsg_k_ESOMsg_UpdateMultiple), noJob, &protocol.CMsgSOMultipleObjects{
		OwnerSoid:    &protocol.CMsgSOIDOwner{Type: new(uint32(1)), Id: new(ft.steamID)},
		ObjectsAdded: []*protocol.CMsgSOMultipleObjects_SingleObject{{TypeId: new(int32(2002)), ObjectData: mustMarshal(t, obj)}},
	}))
	if admin, known := a.IsLeagueAdmin(); !admin || !known {
		t.Errorf("IsLeagueAdmin = %v, %v", admin, known)
	}
}

func TestSubscribeLobby(t *testing.T) {
	ft := newFakeTransport()
	a := newTestAdapter(t, ft)
	ch, cancel := a.SubscribeLobby()
	defer cancel()
	connectWelcomed(t, a, ft)

	l := &protocol.CSODOTALobby{LobbyId: new(uint64(7))}
	ft.deliver(lobbySO(t, "add", l))
	l.MatchId = new(uint64(99))
	ft.deliver(lobbySO(t, "modify", l))
	ft.deliver(lobbySO(t, "remove", l))

	want := []struct {
		kind    LobbyEventKind
		matchID uint64
	}{{LobbyCreated, 0}, {LobbyUpdated, 99}, {LobbyDestroyed, 99}}
	for i, w := range want {
		select {
		case ev := <-ch:
			if ev.Kind != w.kind || ev.Lobby.LobbyID != 7 || ev.Lobby.MatchID != w.matchID {
				t.Errorf("event %d = %s %+v, want %s match %d", i, ev.Kind, ev.Lobby, w.kind, w.matchID)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("no lobby event %d", i)
		}
	}
}

func TestAuthenticate(t *testing.T) {
	guardErr := &steam.AuthSessionError{
		State:         steam.AuthSessionStateDeviceCode,
		Confirmations: []steam.SteamGuardConfirmation{{Type: unified.EAuthSessionGuardType_k_EAuthSessionGuardType_DeviceCode}},
	}
	tests := []struct {
		name      string
		creds     Credentials
		steamErr  error
		wantToken string
		wantErr   func(error) bool
	}{
		{
			name:      "password and totp",
			creds:     Credentials{Username: "bot_a", Password: "hunter2", TwoFactorCode: config.Secret("BC7K9"), DeviceName: "dota_lobby"},
			wantToken: "new.refresh.token",
		},
		{
			name:    "no password",
			creds:   Credentials{Username: "bot_a", AccessToken: "t"},
			wantErr: func(err error) bool { return errors.Is(err, ErrNoCredentials) },
		},
		{
			name:     "guard required",
			creds:    Credentials{Username: "bot_a", Password: "hunter2"},
			steamErr: guardErr,
			wantErr: func(err error) bool {
				var ae *AuthError
				return errors.As(err, &ae) && ae.NeedsGuard() && ae.State == "device_code"
			},
		},
		{
			name:     "transport error",
			creds:    Credentials{Username: "bot_a", Password: "hunter2"},
			steamErr: errors.New("dial tcp: timeout"),
			wantErr: func(err error) bool {
				var ae *AuthError
				return !errors.As(err, &ae) && strings.Contains(err.Error(), "dial tcp")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newTestAdapter(t)
			var got *steam.LogOnDetails
			a.authenticate = func(_ context.Context, d *steam.LogOnDetails) (string, error) {
				got = d
				if tt.steamErr != nil {
					return "", tt.steamErr
				}
				return "new.refresh.token", nil
			}
			token, err := a.Authenticate(t.Context(), tt.creds)
			if tt.wantErr != nil {
				if err == nil || !tt.wantErr(err) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil || token != tt.wantToken {
				t.Fatalf("got %q, %v", token, err)
			}
			if got.Password != "hunter2" || got.TwoFactorCode != "BC7K9" || got.AccessToken != "" || got.DeviceFriendlyName != "dota_lobby" {
				t.Errorf("details = %+v", got)
			}
		})
	}
}

func TestConfirmSteamGuard(t *testing.T) {
	email := []steam.SteamGuardConfirmation{{Type: unified.EAuthSessionGuardType_k_EAuthSessionGuardType_EmailCode}}
	both := append([]steam.SteamGuardConfirmation{{Type: unified.EAuthSessionGuardType_k_EAuthSessionGuardType_DeviceCode}}, email...)
	approve := []steam.SteamGuardConfirmation{{Type: unified.EAuthSessionGuardType_k_EAuthSessionGuardType_DeviceConfirmation}}
	prompt := func(_ context.Context, kinds []GuardKind) (string, error) { return "CODE" + string(kinds[0][0]), nil }

	tests := []struct {
		name     string
		prompt   func(context.Context, []GuardKind) (string, error)
		confs    []steam.SteamGuardConfirmation
		wantCode string
		wantType unified.EAuthSessionGuardType
		wantErr  error
	}{
		{name: "no prompt fails fast", confs: both, wantErr: ErrSteamGuardRequired},
		{name: "prefers authenticator", prompt: prompt, confs: both, wantCode: "CODEd", wantType: unified.EAuthSessionGuardType_k_EAuthSessionGuardType_DeviceCode},
		{name: "email only", prompt: prompt, confs: email, wantCode: "CODEe", wantType: unified.EAuthSessionGuardType_k_EAuthSessionGuardType_EmailCode},
		{name: "approval only cannot be typed", prompt: prompt, confs: approve, wantErr: ErrSteamGuardRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := logOnDetails(Credentials{Username: "u", Password: "p", SteamGuard: tt.prompt}, false)
			code, kind, err := d.ConfirmSteamGuard(t.Context(), tt.confs)
			if !errors.Is(err, tt.wantErr) || code != tt.wantCode || (err == nil && kind != tt.wantType) {
				t.Errorf("got %q, %v, %v", code, kind, err)
			}
		})
	}
}

func TestCredentialsRedact(t *testing.T) {
	c := Credentials{Username: "bot_a", AccessToken: config.Secret("tok-SECRET"), Password: config.Secret("pw-SECRET")}
	for _, s := range []string{fmt.Sprintf("%v", c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c)} {
		if strings.Contains(s, "SECRET") {
			t.Errorf("secret leaked: %s", s)
		}
	}
}

// SetPlaying(false) is queued for playLoop, and Disconnect used to cancel the
// connection at once, so the stop announcement was usually never sent.
func TestDisconnectFlushesSetPlayingFalse(t *testing.T) {
	tests := []struct {
		name string
		play func(a *Adapter)
		// wantSent is whether a stop announcement (SetGamesPlayed with no
		// games) must reach the transport while the connection is still open.
		wantSent bool
	}{
		{"pending SetPlaying(false)", func(a *Adapter) { a.SetPlaying(false) }, true},
		{"nothing pending", func(*Adapter) {}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ft := newFakeTransport()
			a := newTestAdapter(t, ft)
			connectWelcomed(t, a, ft)
			c := a.current()
			var mu sync.Mutex
			var stops, stopsOpen int
			ft.mu.Lock()
			ft.onGamesPlayed = func(ids []uint64) {
				mu.Lock()
				defer mu.Unlock()
				if len(ids) == 0 {
					stops++
					if c.ctx.Err() == nil {
						stopsOpen++
					}
				}
			}
			ft.mu.Unlock()

			tt.play(a)
			a.Disconnect()

			mu.Lock()
			defer mu.Unlock()
			if tt.wantSent && (stops != 1 || stopsOpen != 1) {
				t.Errorf("stop announcements = %d, sent before close = %d; want 1 and 1", stops, stopsOpen)
			}
			if !tt.wantSent && stops != 0 {
				t.Errorf("stop announcements = %d, want none", stops)
			}
		})
	}
}

// With the pump wedged the flush must give up, not hang the caller of
// Disconnect (the supervisor's run loop, which is what drains Events()).
func TestDisconnectFlushIsBounded(t *testing.T) {
	ft := newFakeTransport()
	a := newTestAdapter(t, ft)
	a.opts.PlayFlushTimeout = 50 * time.Millisecond
	connectWelcomed(t, a, ft)
	c := a.current()

	status := protocol.GCConnectionStatus_GCConnectionStatus_HAVE_SESSION
	go func() {
		for range EventBuffer + cap(c.dota) + 8 {
			select {
			case c.dota <- &devents.GCConnectionStatusChanged{OldState: status, NewState: status}:
			case <-c.stop:
				return
			}
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for len(a.events) < EventBuffer || len(c.dota) < cap(c.dota) {
		if time.Now().After(deadline) {
			t.Fatalf("buffers not full: events %d dota %d", len(a.events), len(c.dota))
		}
		time.Sleep(time.Millisecond)
	}

	a.SetPlaying(false)
	done := make(chan struct{})
	go func() {
		a.Disconnect()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Disconnect blocked on a pending SetPlaying(false)")
	}
	if c.ctx.Err() == nil {
		t.Error("connection not cancelled")
	}
}
