package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/paralin/go-steam"
	"github.com/paralin/go-steam/protocol/protobuf/unified"
)

// steamTOTP returns the 5-character Steam Guard code for sharedSecret at t.
func steamTOTP(sharedSecret string, t time.Time) (string, error) {
	key, err := base64.StdEncoding.DecodeString(sharedSecret)
	if err != nil {
		return "", fmt.Errorf("decode shared_secret: %w", err)
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(t.Unix()/30))
	m := hmac.New(sha1.New, key)
	m.Write(buf[:])
	h := m.Sum(nil)
	off := h[19] & 0x0f
	code := binary.BigEndian.Uint32(h[off:off+4]) & 0x7fffffff
	const chars = "23456789BCDFGHJKMNPQRTVWXY"
	out := make([]byte, 5)
	for i := range out {
		out[i] = chars[code%uint32(len(chars))]
		code /= uint32(len(chars))
	}
	return string(out), nil
}

// tokenPath is where a bot's refresh token is stored inside the state dir.
func tokenPath(stateDir string) string { return filepath.Join(stateDir, "refresh_token") }

func loadToken(stateDir string) (string, error) {
	b, err := os.ReadFile(tokenPath(stateDir))
	if err != nil {
		return "", fmt.Errorf("read token (run `probe auth` first): %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

func saveToken(stateDir, token string) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	tmp := tokenPath(stateDir) + ".tmp"
	if err := os.WriteFile(tmp, []byte(token+"\n"), 0o600); err != nil {
		return fmt.Errorf("write token: %w", err)
	}
	return os.Rename(tmp, tokenPath(stateDir))
}

// jwtClaims decodes the unverified payload of a Steam JWT. Only used to
// record aud/exp for the report; the token itself is opaque to us.
func jwtClaims(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("not a JWT")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode JWT payload: %w", err)
	}
	claims := map[string]any{}
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil, fmt.Errorf("parse JWT payload: %w", err)
	}
	return claims, nil
}

// claimsSummary keeps only the claims worth reporting (never the token).
func claimsSummary(token string) map[string]any {
	c, err := jwtClaims(token)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	out := map[string]any{}
	for _, k := range []string{"iss", "sub", "aud", "exp", "nbf", "iat"} {
		if v, ok := c[k]; ok {
			out[k] = v
		}
	}
	if exp, ok := c["exp"].(float64); ok {
		out["exp_time"] = time.Unix(int64(exp), 0).UTC().Format(time.RFC3339)
	}
	return out
}

// cmdAuth runs the credential + Steam Guard flow once and stores the refresh
// token (Q1, first half). It opens no CM connection.
func cmdAuth(ctx context.Context, c *config, rep *reporter) error {
	if c.user == "" || c.pass == "" {
		return errors.New("STEAM_USER and STEAM_PASS are required for auth")
	}
	code := c.guardCode
	if code == "" && c.sharedSecret != "" {
		var err error
		if code, err = steamTOTP(c.sharedSecret, time.Now()); err != nil {
			return err
		}
	}

	sc := steam.NewClient()
	start := time.Now()
	token, err := sc.Auth.Authenticate(ctx, &steam.LogOnDetails{
		Username:               c.user,
		Password:               c.pass,
		TwoFactorCode:          code,
		DeviceFriendlyName:     "dota_lobby probe",
		ShouldRememberPassword: true,
		ConfirmSteamGuard: func(context.Context, []steam.SteamGuardConfirmation) (string, unified.EAuthSessionGuardType, error) {
			return "", 0, errors.New("headless: interactive Steam Guard not supported; set STEAM_SHARED_SECRET or --guard-code")
		},
	})
	if err != nil {
		var ae *steam.AuthSessionError
		if errors.As(err, &ae) {
			rep.emit("Q1", "authenticate_failed", map[string]any{"state": fmt.Sprint(ae.State), "error": err.Error()})
		} else {
			rep.emit("Q1", "authenticate_failed", map[string]any{"error": err.Error()})
		}
		return fmt.Errorf("authenticate: %w", err)
	}
	if err := saveToken(c.stateDir, token); err != nil {
		return err
	}
	rep.emit("Q1", "authenticate_ok", map[string]any{
		"elapsed_ms": time.Since(start).Milliseconds(),
		"used_totp":  c.sharedSecret != "" && c.guardCode == "",
		"claims":     claimsSummary(token),
		"token_file": tokenPath(c.stateDir),
	})
	return nil
}
