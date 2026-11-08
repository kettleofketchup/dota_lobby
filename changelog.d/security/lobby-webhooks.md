- Webhook deliveries are signed: `X-Dota-Lobby-Signature` is the HMAC-SHA256 of
  `"<X-Dota-Lobby-Timestamp>.<body>"` under the webhook's secret, with a fresh timestamp on
  every attempt so receivers can reject replays older than 5 minutes. A webhook without a
  signing secret stops startup.
- An optional static token (`webhooks[].token_header` with `secrets.webhook_tokens`) lets a
  receiver keep its existing service-token check. Neither the token, the secret nor the lobby
  password is logged or put in an event; receiver URLs are logged, and named in startup errors,
  without their query or user info.
- Redirects from a receiver are never followed.
