The database gets a webhook delivery queue on the first start; the migration runs on its own.

`webhooks` entries were accepted but unused before. They are now delivered, and each one needs
its signing secret under `secrets.webhook_secrets`, keyed by the exact URL, or the service
refuses to start. A URL may now appear only once in `webhooks`.
