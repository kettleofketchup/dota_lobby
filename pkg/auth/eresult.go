package auth

import (
	"strconv"
	"time"
)

// EResult is a Steam EResult code. The values match SteamKit's eresult.steamd;
// go-steam master lacks InvalidSignature (121), which is why they live here.
type EResult int32

// EResults the classifier knows about (research 04 §5.1).
const (
	EResultOK                         EResult = 1
	EResultFail                       EResult = 2
	EResultInvalidPassword            EResult = 5
	EResultLoggedInElsewhere          EResult = 6
	EResultAccessDenied               EResult = 15
	EResultServiceUnavailable         EResult = 20
	EResultRevoked                    EResult = 26
	EResultExpired                    EResult = 27
	EResultLogonSessionReplaced       EResult = 34
	EResultTryAnotherCM               EResult = 48
	EResultAlreadyLoggedInElsewhere   EResult = 50
	EResultRateLimitExceeded          EResult = 84
	EResultAccountLoginDeniedThrottle EResult = 87
	EResultTwoFactorCodeMismatch      EResult = 88
	EResultInvalidSignature           EResult = 121
)

var eresultNames = map[EResult]string{
	EResultOK:                         "OK",
	EResultFail:                       "Fail",
	EResultInvalidPassword:            "InvalidPassword",
	EResultLoggedInElsewhere:          "LoggedInElsewhere",
	EResultAccessDenied:               "AccessDenied",
	EResultServiceUnavailable:         "ServiceUnavailable",
	EResultRevoked:                    "Revoked",
	EResultExpired:                    "Expired",
	EResultLogonSessionReplaced:       "LogonSessionReplaced",
	EResultTryAnotherCM:               "TryAnotherCM",
	EResultAlreadyLoggedInElsewhere:   "AlreadyLoggedInElsewhere",
	EResultRateLimitExceeded:          "RateLimitExceeded",
	EResultAccountLoginDeniedThrottle: "AccountLoginDeniedThrottle",
	EResultTwoFactorCodeMismatch:      "TwoFactorCodeMismatch",
	EResultInvalidSignature:           "InvalidSignature",
}

// String returns the SteamKit name, or "EResult(n)" for codes not listed here.
func (e EResult) String() string {
	if n, ok := eresultNames[e]; ok {
		return n
	}
	return "EResult(" + strconv.Itoa(int(e)) + ")"
}

// Action is what the supervisor should do after a failed logon.
type Action int

// Actions, one per row of the EResult policy table.
const (
	// ActionNone means the logon succeeded.
	ActionNone Action = iota
	// ActionRetryBackoff reconnects with jittered exponential backoff.
	ActionRetryBackoff
	// ActionDropTokenAndReauthOnce deletes the stored token and runs one
	// credential Authenticate. A second failure becomes NeedsAttention.
	ActionDropTokenAndReauthOnce
	// ActionNeedsAttention stops retrying until an operator steps in;
	// retrying a bad password is what gets an IP throttled.
	ActionNeedsAttention
	// ActionCoolDown waits Policy.Cooldown before the next logon.
	ActionCoolDown
	// ActionRetryThenAlert retries Policy.MaxRetries times, each in a fresh
	// TOTP window, then alerts.
	ActionRetryThenAlert
)

var actionNames = [...]string{
	ActionNone:                   "none",
	ActionRetryBackoff:           "retry_backoff",
	ActionDropTokenAndReauthOnce: "drop_token_and_reauth_once",
	ActionNeedsAttention:         "needs_attention",
	ActionCoolDown:               "cool_down",
	ActionRetryThenAlert:         "retry_then_alert",
}

func (a Action) String() string {
	if a >= 0 && int(a) < len(actionNames) {
		return actionNames[a]
	}
	return "Action(" + strconv.Itoa(int(a)) + ")"
}

// Reasons attached to a Policy, stable enough for metrics labels and the
// /bots API.
const (
	ReasonOK            = "ok"
	ReasonTransient     = "transient"
	ReasonDeadToken     = "dead_token"
	ReasonBadCredential = "bad_credentials"
	ReasonGuardRequired = "guard_required"
	ReasonRateLimited   = "rate_limited"
	ReasonElsewhere     = "logged_in_elsewhere"
	ReasonTOTPMismatch  = "totp_mismatch"
)

// Default cooldowns, matching config.SteamConfig's defaults.
const (
	DefaultCooldownRateLimited = 30 * time.Minute
	DefaultCooldownElsewhere   = 10 * time.Minute
)

// totpMaxRetries is the "2x" in Retry2xThenAlert.
const totpMaxRetries = 2

// Policy is the classifier's verdict for one failed attempt.
type Policy struct {
	Action Action
	// Cooldown is set for ActionCoolDown.
	Cooldown time.Duration
	// MaxRetries is set for ActionRetryThenAlert.
	MaxRetries int
	Reason     string
	Result     EResult
}

// Classifier maps EResults to policies with configurable cooldowns
// (steam.cooldown_rate_limited, steam.cooldown_elsewhere). A zero duration
// falls back to the default.
type Classifier struct {
	CooldownRateLimited time.Duration
	CooldownElsewhere   time.Duration
}

// Classify uses the default cooldowns.
func Classify(eresult int32, wasTokenLogon bool) Policy {
	return Classifier{}.Classify(eresult, wasTokenLogon)
}

// Classify implements the EResult policy table. wasTokenLogon separates a dead
// token (re-auth once) from a bad password (stop and ask a human): Steam
// reports both as InvalidPassword.
func (c Classifier) Classify(eresult int32, wasTokenLogon bool) Policy {
	r := EResult(eresult)
	p := Policy{Result: r}
	switch r {
	case EResultOK:
		p.Action, p.Reason = ActionNone, ReasonOK
	case EResultInvalidPassword, EResultAccessDenied, EResultRevoked, EResultExpired, EResultInvalidSignature:
		if wasTokenLogon {
			p.Action, p.Reason = ActionDropTokenAndReauthOnce, ReasonDeadToken
		} else {
			p.Action, p.Reason = ActionNeedsAttention, ReasonBadCredential
		}
	case EResultRateLimitExceeded, EResultAccountLoginDeniedThrottle:
		p.Action, p.Reason = ActionCoolDown, ReasonRateLimited
		p.Cooldown = orDefault(c.CooldownRateLimited, DefaultCooldownRateLimited)
	case EResultLoggedInElsewhere, EResultLogonSessionReplaced:
		p.Action, p.Reason = ActionCoolDown, ReasonElsewhere
		p.Cooldown = orDefault(c.CooldownElsewhere, DefaultCooldownElsewhere)
	case EResultTwoFactorCodeMismatch:
		p.Action, p.Reason = ActionRetryThenAlert, ReasonTOTPMismatch
		p.MaxRetries = totpMaxRetries
	default:
		// Fail, ServiceUnavailable, TryAnotherCM and anything unknown are
		// treated as Steam-side trouble.
		p.Action, p.Reason = ActionRetryBackoff, ReasonTransient
	}
	return p
}

// GuardPrompt classifies an auth session that asked for a Steam Guard code.
// With a shared_secret the TOTP answers it; without one only an operator can.
func GuardPrompt(hasSharedSecret bool) Policy {
	if hasSharedSecret {
		return Policy{Action: ActionRetryThenAlert, MaxRetries: totpMaxRetries, Reason: ReasonTOTPMismatch}
	}
	return Policy{Action: ActionNeedsAttention, Reason: ReasonGuardRequired}
}

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}
