# Run the service with Docker Compose. The sops-encrypted bot credentials are
# decrypted on the host into a tmpfs directory that compose.yaml mounts
# read-only; the image never sees a sops key. See
# docs/deployment/docker-compose.md.

set quiet
set working-directory := ".."

DOCKER := env_var_or_default("DOCKER", "docker")
# Where decrypted secrets live while the service runs. $XDG_RUNTIME_DIR is a
# per-user tmpfs (mode 0700), so nothing touches disk and nothing outlives a
# reboot. compose.yaml reads the same variable.
SECRETS_HOST_DIR := env_var_or_default("DOTA_LOBBY_SECRETS_HOST_DIR", env_var_or_default("XDG_RUNTIME_DIR", "/nonexistent-XDG_RUNTIME_DIR") / "dota_lobby")
BOTS_DIR := "secrets/bots"
SERVICE_SECRETS := "secrets/service.sops.yaml"
IMAGE := "ghcr.io/kettleofketchup/dota_lobby"
VERSION := `git describe --tags --always --dirty 2>/dev/null || echo "dev"`
COMMIT := `git rev-parse --short HEAD 2>/dev/null || echo "unknown"`

export DOTA_LOBBY_SECRETS_HOST_DIR := SECRETS_HOST_DIR

# Decrypt the credentials, then start the stack in the background
[group('docker')]
up: decrypt
    {{DOCKER}} compose up -d
    echo "started; decrypted secrets stay in {{SECRETS_HOST_DIR}} until: just docker down"

# Stop the stack and delete the decrypted credentials (the state volume is kept)
[group('docker')]
down: && wipe
    {{DOCKER}} compose down

# Re-decrypt the credentials and restart the container, which re-reads them
[group('docker')]
restart: decrypt
    {{DOCKER}} compose up -d
    {{DOCKER}} compose restart

# Decrypt secrets/bots/*.sops.yaml (and secrets/service.sops.yaml) into the host tmpfs dir
[group('docker')]
decrypt:
    #!/usr/bin/env bash
    set -euo pipefail
    dir="{{SECRETS_HOST_DIR}}"
    parent=$(dirname "$dir")
    [[ -d "$parent" ]] || { echo "$parent does not exist; set XDG_RUNTIME_DIR or DOTA_LOBBY_SECRETS_HOST_DIR"; exit 1; }
    # The files below are world-readable so the container user (UID 65532,
    # however the runtime maps it) can read them. The parent directory is
    # what keeps other local users out, so it must be owner-only.
    [[ $(stat -c %u "$parent") == "$(id -u)" ]] || { echo "$parent is not owned by you"; exit 1; }
    perm=$(stat -c %a "$parent")
    (( (8#$perm & 8#077) == 0 )) || { echo "$parent is mode $perm; it must be 0700 (owner-only)"; exit 1; }
    fs=$(findmnt -n -o FSTYPE --target "$parent" 2>/dev/null || echo unknown)
    [[ "$fs" == tmpfs || "$fs" == ramfs ]] || echo "warning: $parent is on $fs, not tmpfs; decrypted secrets will touch disk"
    shopt -s nullglob
    files=({{BOTS_DIR}}/*.sops.yaml)
    (( ${#files[@]} )) || echo "warning: no {{BOTS_DIR}}/*.sops.yaml; starting with no bot credentials"
    rm -rf "$dir"
    mkdir -m 0755 "$dir" "$dir/creds"
    umask 022
    for f in "${files[@]}"; do
        bot=$(basename "$f" .sops.yaml)
        sops decrypt --output-type yaml "$f" > "$dir/creds/.$bot.tmp"
        mv "$dir/creds/.$bot.tmp" "$dir/creds/$bot.yaml"
    done
    if [[ -f "{{SERVICE_SECRETS}}" ]]; then
        sops decrypt --output-type yaml "{{SERVICE_SECRETS}}" > "$dir/.secrets.tmp"
        mv "$dir/.secrets.tmp" "$dir/secrets.yaml"
    fi
    echo "decrypted ${#files[@]} bot file(s) into $dir"

# Delete the decrypted credentials from the host tmpfs dir
[group('docker')]
wipe:
    rm -rf "{{SECRETS_HOST_DIR}}"

# Follow the service logs
[group('docker')]
logs:
    {{DOCKER}} compose logs -f

# Show the container and its health
[group('docker')]
ps:
    {{DOCKER}} compose ps

# Mint a bot's refresh token inside the running container, then retry the bot
[group('docker')]
auth bot *args:
    {{DOCKER}} compose exec dota_lobby /usr/local/bin/dota_lobby auth --bot {{bot}} --force {{args}}
    echo "now: curl -X POST -H \"X-API-Key: \$KEY\" http://127.0.0.1:8080/v1/bots/{{bot}}/retry"

# Build the image locally, tagged local by default (use with DOTA_LOBBY_TAG=local)
[group('docker')]
build tag="local":
    #!/usr/bin/env bash
    set -euo pipefail
    fmt=()
    # Podman's default OCI format drops HEALTHCHECK; the docker format keeps it.
    if {{DOCKER}} version 2>/dev/null | grep -q Podman; then fmt=(--format docker); fi
    {{DOCKER}} build "${fmt[@]}" -t "{{IMAGE}}:{{tag}}" \
        --build-arg VERSION="{{VERSION}}" --build-arg COMMIT="{{COMMIT}}" \
        --build-arg BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)" .
