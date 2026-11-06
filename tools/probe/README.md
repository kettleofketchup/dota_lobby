# probe

Phase 0 live experiments against Steam and the Dota 2 GC. The plan, the accounts it needs and the run order are in
[docs/plans/2026-11-06-phase0-live-probe.md](../../docs/plans/2026-11-06-phase0-live-probe.md).

This is its own Go module on the upstream go-steam/go-dota2 masters (`go 1.26.0`), separate from the service.

## Build

```bash
cd tools/probe
go build -o ../../bin/probe .
go test ./...
```

## Credentials

Use the just recipes. They keep credentials in local sops files encrypted with a repo-local age key, all
git-ignored (see the plan's "Secrets" section):

```bash
just secrets init
just secrets new-bot bot-a        # fill in STEAM_PASS and STEAM_SHARED_SECRET
just probe auth bot-a             # token is stored in secrets/bots/bot-a.sops.yaml
just probe run bot-a leagues
```

| Env | Used by |
|---|---|
| `STEAM_USER` | every command |
| `STEAM_PASS` | `auth` only |
| `STEAM_SHARED_SECRET` | `auth`, to generate the Steam Guard code (or pass `-guard-code`) |
| `STEAM_REFRESH_TOKEN` | every command after `auth` |

Run by hand without `STEAM_REFRESH_TOKEN`, `auth` writes the token to `probe-state/<user>/refresh_token` (0600)
and later commands read it from there. `-token-out -` prints it to stdout instead, which is what the recipes pipe
into sops. Use dedicated bot accounts, and don't log the same account into a real client while the probe runs.

## Commands

```bash
probe auth                                   # Q1: password + TOTP -> refresh token (just probe auth <bot>)
probe login                                  # Q1: token login to GC welcome (run twice to test a restart)
probe login -renew                           # Q12: CM token renewal
probe leagues                                # Q2, Q18: 7633 via MakeRequest, raw with job, raw without
probe create -league 123                     # Q4, Q7: create and record 7055, popups, read-back
probe create -league 123 -no-job             # Q7: same, without a source job ID
probe create -league 123 -tv-wire 2          # Q6: expect read-back 2 (120 s)
probe create -tv-wire 1                      # Q19: 60 s without a league
probe create -league 123 -hold               # Q5, Q8: keep the lobby open and use the console
```

In the `-hold` console: `dump`, `invite <steamid64>`, `team <radiant|dire> <slot>`,
`seat <self_kick|player_pool|spectator|caster|unassigned>`, `tv <wire>`, `launch`, `leave`, `destroy`, `quit`.
Every lobby state change is reported while it runs.

## Output

Each run appends to `probe-state/<user>/report.jsonl`. Every line has `event`, and where it answers a research
question, `q` (for example `"q":"Q18"`). Every GC message in or out is a `gc_in`/`gc_out` line with its message ID,
name and job IDs, and its raw body is saved under `captures/` for golden-file tests. Use `-q` to hide that traffic
on stderr.

```bash
jq -c 'select(.q=="Q18")' probe-state/<user>/report.jsonl
```

`leagues` runs its three 7633 attempts one after another. A late reply to an earlier attempt can arrive during a
later one, so judge job echo by the `target_job` on each `gc_in` 7634 line, not only by which attempt reported it.

Captures of outgoing creates contain the lobby pass key, so use a throwaway `-pass`.
