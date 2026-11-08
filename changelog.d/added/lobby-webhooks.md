- **Webhooks.** Every lobby event is pushed to the receivers in `webhooks` as a JSON envelope
  with a unique `id`, the lobby's `seq`, the time, a summary of the lobby (`external_ref`,
  `state`, `bot`, `gc_lobby_id`, `league_id`, `metadata`) and the event's `data`. Each
  webhook picks event types with globs in `webhooks[].events`.
- Deliveries are queued in the database in the transaction that stores the event and survive
  restarts. A lobby's events reach each receiver in `seq` order; other lobbies are not held
  back. Timeouts, connection errors, `408`, `425`, `429` and `5xx` are retried after 10 s,
  30 s, 2 min, 10 min, 30 min and then hourly (±20 %, `Retry-After` honoured) until
  `webhook_delivery.max_age` (24 h); other answers give the delivery up at once.
- `GET /v1/lobbies/{id}/events?after=&limit=` lists a lobby's events in the same envelope, for
  catching up on anything a receiver missed.
- New keys: `webhooks[].token_header`, `webhook_delivery.timeout`, `webhook_delivery.max_age`
  and `secrets.webhook_tokens`. `webhooks` and `secrets.webhook_secrets` are now used.
- Each webhook has its own 4 delivery slots, so a receiver that hangs until
  `webhook_delivery.timeout` does not delay the others. A delivery whose queue update fails
  (a full or read-only database) is tried again 30 s later instead of being resent at once.
