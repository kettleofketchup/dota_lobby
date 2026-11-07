package auth

import (
	"crypto/hmac"
	"crypto/sha1" // Steam Guard TOTP is defined over HMAC-SHA1.
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/config"
)

// steamAlphabet is Steam's 26-character code alphabet (no vowels, no 0/1/A/E...).
const steamAlphabet = "23456789BCDFGHJKMNPQRTVWXY"

// totpPeriod is the length of one Steam Guard code window.
const totpPeriod = 30 * time.Second

// ErrNoSharedSecret is returned when a code is asked for without a secret.
var ErrNoSharedSecret = errors.New("auth: no shared_secret configured")

// Code returns the 5-character Steam Guard mobile code for sharedSecret (the
// base64 shared_secret from the authenticator's maFile) at time t. The
// algorithm is RFC 6238 with HMAC-SHA1 and a 30 s step, with Steam's own
// output encoding (research 04 §4.2).
func Code(sharedSecret config.Secret, t time.Time) (string, error) {
	key, err := decodeSharedSecret(sharedSecret)
	if err != nil {
		return "", err
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(t.Unix()/int64(totpPeriod/time.Second)))
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	out := make([]byte, 5)
	for i := range out {
		out[i] = steamAlphabet[v%uint32(len(steamAlphabet))]
		v /= uint32(len(steamAlphabet))
	}
	return string(out), nil
}

func decodeSharedSecret(s config.Secret) ([]byte, error) {
	raw := strings.TrimSpace(s.Reveal())
	if raw == "" {
		return nil, ErrNoSharedSecret
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		// Some tools strip the padding.
		if k, rerr := base64.RawStdEncoding.DecodeString(raw); rerr == nil {
			return k, nil
		}
		// The decode error does not echo the input, so it is safe to wrap.
		return nil, fmt.Errorf("auth: decode shared_secret: %w", err)
	}
	return key, nil
}

// TOTP generates codes for one bot. Skew is Steam server time minus local
// time (from ITwoFactorService/QueryTime, or configured); a host clock that
// drifts past a window gets TwoFactorCodeMismatch (88).
type TOTP struct {
	SharedSecret config.Secret
	Skew         time.Duration
	// Now defaults to time.Now; tests inject a fixed clock.
	Now func() time.Time
}

func (g TOTP) now() time.Time {
	if g.Now != nil {
		return g.Now().Add(g.Skew)
	}
	return time.Now().Add(g.Skew)
}

// Code returns the code for the current window.
func (g TOTP) Code() (string, error) {
	return Code(g.SharedSecret, g.now())
}

// UntilNextWindow is how long until the code changes. After an 88 the next
// attempt must wait for a new window, because Steam rejects a reused code.
func (g TOTP) UntilNextWindow() time.Duration {
	t := g.now()
	elapsed := time.Duration(t.UnixNano() % int64(totpPeriod))
	return totpPeriod - elapsed
}
