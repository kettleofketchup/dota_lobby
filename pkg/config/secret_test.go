package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const raw = "hunter2-raw-value"

type holder struct {
	User     string
	Password Secret
	Map      map[string]Secret
	Ptr      *Secret
}

func TestSecretRedactsInFmt(t *testing.T) {
	s := Secret(raw)
	h := holder{User: "u", Password: s, Map: map[string]Secret{"k": s}, Ptr: &s}

	cases := []struct {
		format string
		arg    any
		want   string
	}{
		{"%v", s, redacted},
		{"%s", s, redacted},
		{"%q", s, `"` + redacted + `"`},
		{"%x", s, fmt.Sprintf("%x", redacted)},
		{"%10.3s", s, "       ***"},
		{"%#v", s, `config.Secret("` + redacted + `")`},
		{"%d", s, ""},
		{"%v", h, ""},
		{"%+v", h, ""},
		{"%#v", h, ""},
		{"%v", []Secret{s}, ""},
		{"%s", fmt.Sprint(s), redacted},
	}
	for _, tc := range cases {
		t.Run(tc.format, func(t *testing.T) {
			got := fmt.Sprintf(tc.format, tc.arg)
			if strings.Contains(got, raw) {
				t.Fatalf("%s leaked the value: %s", tc.format, got)
			}
			if tc.want != "" && got != tc.want {
				t.Fatalf("%s = %q, want %q", tc.format, got, tc.want)
			}
		})
	}
}

func TestSecretEmpty(t *testing.T) {
	var s Secret
	if s.IsSet() || s.String() != "" || fmt.Sprint(s) != "" {
		t.Fatalf("empty secret should print empty, got %q", fmt.Sprint(s))
	}
	if got := fmt.Sprintf("%#v", s); got != `config.Secret("")` {
		t.Fatalf("%%#v = %s", got)
	}
	b, _ := json.Marshal(s)
	if string(b) != `""` {
		t.Fatalf("json = %s", b)
	}
}

func TestSecretReveal(t *testing.T) {
	if got := Secret(raw).Reveal(); got != raw {
		t.Fatalf("Reveal = %q", got)
	}
}

func TestSecretRedactsInEncoders(t *testing.T) {
	s := Secret(raw)
	h := holder{User: "u", Password: s, Map: map[string]Secret{"k": s}, Ptr: &s}

	j, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	y, err := yaml.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	text, err := s.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]string{"json": string(j), "yaml": string(y), "text": string(text)} {
		if strings.Contains(out, raw) {
			t.Errorf("%s leaked the value: %s", name, out)
		}
		if !strings.Contains(out, redacted) {
			t.Errorf("%s has no redaction marker: %s", name, out)
		}
	}
}

func TestSecretRedactsInSlog(t *testing.T) {
	s := Secret(raw)
	h := holder{User: "u", Password: s, Map: map[string]Secret{"k": s}, Ptr: &s}

	handlers := map[string]func(*bytes.Buffer) slog.Handler{
		"json": func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
		"text": func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
	}
	for name, mk := range handlers {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(mk(&buf))
			log.Info("msg", "secret", s, "ptr", &s, "struct", h, "group", slog.Group("g", "p", s))
			if strings.Contains(buf.String(), raw) {
				t.Fatalf("slog %s leaked the value: %s", name, buf.String())
			}
			if !strings.Contains(buf.String(), redacted) {
				t.Fatalf("slog %s has no redaction marker: %s", name, buf.String())
			}
		})
	}
}
