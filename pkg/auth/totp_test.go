package auth

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/config"
)

// Known-answer vector from steamguard-cli (dyc3/steamguard-cli,
// steamguard/src/token.rs, test_generate_code): shared secret
// "zvIayp3JPvtvX/QGHqsqKBk/44s=" at unix time 1616374841 gives "2F9J5".
const (
	knownSecret = config.Secret("zvIayp3JPvtvX/QGHqsqKBk/44s=")
	knownTime   = 1616374841
	knownCode   = "2F9J5"
)

func TestCodeKnownAnswer(t *testing.T) {
	tests := []struct {
		name   string
		secret config.Secret
		at     int64
	}{
		{"published vector", knownSecret, knownTime},
		{"start of the same window", knownSecret, knownTime - knownTime%30},
		{"end of the same window", knownSecret, knownTime - knownTime%30 + 29},
		{"surrounding whitespace", "  " + knownSecret + "\n", knownTime},
		{"unpadded base64", config.Secret(strings.TrimRight(string(knownSecret), "=")), knownTime},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Code(tt.secret, time.Unix(tt.at, 0))
			if err != nil {
				t.Fatal(err)
			}
			if got != knownCode {
				t.Errorf("Code = %q, want %q", got, knownCode)
			}
		})
	}
}

func TestCodeProperties(t *testing.T) {
	secret := config.Secret(base64.StdEncoding.EncodeToString([]byte("12345678901234567890")))
	base := time.Unix(1_699_999_990, 0) // 10 s into a window
	a, err := Code(secret, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 5 || strings.Trim(a, steamAlphabet) != "" {
		t.Fatalf("code %q is not 5 Steam alphabet characters", a)
	}
	if b, _ := Code(secret, base.Add(30*time.Second)); a == b {
		t.Errorf("next window gave the same code %q", a)
	}
}

func TestCodeErrors(t *testing.T) {
	tests := []struct {
		name   string
		secret config.Secret
		is     error
	}{
		{"empty", "", ErrNoSharedSecret},
		{"blank", "   ", ErrNoSharedSecret},
		{"not base64", "not base64!", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Code(tt.secret, time.Unix(knownTime, 0))
			if err == nil {
				t.Fatal("expected an error")
			}
			if tt.is != nil && !errors.Is(err, tt.is) {
				t.Errorf("err = %v, want %v", err, tt.is)
			}
			if s := strings.TrimSpace(tt.secret.Reveal()); s != "" && strings.Contains(err.Error(), s) {
				t.Errorf("error %q echoes the secret", err)
			}
		})
	}
}

func TestTOTPSkew(t *testing.T) {
	fixed := func() time.Time { return time.Unix(knownTime-90, 0) }
	tests := []struct {
		name string
		skew time.Duration
		want bool // matches the known code
	}{
		{"no skew is a different window", 0, false},
		{"server ahead by 90s", 90 * time.Second, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := TOTP{SharedSecret: knownSecret, Skew: tt.skew, Now: fixed}
			got, err := g.Code()
			if err != nil {
				t.Fatal(err)
			}
			if (got == knownCode) != tt.want {
				t.Errorf("Code = %q, match want %v", got, tt.want)
			}
		})
	}
}

func TestTOTPUntilNextWindow(t *testing.T) {
	tests := []struct {
		at   int64
		skew time.Duration
		want time.Duration
	}{
		{1_699_999_990, 0, 20 * time.Second},
		{1_699_999_980, 0, 30 * time.Second}, // window boundary
		{1_699_999_990, 15 * time.Second, 5 * time.Second},
	}
	for _, tt := range tests {
		g := TOTP{Skew: tt.skew, Now: func() time.Time { return time.Unix(tt.at, 0) }}
		if got := g.UntilNextWindow(); got != tt.want {
			t.Errorf("at %d skew %v: UntilNextWindow = %v, want %v", tt.at, tt.skew, got, tt.want)
		}
	}
}
