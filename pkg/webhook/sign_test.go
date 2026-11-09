package webhook

import (
	"errors"
	"testing"
	"time"
)

// The vector was computed independently with Python's hmac module:
// hmac.new(b"whsec_test", b'1762624800.{"id":"evt_1"}', sha256).hexdigest().
const (
	vectorSecret = "whsec_test"
	vectorTS     = 1762624800
	vectorBody   = `{"id":"evt_1"}`
	vectorSig    = "v1=898d18dde6b7357e818e3ae907e65e07b079679e0f30cb710a2f307c7e7ee5a0"
)

func TestSignGoldenVector(t *testing.T) {
	if got := Sign([]byte(vectorSecret), vectorTS, []byte(vectorBody)); got != vectorSig {
		t.Errorf("Sign = %s, want %s", got, vectorSig)
	}
}

func TestVerify(t *testing.T) {
	at := time.Unix(vectorTS, 0)
	tests := []struct {
		name      string
		secret    string
		ts        string
		sig       string
		body      string
		now       time.Time
		tolerance time.Duration
		want      error
	}{
		{"valid", vectorSecret, "1762624800", vectorSig, vectorBody, at, 0, nil},
		{"valid among several", vectorSecret, "1762624800", "v1=00, " + vectorSig, vectorBody, at, 0, nil},
		{"within default skew", vectorSecret, "1762624800", vectorSig, vectorBody, at.Add(5 * time.Minute), 0, nil},
		{"past default skew", vectorSecret, "1762624800", vectorSig, vectorBody, at.Add(5*time.Minute + time.Second), 0, ErrBadTimestamp},
		{"from the future", vectorSecret, "1762624800", vectorSig, vectorBody, at.Add(-6 * time.Minute), 0, ErrBadTimestamp},
		{"custom tolerance", vectorSecret, "1762624800", vectorSig, vectorBody, at.Add(time.Minute), 30 * time.Second, ErrBadTimestamp},
		{"timestamp not a number", vectorSecret, "soon", vectorSig, vectorBody, at, 0, ErrBadTimestamp},
		{"body changed", vectorSecret, "1762624800", vectorSig, `{"id":"evt_2"}`, at, 0, ErrBadSignature},
		{"wrong secret", "other", "1762624800", vectorSig, vectorBody, at, 0, ErrBadSignature},
		{"timestamp changed", vectorSecret, "1762624801", vectorSig, vectorBody, at, 0, ErrBadSignature},
		{"no version prefix", vectorSecret, "1762624800", vectorSig[3:], vectorBody, at, 0, ErrBadSignature},
		{"empty", vectorSecret, "1762624800", "", vectorBody, at, 0, ErrBadSignature},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Verify([]byte(tc.secret), tc.ts, tc.sig, []byte(tc.body), tc.now, tc.tolerance)
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Errorf("Verify = %v, want %v", err, tc.want)
			}
		})
	}
}
