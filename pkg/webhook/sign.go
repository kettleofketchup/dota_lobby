// Package webhook delivers lobby events to the configured receivers:
// signed POSTs, in order per lobby, retried with backoff from a queue in
// the database so nothing is lost across a restart.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Request headers.
const (
	HeaderEventID   = "X-Dota-Lobby-Event-Id"
	HeaderEventType = "X-Dota-Lobby-Event-Type"
	HeaderTimestamp = "X-Dota-Lobby-Timestamp"
	HeaderSignature = "X-Dota-Lobby-Signature"
	// HeaderSimulation is "true" on every delivery of a service in
	// simulation mode, so a receiver can refuse simulated events. It is
	// not covered by the signature.
	HeaderSimulation = "X-Dota-Lobby-Simulation"
)

// DefaultTolerance is the clock skew receivers should allow between the
// timestamp header and their own clock.
const DefaultTolerance = 5 * time.Minute

// signatureVersion prefixes every signature, so the scheme can change
// without breaking receivers that check the prefix.
const signatureVersion = "v1="

// Verification errors.
var (
	ErrBadSignature = errors.New("webhook: signature does not match")
	ErrBadTimestamp = errors.New("webhook: timestamp missing or outside the tolerance")
)

// Sign returns the signature header value for body sent at ts (Unix
// seconds): "v1=" and the hex HMAC-SHA256 of "<ts>.<body>" under secret.
func Sign(secret []byte, ts int64, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte{'.'})
	mac.Write(body)
	return signatureVersion + hex.EncodeToString(mac.Sum(nil))
}

// Verify checks a delivery as a receiver would: the timestamp header must
// be within tolerance of now (0 means DefaultTolerance), and one of the
// comma-separated v1 signatures must match, compared in constant time.
func Verify(secret []byte, timestamp, signature string, body []byte, now time.Time, tolerance time.Duration) error {
	if tolerance <= 0 {
		tolerance = DefaultTolerance
	}
	ts, err := strconv.ParseInt(strings.TrimSpace(timestamp), 10, 64)
	if err != nil {
		return fmt.Errorf("%w: %q is not Unix seconds", ErrBadTimestamp, timestamp)
	}
	if skew := now.Sub(time.Unix(ts, 0)); skew > tolerance || skew < -tolerance {
		return fmt.Errorf("%w: off by %s", ErrBadTimestamp, skew.Round(time.Second))
	}
	want := []byte(Sign(secret, ts, body))
	for _, got := range strings.Split(signature, ",") {
		if hmac.Equal([]byte(strings.TrimSpace(got)), want) {
			return nil
		}
	}
	return ErrBadSignature
}
