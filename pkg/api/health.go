package api

import (
	"net/http"
	"time"
)

// Liveness must not depend on Steam or the GC: a Steam outage would turn
// into a restart loop and a login storm (07 §1). /livez only checks that
// the process serves and that no bot loop is wedged.
func (s *Server) handleLivez(w http.ResponseWriter, _ *http.Request) {
	if stalled := s.opts.Bots.Stalled(s.opts.WatchdogMaxAge); len(stalled) > 0 {
		// /livez is open, so it reports a count; names stay behind /v1/bots
		// and in the log.
		s.log.Error("bot run loop stalled", "bots", stalled)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "stalled", "stalled_bots": len(stalled)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

type readiness struct {
	Status       string `json:"status"`
	Draining     bool   `json:"draining"`
	BotsReady    int    `json:"bots_ready"`
	BotsTotal    int    `json:"bots_total"`
	MinReadyBots int    `json:"min_ready_bots"`
}

func (s *Server) readiness() readiness {
	sts := s.opts.Bots.Statuses()
	r := readiness{
		Draining:     s.draining.Load(),
		BotsTotal:    len(sts),
		MinReadyBots: s.opts.MinReadyBots,
	}
	for _, st := range sts {
		if st.Ready {
			r.BotsReady++
		}
	}
	switch {
	case r.Draining:
		r.Status = "draining"
	case r.BotsReady < r.MinReadyBots:
		r.Status = "not_ready"
	default:
		r.Status = "ready"
	}
	return r
}

func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	r := s.readiness()
	status := http.StatusOK
	if r.Status != "ready" {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, r)
}

type healthBody struct {
	Status       string `json:"status"`
	Ready        bool   `json:"ready"`
	Draining     bool   `json:"draining"`
	BotsReady    int    `json:"bots_ready"`
	BotsTotal    int    `json:"bots_total"`
	MinReadyBots int    `json:"min_ready_bots"`
	// States counts bots per state; names stay behind /v1/bots.
	States        map[string]int `json:"states"`
	StalledBots   int            `json:"stalled_bots"`
	Version       string         `json:"version,omitempty"`
	UptimeSeconds int64          `json:"uptime_seconds"`
	Time          time.Time      `json:"time"`
}

// /health is kept for old monitors: always 200, with the detail in the
// body.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	now := s.opts.Now()
	r := s.readiness()
	states := map[string]int{}
	for _, st := range s.opts.Bots.Statuses() {
		states[string(st.State)]++
	}
	writeJSON(w, http.StatusOK, healthBody{
		Status:        "healthy",
		Ready:         r.Status == "ready",
		Draining:      r.Draining,
		BotsReady:     r.BotsReady,
		BotsTotal:     r.BotsTotal,
		MinReadyBots:  r.MinReadyBots,
		States:        states,
		StalledBots:   len(s.opts.Bots.Stalled(s.opts.WatchdogMaxAge)),
		Version:       s.opts.Version,
		UptimeSeconds: int64(now.Sub(s.started) / time.Second),
		Time:          now.UTC(),
	})
}
