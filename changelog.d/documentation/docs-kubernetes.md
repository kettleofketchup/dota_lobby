- New [Kubernetes](https://kettleofketchup.github.io/dota_lobby/deployment/kubernetes/)
  deployment page: one replica with `Recreate`, state on `ReadWriteOnce` block storage, bot
  credentials as one Secret key per bot mounted as `credentials_dir`, `/livez` for both
  probes, restricting access behind a tunnel, adding a bot, and backups.
