- **Backup before migrations.** Before it applies a pending migration to an existing database,
  the service writes a copy to `<state_dir>/backups/`
  (`dota_lobby-<from>-to-<to>-<UTC time>.db`) and checks it. Once the migrations have
  succeeded it keeps the newest 5; a failed migration deletes none. If the copy fails,
  nothing is migrated and the service exits with
  `refusing to migrate: pre-migration backup failed`. New keys: `store.backup.enabled`,
  `store.backup.dir` and `store.backup.keep`.
- `dota_lobby backup --out PATH` copies the database on demand. It opens it read-only and takes
  no lock, so it works while the service runs, and it needs no secrets. A directory `--out`
  gets a timestamped `dota_lobby-manual-*.db`, and `--keep-manual N` prunes older manual copies
  there.
- `just docker backup [dir]` takes that snapshot inside the container, running or stopped, and
  copies it to a host directory (`backups/` by default).
- `just secrets webhook-secret <url>` generates a webhook signing secret into
  `secrets/service.sops.yaml` without printing it. `just secrets webhook-secret-env <url>
  <env_file>` hands the same secret to the receiver's env file, `just secrets webhook-token
  <url>` stores a receiver token read from stdin, and `just secrets list` shows which webhook
  URLs have a secret or token.
