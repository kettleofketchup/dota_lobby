package main

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestSteamTOTP(t *testing.T) {
	secret := base64.StdEncoding.EncodeToString([]byte("12345678901234567890"))
	at := time.Unix(1_700_000_000, 0)

	a, err := steamTOTP(secret, at)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 5 || strings.Trim(a, "23456789BCDFGHJKMNPQRTVWXY") != "" {
		t.Fatalf("code %q is not 5 Steam alphabet chars", a)
	}
	b, _ := steamTOTP(secret, at.Add(29*time.Second-time.Duration(at.Unix()%30)*time.Second))
	if a != b {
		t.Fatalf("same 30s window gave %q and %q", a, b)
	}
	c, _ := steamTOTP(secret, at.Add(30*time.Second))
	if a == c {
		t.Fatalf("next window gave the same code %q", a)
	}
	if _, err := steamTOTP("not base64!", at); err == nil {
		t.Fatal("expected an error for a bad secret")
	}
}

func TestClaimsSummaryOmitsToken(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"7656","aud":["client","web"],"exp":1800000000,"secret":"x"}`))
	token := "eyJhbGciOiJFZERTQSJ9." + payload + ".sig"

	got := claimsSummary(token)
	if got["sub"] != "7656" || got["exp_time"] != "2027-01-15T08:00:00Z" {
		t.Fatalf("unexpected claims %v", got)
	}
	if _, ok := got["secret"]; ok {
		t.Fatal("unlisted claim leaked into the summary")
	}
	if claimsSummary("garbage")["error"] == nil {
		t.Fatal("expected an error entry for a non-JWT")
	}
}

// Known-answer vector from steamguard-cli (steamguard/src/token.rs, test_generate_code).
func TestSteamTOTPKnownAnswer(t *testing.T) {
	got, err := steamTOTP("zvIayp3JPvtvX/QGHqsqKBk/44s=", time.Unix(1616374841, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got != "2F9J5" {
		t.Fatalf("got %q, want 2F9J5", got)
	}
}
