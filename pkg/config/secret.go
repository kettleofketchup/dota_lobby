package config

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strconv"
)

const redacted = "***REDACTED***"

// Secret holds a sensitive string. Every formatting and encoding path prints
// a redaction marker instead of the value, so a Secret can sit in a struct
// that gets logged. Call Reveal only at the point the value is sent somewhere.
//
// Persisted structs must use a plain string: MarshalJSON redacts, so a Secret
// written to disk would replace the value with the marker (07 A14).
//
// fmt cannot call methods on unexported struct fields, so a Secret stored in
// an unexported field is printed raw by %v. Keep Secret fields exported.
type Secret string

var (
	_ fmt.Stringer   = Secret("")
	_ fmt.GoStringer = Secret("")
	_ fmt.Formatter  = Secret("")
	_ json.Marshaler = Secret("")
	_ slog.LogValuer = Secret("")
)

// Reveal returns the underlying value.
func (s Secret) Reveal() string { return string(s) }

// IsSet reports whether the secret has a value.
func (s Secret) IsSet() bool { return s != "" }

// String returns the redaction marker, or "" when the secret is empty so that
// "not configured" stays visible in logs.
func (s Secret) String() string {
	if s == "" {
		return ""
	}
	return redacted
}

// GoString covers %#v, which otherwise prints the raw string (07 A13).
func (s Secret) GoString() string {
	return "config.Secret(" + strconv.Quote(s.String()) + ")"
}

// Format applies every verb to the redacted form. Without it a bad verb such
// as %d makes fmt print the raw value inside its %!d(...) error text.
func (s Secret) Format(f fmt.State, verb rune) {
	if verb == 'v' && f.Flag('#') {
		_, _ = io.WriteString(f, s.GoString())
		return
	}
	_, _ = fmt.Fprintf(f, fmt.FormatString(f, verb), s.String())
}

// MarshalJSON emits the redacted form.
func (s Secret) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.String())
}

// MarshalText emits the redacted form, which also covers YAML encoders and
// slog's text handler.
func (s Secret) MarshalText() ([]byte, error) {
	return []byte(s.String()), nil
}

// LogValue keeps slog from reaching the raw value through reflection.
func (s Secret) LogValue() slog.Value {
	return slog.StringValue(s.String())
}
