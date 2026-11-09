# Local sops + age encryption for bot credentials.
# The key lives in the main sops key file (~/.config/sops/age/keys.txt) under
# a "dota_lobby" label; .sops.yaml and every encrypted file are git-ignored.

set quiet
set working-directory := ".."

KEYS := env_var_or_default("SOPS_AGE_KEY_FILE", env_var_or_default("XDG_CONFIG_HOME", env_var("HOME") / ".config") / "sops" / "age" / "keys.txt")
LABEL := "# dota_lobby bot credentials key"
BOTS_DIR := "secrets/bots"
SERVICE_SECRETS := "secrets/service.sops.yaml"

# Shared by the webhook recipes: check the URL and work out the secrets-file
# key. The service looks webhook secrets and tokens up by the lowercased URL.
# jq escapes the key for a sops path, so any URL is safe.
WEBHOOK_KEY := '''
    [[ -f .sops.yaml ]] || { echo "run: just secrets init"; exit 1; }
    [[ "$url" =~ ^https?:// ]] || { echo "webhook URL must start with http:// or https://: $url"; exit 1; }
    [[ "$url" != *[\"\\[:space:]]* ]] || { echo "webhook URL must not contain quotes, backslashes or spaces (percent-encode them): $url"; exit 1; }
    key=${url,,}
    jkey=$(jq -rn --arg u "$key" '$u | tojson')
'''

# Add a labelled dota_lobby key to the main sops key file if missing, and write .sops.yaml
[group('secrets')]
init:
    #!/usr/bin/env bash
    set -euo pipefail
    keys="{{KEYS}}"
    if ! grep -qF "{{LABEL}}" "$keys" 2>/dev/null; then
        mkdir -p "$(dirname "$keys")"
        [[ -f "$keys" ]] && cp -p "$keys" "$keys.bak-$(date +%Y%m%d%H%M%S)"
        new=$(umask 077 && age-keygen 2>/dev/null)
        (umask 077 && printf '\n%s (generated %s)\n%s\n' "{{LABEL}}" "$(date +%F)" "$new" >> "$keys")
        echo "added a dota_lobby key to $keys (back that file up; losing it loses the secrets)"
    fi
    recipient=$(grep -A2 -F "{{LABEL}}" "$keys" | sed -n 's/^# public key: //p' | head -1)
    [[ -n "$recipient" ]] || { echo "no public key under the dota_lobby label in $keys"; exit 1; }
    printf 'creation_rules:\n  - path_regex: ^secrets/.*\\.sops\\.ya?ml$\n    age: %s\n' "$recipient" > .sops.yaml
    mkdir -p {{BOTS_DIR}} && chmod 700 secrets {{BOTS_DIR}}
    echo "wrote .sops.yaml for $recipient"

# Create an encrypted credentials file for a bot and open it in $EDITOR
[group('secrets')]
new-bot name:
    #!/usr/bin/env bash
    set -euo pipefail
    file={{BOTS_DIR}}/{{name}}.sops.yaml
    [[ -f .sops.yaml ]] || { echo "run: just secrets init"; exit 1; }
    [[ -e "$file" ]] && { echo "$file already exists; use: just secrets edit {{name}}"; exit 1; }
    printf 'STEAM_USER: "{{name}}"\nSTEAM_PASS: ""\nSTEAM_SHARED_SECRET: ""\n' \
        | sops encrypt --filename-override "$file" --input-type yaml --output-type yaml /dev/stdin > "$file"
    chmod 600 "$file"
    sops edit "$file" || [[ $? -eq 200 ]]  # 200 = saved without changes

# Edit a bot's encrypted credentials in $EDITOR
[group('secrets')]
edit name:
    sops edit {{BOTS_DIR}}/{{name}}.sops.yaml

# List bots and which keys are set, and the webhook URLs with a secret or token (values are never printed)
[group('secrets')]
list:
    #!/usr/bin/env bash
    set -euo pipefail
    shopt -s nullglob
    files=({{BOTS_DIR}}/*.sops.yaml)
    (( ${#files[@]} )) || echo "no bots; run: just secrets new-bot <name>"
    for f in "${files[@]}"; do
        bot=$(basename "$f" .sops.yaml)
        sops exec-env "$f" 'for k in STEAM_USER STEAM_PASS STEAM_SHARED_SECRET STEAM_REFRESH_TOKEN; do
            eval "v=\${$k:-}"; [ -n "$v" ] && printf "  %s set\n" "$k" || printf "  %s -\n" "$k"; done' \
            | sed "1i $bot:"
    done
    [[ -f "{{SERVICE_SECRETS}}" ]] || exit 0
    echo "{{SERVICE_SECRETS}}:"
    sops decrypt --output-type json "{{SERVICE_SECRETS}}" | jq -r '
        .secrets // {} as $s
        | "  api_key \(if ($s.api_key // "") != "" then "set" else "-" end)",
          (["webhook_secrets", "webhook_tokens"][] as $m
           | "  \($m):", ($s[$m] // {} | keys[] | "    \(.)"))'

# Store a value read from stdin (JSON-encoded string) under KEY in a bot's file
[group('secrets')]
set-stdin name key:
    sops set --value-stdin {{BOTS_DIR}}/{{name}}.sops.yaml '["{{key}}"]'

# Import a steamguard-cli maFile: shared_secret into the bot file, recovery fields into secrets/guard (never printed)
[group('secrets')]
import-mafile name mafile:
    #!/usr/bin/env bash
    set -euo pipefail
    bot={{BOTS_DIR}}/{{name}}.sops.yaml
    guard=secrets/guard/{{name}}.sops.yaml
    [[ -f .sops.yaml ]] || { echo "run: just secrets init"; exit 1; }
    [[ -f "$bot" ]] || { echo "run: just secrets new-bot {{name}}"; exit 1; }
    jq -e '[.shared_secret, .identity_secret, .revocation_code] | all(type == "string" and length > 0)' "{{mafile}}" >/dev/null \
        || { echo "{{mafile}}: missing secrets, or the maFile is encrypted (run: steamguard decrypt)"; exit 1; }
    acct=$(jq -r .account_name "{{mafile}}")
    user=$(sops decrypt --extract '["STEAM_USER"]' "$bot")
    [[ "${acct,,}" == "${user,,}" ]] || { echo "{{mafile}} is for $acct, not $user"; exit 1; }
    jq -c .shared_secret "{{mafile}}" | sops set --value-stdin "$bot" '["STEAM_SHARED_SECRET"]'
    if [[ ! -f "$guard" ]]; then
        mkdir -p secrets/guard && chmod 700 secrets/guard
        printf 'STEAM_ID: ""\n' | sops encrypt --filename-override "$guard" --input-type yaml --output-type yaml /dev/stdin > "$guard"
        chmod 600 "$guard"
    fi
    # The service never loads secrets/guard; these are only for recovery and support.
    for k in identity_secret revocation_code serial_number device_id; do
        jq -c --arg k "$k" '.[$k] // "" | tostring' "{{mafile}}" | sops set --value-stdin "$guard" "[\"STEAM_${k^^}\"]"
    done
    jq -c '(.steam_id // .Session.SteamID // "") | tostring' "{{mafile}}" | sops set --value-stdin "$guard" '["STEAM_ID"]'
    echo "imported into $bot (STEAM_SHARED_SECRET) and $guard; now run: steamguard encrypt (or delete the maFile)"

# Generate a webhook signing secret for URL into secrets/service.sops.yaml (never printed)
[group('secrets')]
webhook-secret url force="false":
    #!/usr/bin/env bash
    set -euo pipefail
    url={{quote(url)}}
    {{WEBHOOK_KEY}}
    f="{{SERVICE_SECRETS}}"
    p='["secrets"]["webhook_secrets"]['"$jkey"']'
    if [[ ! -f "$f" ]]; then
        mkdir -p "$(dirname "$f")" && chmod 700 "$(dirname "$f")"
        printf 'secrets: {}\n' | sops encrypt --filename-override "$f" --input-type yaml --output-type yaml /dev/stdin > "$f.tmp"
        chmod 600 "$f.tmp" && mv "$f.tmp" "$f"
    fi
    if sops decrypt --extract "$p" "$f" >/dev/null 2>&1 && [[ {{quote(force)}} != true ]]; then
        echo "secrets.webhook_secrets[$key] is already set; to replace it (the receiver must get the new one too): just secrets webhook-secret $url true"
        exit 1
    fi
    openssl rand -hex 32 | jq -Rc . | sops set --value-stdin "$f" "$p"
    echo "set secrets.webhook_secrets[$key]; add the same URL to webhooks[] in config/config.yaml, share the secret with the receiver (just secrets webhook-secret-env), then: just docker restart"

# Write URL's webhook secret into an env file as VAR=<secret>, for the receiver (never printed)
[group('secrets')]
webhook-secret-env url env_file var="DOTA_LOBBY_WEBHOOK_SECRET":
    #!/usr/bin/env bash
    set -euo pipefail
    url={{quote(url)}}
    {{WEBHOOK_KEY}}
    f="{{SERVICE_SECRETS}}"
    env_file={{quote(env_file)}}
    var={{quote(var)}}
    [[ "$var" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || { echo "not a variable name: $var"; exit 1; }
    p='["secrets"]["webhook_secrets"]['"$jkey"']'
    sops decrypt --extract "$p" "$f" >/dev/null 2>&1 \
        || { echo "no secrets.webhook_secrets[$key] in $f; run: just secrets webhook-secret $url"; exit 1; }
    dir=$(dirname "$env_file")
    [[ -d "$dir" ]] || { echo "$dir does not exist"; exit 1; }
    src="$env_file"
    [[ -f "$src" ]] || src=/dev/null
    tmp=$(mktemp "$dir/.webhook-secret-env.XXXXXX")  # mode 0600
    trap 'rm -f "$tmp"' EXIT
    # The secret goes from sops to awk through a pipe, never through argv or
    # stdout. An existing VAR= (or export VAR=) line is replaced in place.
    sops decrypt --extract "$p" "$f" | awk -v var="$var" '
        FILENAME == "-" { val = $0; next }
        index($0, var "=") == 1 || index($0, "export " var "=") == 1 {
            if (!done) { sub(/=.*/, "", $0); print $0 "=" val }
            done = 1; next
        }
        { print }
        END { if (!done) print var "=" val }
    ' - "$src" > "$tmp"
    mv "$tmp" "$env_file"
    trap - EXIT
    echo "wrote $var for $key into $env_file (mode 0600)"

# Store the receiver's token for URL (for token_header) from stdin into secrets/service.sops.yaml
[group('secrets')]
webhook-token url:
    #!/usr/bin/env bash
    set -euo pipefail
    url={{quote(url)}}
    {{WEBHOOK_KEY}}
    f="{{SERVICE_SECRETS}}"
    [[ -f "$f" ]] || { echo "no $f; run first: just secrets webhook-secret $url"; exit 1; }
    tok=
    if [[ -t 0 ]]; then
        IFS= read -rs -p "token for $key: " tok || true
        echo
    else
        IFS= read -r tok || true
    fi
    tok=${tok%$'\r'}
    [[ -n "$tok" ]] || { echo "no token on stdin"; exit 1; }
    p='["secrets"]["webhook_tokens"]['"$jkey"']'
    printf '%s' "$tok" | jq -Rc . | sops set --value-stdin "$f" "$p"
    echo "set secrets.webhook_tokens[$key]; the webhook needs token_header (for DraftForge: X-Internal-Token), then: just docker restart"
