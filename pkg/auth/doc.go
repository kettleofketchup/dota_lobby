// Package auth holds the Steam authentication pieces that do not need a CM
// connection: the per-bot refresh token store, Steam Guard TOTP codes, the
// EResult classifier that drives the supervisor's retry policy, and the
// process lock on the state directory.
//
// It deliberately does not import go-steam: EResults arrive as int32 and are
// named here, so the policy can be tested without the Steam stack.
package auth
