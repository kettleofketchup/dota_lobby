- The HTTP API runs on huma v2 on the standard library mux. Routes, success statuses and
  response fields are unchanged; errors are `application/problem+json` and validation errors
  are `422` (see Upgrading).
- Error responses never echo a rejected request body or object back, so a lobby password in a
  bad request does not reach a client's error log. The cause of a `5xx` is logged, never sent.
- `invited` in the `POST /v1/lobbies/{id}/invite` response is `[]` rather than `null` when
  nobody was invited.
