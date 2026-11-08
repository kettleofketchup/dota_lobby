package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/bot"
)

// botView is one bot in /v1/bots. 64-bit IDs are strings, and
// unknown values are null rather than zero. Credentials never appear.
type botView struct {
	Name     string    `json:"name"`
	Username string    `json:"username"`
	State    bot.State `json:"state"`
	Ready    bool      `json:"ready"`
	Reason   *string   `json:"reason"`
	// LastError is the last Steam EResult name, e.g. "LoggedInElsewhere".
	LastError *string `json:"last_error"`
	// LastErrorMessage is the last error text, which may have no EResult.
	LastErrorMessage *string    `json:"last_error_message"`
	Since            time.Time  `json:"since"`
	SteamID          *string    `json:"steam_id"`
	Limited          bool       `json:"limited"`
	LobbyStale       bool       `json:"lobby_stale"`
	TokenExpiresAt   *time.Time `json:"token_expires_at"`
	// RenewBlocked is why background token renewal stopped, e.g.
	// "bad_credentials"; the bot's connection is unaffected.
	RenewBlocked    *string `json:"renew_blocked"`
	Attempt         int     `json:"attempt"`
	GCQueuePosition *int32  `json:"gc_queue_position"`
}

func newBotView(st bot.Status) botView {
	v := botView{
		Name:             st.Name,
		Username:         st.Username,
		State:            st.State,
		Ready:            st.Ready,
		Reason:           nonEmpty(st.Reason),
		LastError:        nonEmpty(st.LastResult),
		LastErrorMessage: nonEmpty(st.LastError),
		RenewBlocked:     nonEmpty(st.RenewBlocked),
		Since:            st.Since.UTC(),
		Limited:          st.Limited,
		LobbyStale:       st.LobbyStale,
		Attempt:          st.Attempt,
	}
	if st.SteamID != 0 {
		id := strconv.FormatUint(st.SteamID, 10)
		v.SteamID = &id
	}
	if !st.TokenExpiresAt.IsZero() {
		t := st.TokenExpiresAt.UTC()
		v.TokenExpiresAt = &t
	}
	if st.State == bot.StateGCQueued {
		q := st.QueuePosition
		v.GCQueuePosition = &q
	}
	return v
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (s *Server) handleBots(w http.ResponseWriter, _ *http.Request) {
	sts := s.opts.Bots.Statuses()
	out := make([]botView, len(sts))
	for i, st := range sts {
		out[i] = newBotView(st)
	}
	writeJSON(w, http.StatusOK, map[string]any{"bots": out})
}

func (s *Server) handleBot(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	for _, st := range s.opts.Bots.Statuses() {
		if strings.EqualFold(st.Name, name) {
			writeJSON(w, http.StatusOK, newBotView(st))
			return
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "no bot named "+strconv.Quote(name))
}

// handleBotRetry is the operator's way out of needs_attention,
// e.g. after `dota_lobby auth --bot <name> --force` stored a new token or a
// password was fixed. It is a no-op for a bot that is not waiting.
func (s *Server) handleBotRetry(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !s.opts.Bots.Retry(name) {
		writeError(w, http.StatusNotFound, "not_found", "no bot named "+strconv.Quote(name))
		return
	}
	s.log.Info("bot retry requested", "bot", name)
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "retry_requested"})
}
