# Phase 0 live probe (tools/probe), run with a bot's sops-decrypted credentials.
# See tools/probe/README.md.

set quiet
set working-directory := ".."


BOTS_DIR := "secrets/bots"

# Build bin/probe
[group('probe')]
build:
    go -C tools/probe build -trimpath -o ../../bin/probe .

# Run the probe's offline tests
[group('probe')]
test:
    go -C tools/probe test ./...

# Password + Steam Guard login; the refresh token goes straight into the bot's sops file (Q1)
[group('probe')]
auth bot: build
    #!/usr/bin/env bash
    set -euo pipefail
    file={{BOTS_DIR}}/{{bot}}.sops.yaml
    token=$(sops exec-env "$file" 'bin/probe auth -state probe-state/{{bot}} -token-out -')
    [[ -n "$token" ]] || { echo "auth produced no token"; exit 1; }
    printf '"%s"' "$token" | sops set --value-stdin "$file" '["STEAM_REFRESH_TOKEN"]'
    echo "stored STEAM_REFRESH_TOKEN in $file"

# Token login, then CM renewal; stores the new token if Steam issued one (Q12)
[group('probe')]
renew bot: build
    #!/usr/bin/env bash
    set -euo pipefail
    file={{BOTS_DIR}}/{{bot}}.sops.yaml
    token=$(sops exec-env "$file" 'bin/probe login -renew -state probe-state/{{bot}} -token-out -')
    if [[ -n "$token" ]]; then
        printf '"%s"' "$token" | sops set --value-stdin "$file" '["STEAM_REFRESH_TOKEN"]'
        echo "stored renewed STEAM_REFRESH_TOKEN in $file"
    else
        echo "no new token issued"
    fi

# Print the bot's current Steam Guard code, e.g. to log in to its store page in a browser
[group('probe')]
code bot: build
    sops exec-env {{BOTS_DIR}}/{{bot}}.sops.yaml 'bin/probe code -state probe-state/{{bot}}'

# Run any probe command as a bot, e.g. `just probe run bot-a create -league 123 -hold`
[group('probe')]
[positional-arguments]
run bot cmd *args: build
    #!/usr/bin/env bash
    set -euo pipefail
    bot=$1 cmd=$2; shift 2
    sops exec-env {{BOTS_DIR}}/$bot.sops.yaml "bin/probe $cmd -state probe-state/$bot $(printf '%q ' "$@")"

# Show this bot's report lines for one research question, e.g. `just probe report bot-a Q18`
[group('probe')]
report bot q:
    jq -c 'select(.q=="{{q}}")' probe-state/{{bot}}/report.jsonl
