package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/config"
)

// Token sources recorded in Token.Source.
const (
	SourceSeed         = "seed"         // copied from the configured STEAM_REFRESH_TOKEN
	SourceAuthenticate = "authenticate" // minted by a credential Authenticate
	SourceRenew        = "renew"        // returned by GenerateAccessTokenForApp
)

// Token is the persisted form of a bot's Steam refresh token.
//
// RefreshToken is a plain string on purpose: config.Secret's MarshalJSON
// redacts, so a Secret here would write the marker to disk (07 A14). The
// fmt and slog methods below redact it instead, so a Token is safe to log.
type Token struct {
	RefreshToken string    `json:"refresh_token"`
	SteamID      uint64    `json:"steam_id,string,omitempty"`
	ObtainedAt   time.Time `json:"obtained_at"`
	IssuedAt     time.Time `json:"issued_at,omitzero"`
	ExpiresAt    time.Time `json:"expires_at,omitzero"`
	Source       string    `json:"source,omitempty"`
}

// ErrNotJWT is returned for a refresh token that is not a three-part JWT.
var ErrNotJWT = errors.New("auth: refresh token is not a JWT")

// NewToken builds a Token from a raw refresh token, decoding steam_id, iat
// and exp from the JWT payload. The signature is not checked: the token is
// opaque to us and Steam is the only party that can verify it.
func NewToken(raw string, obtainedAt time.Time, source string) (Token, error) {
	raw = strings.TrimSpace(raw)
	c, err := decodeClaims(raw)
	if err != nil {
		return Token{}, err
	}
	t := Token{RefreshToken: raw, ObtainedAt: obtainedAt.UTC(), Source: source}
	if c.Sub != "" {
		id, err := strconv.ParseUint(c.Sub, 10, 64)
		if err != nil {
			return Token{}, fmt.Errorf("auth: JWT sub is not a SteamID: %w", err)
		}
		t.SteamID = id
	}
	if c.Iat > 0 {
		t.IssuedAt = time.Unix(int64(c.Iat), 0).UTC()
	}
	if c.Exp > 0 {
		t.ExpiresAt = time.Unix(int64(c.Exp), 0).UTC()
	}
	return t, nil
}

// FromMinted builds the Token for a refresh token that Steam just minted
// through Authenticate. Steam's token is used as-is at logon, so one whose
// claims cannot be decoded is still kept, with the expiry unknown.
func FromMinted(raw string, now time.Time) Token {
	tok, err := NewToken(raw, now, SourceAuthenticate)
	if err != nil {
		return opaqueToken(raw, now, SourceAuthenticate)
	}
	return tok
}

// opaqueToken wraps a refresh token whose claims are not read: the SteamID
// and expiry stay unknown and Steam is left to judge it at logon.
func opaqueToken(raw string, now time.Time, source string) Token {
	return Token{RefreshToken: strings.TrimSpace(raw), ObtainedAt: now.UTC(), Source: source}
}

type jwtClaims struct {
	Sub string  `json:"sub"`
	Iat float64 `json:"iat"`
	Exp float64 `json:"exp"`
}

func decodeClaims(raw string) (jwtClaims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || parts[1] == "" {
		return jwtClaims{}, ErrNotJWT
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return jwtClaims{}, fmt.Errorf("auth: decode JWT payload: %w", err)
	}
	var c jwtClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return jwtClaims{}, fmt.Errorf("auth: parse JWT payload: %w", err)
	}
	return c, nil
}

// Secret returns the token wrapped for passing to the gc layer.
func (t Token) Secret() config.Secret { return config.Secret(t.RefreshToken) }

// IsSet reports whether the token holds a value.
func (t Token) IsSet() bool { return t.RefreshToken != "" }

// Expired reports whether exp has passed. A token with no exp never expires
// by this test; Steam will say so at logon.
func (t Token) Expired(now time.Time) bool {
	return !t.ExpiresAt.IsZero() && !now.Before(t.ExpiresAt)
}

// ExpiresWithin reports whether exp is less than d away. The supervisor
// re-authenticates once the token is inside its RenewBefore window (14 days
// by default).
func (t Token) ExpiresWithin(now time.Time, d time.Duration) bool {
	return !t.ExpiresAt.IsZero() && t.ExpiresAt.Sub(now) < d
}

func (t Token) summary() string {
	var b strings.Builder
	b.WriteString("auth.Token{")
	fmt.Fprintf(&b, "SteamID:%d Source:%q", t.SteamID, t.Source)
	if !t.ExpiresAt.IsZero() {
		fmt.Fprintf(&b, " ExpiresAt:%s", t.ExpiresAt.Format(time.RFC3339))
	}
	fmt.Fprintf(&b, " RefreshToken:%s}", config.Secret(t.RefreshToken))
	return b.String()
}

// String redacts the refresh token.
func (t Token) String() string { return t.summary() }

// GoString redacts the refresh token for %#v.
func (t Token) GoString() string { return t.summary() }

// Format applies every verb to the redacted summary.
func (t Token) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, t.summary()) }

// LogValue keeps slog from reflecting into the struct.
func (t Token) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Uint64("steam_id", t.SteamID),
		slog.String("source", t.Source),
		slog.Time("expires_at", t.ExpiresAt),
		slog.Any("refresh_token", config.Secret(t.RefreshToken)),
	)
}
