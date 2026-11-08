package gc

import (
	"log/slog"
	"testing"

	"github.com/paralin/go-dota2/protocol"
	"github.com/paralin/go-steam/protocol/gamecoordinator"
)

func TestTapPracticeLobbyResponse(t *testing.T) {
	tests := []struct {
		name      string
		pkt       func(t *testing.T) *gamecoordinator.GCPacket
		wantEvent bool
		want      PracticeLobbyResponse
	}{
		{
			name: "ok, job addressed",
			pkt: func(t *testing.T) *gamecoordinator.GCPacket {
				return packet(t, msgPracticeLobbyResponse, tapJobBase+1, &protocol.CMsgGenericResult{Eresult: new(uint32(1))})
			},
			wantEvent: true,
			want:      PracticeLobbyResponse{EResult: 1, TargetJobID: tapJobBase + 1},
		},
		{
			name: "failure with debug message, no job",
			pkt: func(t *testing.T) *gamecoordinator.GCPacket {
				return packet(t, msgPracticeLobbyResponse, noJob, &protocol.CMsgGenericResult{Eresult: new(uint32(2)), DebugMessage: new("already in a lobby")})
			},
			wantEvent: true,
			want:      PracticeLobbyResponse{EResult: 2, DebugMessage: "already in a lobby", TargetJobID: noJob},
		},
		{
			name: "eresult omitted decodes as the proto default 2",
			pkt: func(t *testing.T) *gamecoordinator.GCPacket {
				return packet(t, msgPracticeLobbyResponse, 5, &protocol.CMsgGenericResult{})
			},
			wantEvent: true,
			want:      PracticeLobbyResponse{EResult: 2, TargetJobID: 5},
		},
		{
			name: "undecodable body is dropped",
			pkt: func(t *testing.T) *gamecoordinator.GCPacket {
				return &gamecoordinator.GCPacket{AppId: 570, MsgType: msgPracticeLobbyResponse, IsProto: true, Body: []byte{0x0a}}
			},
		},
		{
			name: "other app is ignored",
			pkt: func(t *testing.T) *gamecoordinator.GCPacket {
				p := packet(t, msgPracticeLobbyResponse, 1, &protocol.CMsgGenericResult{Eresult: new(uint32(1))})
				p.AppId = 440
				return p
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []Event
			tp := newTap(slog.New(slog.DiscardHandler), func(ev Event) { got = append(got, ev) })
			p := tt.pkt(t)
			tp.HandleGCPacket(p)
			if !tt.wantEvent {
				if len(got) != 0 {
					t.Fatalf("got events %+v, want none", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("got %d events, want 1", len(got))
			}
			ev, ok := got[0].(PracticeLobbyResponse)
			if !ok {
				t.Fatalf("event %T", got[0])
			}
			if ev.EResult != tt.want.EResult || ev.DebugMessage != tt.want.DebugMessage || ev.TargetJobID != tt.want.TargetJobID {
				t.Errorf("got %+v, want %+v", ev, tt.want)
			}
			if string(ev.Raw) != string(p.Body) {
				t.Error("Raw does not hold the body")
			}
		})
	}
}

func TestTapWatchers(t *testing.T) {
	tp := newTap(slog.New(slog.DiscardHandler), func(Event) {})
	leagues, stopLeagues := tp.watch(msgLeagueAdminsResponse)
	popups, stopPopups := tp.watch(msgPopup)
	defer stopPopups()

	id := protocol.CMsgDOTAPopup_NOT_LEAGUE_ADMIN
	tp.HandleGCPacket(packet(t, msgPopup, noJob, &protocol.CMsgDOTAPopup{Id: &id}))
	tp.HandleGCPacket(packet(t, msgLeagueAdminsResponse, 77, &protocol.CMsgDOTALeagueInfoList{
		Infos: []*protocol.CMsgDOTALeagueInfo{{LeagueId: new(uint32(9))}},
	}))

	select {
	case p := <-popups:
		pop, err := decodePopup(p.Body)
		if err != nil || pop.ID != PopupNotLeagueAdmin || pop.Name != "NOT_LEAGUE_ADMIN" {
			t.Errorf("popup = %+v, %v", pop, err)
		}
	default:
		t.Fatal("popup watcher got nothing")
	}
	select {
	case p := <-leagues:
		if uint64(p.TargetJobId) != 77 {
			t.Errorf("target job %d", p.TargetJobId)
		}
		infos, err := decodeLeagueList(p.Body)
		if err != nil || len(infos) != 1 || infos[0].LeagueID != 9 {
			t.Errorf("leagues = %+v, %v", infos, err)
		}
	default:
		t.Fatal("league watcher got nothing")
	}
	if len(popups) != 0 || len(leagues) != 0 {
		t.Error("a watcher got a packet of another type")
	}

	// After cancelling, nothing more is delivered, and a full watcher never
	// blocks the reader.
	stopLeagues()
	tp.HandleGCPacket(packet(t, msgLeagueAdminsResponse, 1, &protocol.CMsgDOTALeagueInfoList{}))
	if len(leagues) != 0 {
		t.Error("cancelled watcher still receives")
	}
	for range 50 {
		tp.HandleGCPacket(packet(t, msgPopup, noJob, &protocol.CMsgDOTAPopup{Id: &id}))
	}
}

func TestTapJobIDsAreHigh(t *testing.T) {
	tp := newTap(slog.New(slog.DiscardHandler), func(Event) {})
	a, b := tp.nextJob(), tp.nextJob()
	if a <= tapJobBase || b != a+1 {
		t.Errorf("jobs %d, %d; want consecutive above %d", a, b, tapJobBase)
	}
}
