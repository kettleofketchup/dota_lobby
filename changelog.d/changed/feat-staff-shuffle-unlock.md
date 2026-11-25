- `POST /v1/lobbies/{id}/teams/shuffle` now works on a lobby with a roster: it unlocks the
  teams first instead of answering `409 invalid_state`. It takes an optional `{"times": N}` body.
