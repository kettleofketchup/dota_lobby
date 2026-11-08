package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// reporter writes one JSON line per observation to <state>/report.jsonl and
// a readable line to stderr. Lines are tagged with the research question
// they answer so the write-up can filter by Q.
type reporter struct {
	mu   sync.Mutex
	f    *os.File
	user string
	cmd  string
	// quiet hides raw gc_in/gc_out traffic from stderr (it is still in the file).
	quiet bool
}

func newReporter(stateDir, user, cmd string, quiet bool) (*reporter, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(stateDir, "report.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open report: %w", err)
	}
	return &reporter{f: f, user: user, cmd: cmd, quiet: quiet}, nil
}

func (r *reporter) emit(q, event string, fields map[string]any) {
	line := map[string]any{
		"ts":    time.Now().UTC().Format(time.RFC3339Nano),
		"user":  r.user,
		"cmd":   r.cmd,
		"event": event,
	}
	if q != "" {
		line["q"] = q
	}
	for k, v := range fields {
		line[k] = v
	}
	b, err := json.Marshal(line)
	if err != nil {
		b = []byte(fmt.Sprintf(`{"event":"marshal_error","error":%q}`, err.Error()))
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	_, _ = r.f.Write(append(b, '\n'))
	if r.quiet && (event == "gc_in" || event == "gc_out") {
		return
	}
	tag := event
	if q != "" {
		tag = q + " " + event
	}
	extra, _ := json.Marshal(fields)
	if fields == nil {
		extra = nil
	}
	fmt.Fprintf(os.Stderr, "%s %s\n", tag, extra)
}

func (r *reporter) close() { _ = r.f.Close() }
