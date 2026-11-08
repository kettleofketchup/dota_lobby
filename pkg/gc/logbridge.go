package gc

import (
	"context"
	"io"
	"log/slog"

	"github.com/sirupsen/logrus"
)

// newLogrus gives go-dota2 the logrus.FieldLogger it wants, forwarding every
// entry to slog so the service has one log stream.
func newLogrus(log *slog.Logger) *logrus.Logger {
	l := logrus.New()
	l.SetOutput(io.Discard)
	l.SetFormatter(nopFormatter{})
	l.SetLevel(logrus.InfoLevel)
	if log.Enabled(context.Background(), slog.LevelDebug) {
		l.SetLevel(logrus.DebugLevel)
	}
	l.AddHook(slogHook{log: log})
	return l
}

// nopFormatter skips formatting, since the output is discarded anyway.
type nopFormatter struct{}

func (nopFormatter) Format(*logrus.Entry) ([]byte, error) { return nil, nil }

type slogHook struct{ log *slog.Logger }

func (slogHook) Levels() []logrus.Level { return logrus.AllLevels }

func (h slogHook) Fire(e *logrus.Entry) error {
	lvl := slog.LevelDebug
	switch {
	case e.Level <= logrus.ErrorLevel:
		lvl = slog.LevelError
	case e.Level == logrus.WarnLevel:
		lvl = slog.LevelWarn
	case e.Level == logrus.InfoLevel:
		lvl = slog.LevelInfo
	}
	args := make([]any, 0, 2*len(e.Data)+2)
	args = append(args, "src", "go-dota2")
	for k, v := range e.Data {
		args = append(args, k, v)
	}
	h.log.Log(context.Background(), lvl, e.Message, args...)
	return nil
}
