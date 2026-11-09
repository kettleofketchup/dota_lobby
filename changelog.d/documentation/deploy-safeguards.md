- A Backups section in the Docker Compose guide: the automatic pre-migration copies,
  `just docker backup`, and how to restore one. The upgrade steps now take a backup first and
  point to the changelog's upgrade notes.
- The webhooks guide shows the `just secrets` recipes for setting up DraftForge, and notes that
  a `403` from DraftForge's allowlist or its Helm ingress gives deliveries up at once.
- The `store.backup.*` keys in the configuration reference, the `backup` subcommand in the
  build-from-source guide, and a troubleshooting entry for a failed pre-migration backup.
