From this release on, the service copies its database before it applies any migration, to
`<state_dir>/backups/` (`/app/state/backups/` in the container). The first start after an
upgrade that brings migrations writes a copy as large as the database, so leave that much free
space on the state volume. If the copy cannot be written, the service exits with
`refusing to migrate: pre-migration backup failed` and leaves the database untouched. The new
keys `store.backup.enabled` (default `true`), `store.backup.dir` (default
`<state_dir>/backups`) and `store.backup.keep` (default `5`) need no change.

Migrations cannot be undone, so before every upgrade also take a copy on the host. With Docker
Compose, after `docker compose pull` and before `just docker up`:

```bash
just docker backup
```

It runs the new image in a one-off container on the state volume, never the old container,
which has no `backup` command. So it works whether the old container is running or not, with
the new `DOTA_LOBBY_TAG` set and pulled.
