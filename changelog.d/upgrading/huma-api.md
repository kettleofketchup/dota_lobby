API errors changed format. Every error is now an RFC 9457 problem with
`Content-Type: application/problem+json`, and the fields moved to the top level:

```json
{"title": "Conflict", "status": 409, "detail": "lobby is in_game", "code": "invalid_state", "retryable": false}
```

was

```json
{"error": {"code": "invalid_state", "message": "lobby is in_game", "retryable": false}}
```

Read `code` and `retryable` from the top level, and `detail` instead of `message`. The `code`
values are unchanged, with `internal` added for a `500`.

Requests that fail validation now answer `422 invalid_request` instead of `400`, with one
entry per problem in `errors` (`location` such as `body.teams.radiant.players[0]` or
`query.limit`, and `message`). This covers unknown or mistyped body fields, missing required
fields (`external_ref`, `name`, `teams` on create; `teams` on roster; `account_id` on kick),
bad query parameters (`state`, `active`, `limit`, `after`) and bad path parameters such as a
league ID that is not a positive 32-bit number. `400` is left for a body that is not JSON. A
body without a `Content-Type` is now read as JSON instead of answering `415`.
