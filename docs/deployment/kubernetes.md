# Kubernetes

This page runs dota_lobby as a single pod with plain manifests. The examples assume a GitOps
setup that keeps Secrets encrypted with sops (for example ArgoCD with ksops). Adapt the names,
the StorageClass and the ingress to your cluster.

The rules from [Docker Compose](docker-compose.md) still apply:

- **One instance per set of Steam accounts.** Two pods logged on as the same account kick each
  other off the Game Coordinator. Use `replicas: 1` and `strategy: Recreate`, never a rolling
  update, which briefly runs the old and new pod together.
- **Keep the state volume.** Renewed refresh tokens and the lobby database live in
  `/app/state`. Losing it means every bot logs on with its password again and every lobby
  is forgotten.

## Objects

| Object | Purpose |
|---|---|
| `Deployment` | One replica, `Recreate`, the image pinned to a release (`ghcr.io/kettleofketchup/dota_lobby:0.1.0`) |
| `PersistentVolumeClaim` | `/app/state`: `ReadWriteOnce` block storage. SQLite and the `flock` on `LOCK` are not safe on a network filesystem such as NFS or CephFS |
| `ConfigMap` | `config.yaml`, mounted at `/app/config` |
| `Secret` (API key) | `DOTA_LOBBY_SECRETS_API_KEY`, loaded with `envFrom` |
| `Secret` (bots) | One key per bot, `<bot>.yaml`, mounted as `credentials_dir` |
| `Service` | Port 8080. Metrics are on 9090 if you scrape them |

## Configuration

Put the non-secret settings in a ConfigMap. Point `credentials_dir` at the bot Secret's mount
and list the bots that should run:

```yaml
# config.yaml
server:
  host: 0.0.0.0
  port: 8080
state_dir: /app/state
credentials_dir: /run/secrets/dota_lobby/creds
log:
  level: info
  format: json
readiness:
  min_ready_bots: 1
match:
  webapi: { enabled: false }   # until secrets.webapi_key is set
bots:
  - name: draftforge1
    enabled: true
```

Set `DOTA_LOBBY_CONFIG_FILE=/app/config/config.yaml` on the container. Unknown keys stop the
service at startup, so a typo shows up as a crash loop with the key named in the log.

## Credentials

The service reads `<credentials_dir>/<bot>.yaml` for each bot in `bots:`. Keep one Secret with
one key per bot and mount the whole Secret as that directory:

```yaml
volumes:
  - name: creds
    secret:
      secretName: dota-lobby-bots
      defaultMode: 0400
```

With sops-encrypted manifests, fill the Secret from the bot files in this repository with
`just secrets k8s-secret`. It decrypts `secrets/bots/<bot>.sops.yaml` in a pipe and writes the
credentials into the manifest as `stringData["<bot>.yaml"]`, still encrypted, keeping any other
bots already in it:

```bash
just secrets k8s-secret draftforge1 path/to/dota-lobby-bots.enc.yaml
```

The target file must already exist as an encrypted `kind: Secret`, created with your
cluster repository's own sops rules. Only `STEAM_USER`, `STEAM_PASS`,
`STEAM_SHARED_SECRET` and `STEAM_REFRESH_TOKEN` are copied.

The API key goes in a second Secret, read through the environment:

```yaml
envFrom:
  - secretRef:
      name: dota-lobby-api   # stringData: DOTA_LOBBY_SECRETS_API_KEY
```

Generate it with `openssl rand -hex 32` and give the same value to the client (DraftForge's
`DOTA_LOBBY_API_KEY`).

Restart the pod after changing either Secret: `envFrom` and the configuration are read at
start. With [Reloader](https://github.com/stakater/Reloader), annotate the Deployment with
`secret.reloader.stakater.com/reload: dota-lobby-api,dota-lobby-bots` to restart it
automatically.

## The pod

The image runs as `65532:65532` on a distroless base and works with the `restricted` Pod
Security Standard:

```yaml
spec:
  replicas: 1
  strategy:
    type: Recreate
  template:
    spec:
      terminationGracePeriodSeconds: 30   # 5 s for HTTP, 15 s for the bots
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        runAsGroup: 65532
        fsGroup: 65532                    # makes the new volume writable
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: dota-lobby
          image: ghcr.io/kettleofketchup/dota_lobby:0.1.0
          args: ["serve"]
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
```

### Probes

Use `/livez` for both probes. `/readyz` turns `503` whenever fewer than
`readiness.min_ready_bots` bots are ready, for example while Steam is down. As a readiness
probe it would take the pod out of the Service, so `/v1/bots` and the lobby endpoints would
become unreachable exactly when you need them to see what is wrong.

```yaml
livenessProbe:
  httpGet: { path: /livez, port: 8080 }
  periodSeconds: 30
  timeoutSeconds: 5
  failureThreshold: 3
readinessProbe:
  httpGet: { path: /livez, port: 8080 }
  periodSeconds: 10
```

`/livez` fails when a bot's loop has stalled for 30 s, which is the case a restart fixes.

## Exposing it

`/v1` needs the API key; the health endpoints are open. If the client is outside the cluster,
put the Service behind your ingress and also restrict who can reach it.

For example, published through a tunnel with no SSO in front of it, Traefik can allow only
the client's address:

```yaml
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata:
  name: dota-lobby-draftforge-only
spec:
  ipAllowList:
    sourceRange: ["203.0.113.10/32"]   # the client's public address
    ipStrategy:
      depth: 1
```

Behind a tunnel or proxy, Traefik sees the proxy as the client. The allowlist only works
when the entrypoint trusts `X-Forwarded-For` from the proxy
(`forwardedHeaders.trustedIPs`), and `depth: 1` then picks the address the edge saw. Anyone
who can reach the entrypoint from a trusted range can forge that header, so treat the
allowlist as a second layer and the API key as the gate.

## Adding a bot

1. Create its credentials here: `just secrets new-bot <bot>` (and `just secrets import-mafile`
   for the authenticator), as in [Bot accounts](../configuration/bot-accounts.md).
2. Make sure nothing else runs that account, such as a local `just docker up`.
3. Copy it into the cluster Secret: `just secrets k8s-secret <bot> <path to the bots Secret>`.
4. Add it to `bots:` in the ConfigMap.
5. Commit both and let the pod restart. The new bot logs on with its password and stores its
   token in `/app/state`.

To take a bot out, set `enabled: false` (or remove it from `bots:`) before deleting its key
from the Secret.

## Running the other subcommands

The image has no shell, but `kubectl exec` can run the binary directly:

```bash
kubectl -n dota-lobby exec deploy/dota-lobby -- /usr/local/bin/dota_lobby version
kubectl -n dota-lobby exec -it deploy/dota-lobby -- /usr/local/bin/dota_lobby auth --bot <bot> --force
```

After `auth`, call `POST /v1/bots/<bot>/retry`, as in
[Re-authenticating a bot](docker-compose.md#re-authenticating-a-bot). Port-forward to reach the
API from your machine without going through the ingress:

```bash
kubectl -n dota-lobby port-forward svc/dota-lobby 8080:8080
curl -s -H "X-API-Key: $KEY" localhost:8080/v1/bots
```

## Backups and upgrades

A release with pending migrations writes a checked copy of the database to
`/app/state/backups/` before migrating; see [Backups](docker-compose.md#backups). For a manual
copy on the volume, run `backup` in the pod:

```bash
kubectl -n dota-lobby exec deploy/dota-lobby -- /usr/local/bin/dota_lobby backup --out /app/state/backups/ --missing-ok
```

Both copies stay on the same volume as the database. The image has no shell or `tar`, so
`kubectl cp` cannot read them. For a copy that survives losing the volume, snapshot the
volume with your storage's tools (a CSI `VolumeSnapshot` on Ceph RBD), or scale the
Deployment to 0 and mount the claim in a temporary pod that has a shell.

To upgrade, change the image tag. `Recreate` stops the old pod before the new one starts,
so the two never share the volume or the accounts.
