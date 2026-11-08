package auth

import (
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name       string
		eresult    int32
		token      bool
		action     Action
		cooldown   time.Duration
		maxRetries int
		reason     string
	}{
		{"ok", 1, true, ActionNone, 0, 0, ReasonOK},
		{"fail", 2, true, ActionRetryBackoff, 0, 0, ReasonTransient},
		{"service unavailable", 20, false, ActionRetryBackoff, 0, 0, ReasonTransient},
		{"try another cm", 48, true, ActionRetryBackoff, 0, 0, ReasonTransient},
		{"unknown code", 9999, true, ActionRetryBackoff, 0, 0, ReasonTransient},

		{"invalid password on token", 5, true, ActionDropTokenAndReauthOnce, 0, 0, ReasonDeadToken},
		{"access denied on token", 15, true, ActionDropTokenAndReauthOnce, 0, 0, ReasonDeadToken},
		{"revoked on token", 26, true, ActionDropTokenAndReauthOnce, 0, 0, ReasonDeadToken},
		{"expired on token", 27, true, ActionDropTokenAndReauthOnce, 0, 0, ReasonDeadToken},
		{"invalid signature on token", 121, true, ActionDropTokenAndReauthOnce, 0, 0, ReasonDeadToken},

		{"invalid password on credentials", 5, false, ActionNeedsAttention, 0, 0, ReasonBadCredential},
		{"access denied on credentials", 15, false, ActionNeedsAttention, 0, 0, ReasonBadCredential},

		{"rate limit", 84, true, ActionCoolDown, 30 * time.Minute, 0, ReasonRateLimited},
		{"throttle", 87, false, ActionCoolDown, 30 * time.Minute, 0, ReasonRateLimited},
		{"logged in elsewhere", 6, true, ActionCoolDown, 10 * time.Minute, 0, ReasonElsewhere},
		{"session replaced", 34, true, ActionCoolDown, 10 * time.Minute, 0, ReasonElsewhere},

		{"totp mismatch", 88, false, ActionRetryThenAlert, 0, 2, ReasonTOTPMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Classify(tt.eresult, tt.token)
			if p.Action != tt.action || p.Cooldown != tt.cooldown || p.MaxRetries != tt.maxRetries || p.Reason != tt.reason {
				t.Errorf("Classify(%d, %v) = %+v, want %v/%v/%d/%s", tt.eresult, tt.token, p, tt.action, tt.cooldown, tt.maxRetries, tt.reason)
			}
			if p.Result != EResult(tt.eresult) {
				t.Errorf("Result = %v", p.Result)
			}
		})
	}
}

func TestClassifierCooldowns(t *testing.T) {
	c := Classifier{CooldownRateLimited: time.Hour, CooldownElsewhere: 2 * time.Minute}
	tests := []struct {
		eresult int32
		want    time.Duration
	}{
		{84, time.Hour},
		{87, time.Hour},
		{6, 2 * time.Minute},
		{34, 2 * time.Minute},
	}
	for _, tt := range tests {
		if got := c.Classify(tt.eresult, true).Cooldown; got != tt.want {
			t.Errorf("Classify(%d).Cooldown = %v, want %v", tt.eresult, got, tt.want)
		}
	}
}

func TestGuardPrompt(t *testing.T) {
	if p := GuardPrompt(false); p.Action != ActionNeedsAttention || p.Reason != ReasonGuardRequired {
		t.Errorf("GuardPrompt(false) = %+v", p)
	}
	if p := GuardPrompt(true); p.Action != ActionRetryThenAlert || p.MaxRetries != 2 {
		t.Errorf("GuardPrompt(true) = %+v", p)
	}
}

func TestEResultString(t *testing.T) {
	tests := []struct {
		e    EResult
		want string
	}{
		{EResultInvalidSignature, "InvalidSignature"},
		{EResultAccountLoginDeniedThrottle, "AccountLoginDeniedThrottle"},
		{EResult(4242), "EResult(4242)"},
	}
	for _, tt := range tests {
		if got := tt.e.String(); got != tt.want {
			t.Errorf("%d.String() = %q, want %q", int32(tt.e), got, tt.want)
		}
	}
	if ActionDropTokenAndReauthOnce.String() != "drop_token_and_reauth_once" || Action(99).String() != "Action(99)" {
		t.Error("Action.String mismatch")
	}
}
