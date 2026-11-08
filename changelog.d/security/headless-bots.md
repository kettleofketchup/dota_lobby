- `Secret` values are redacted in every `fmt` verb, JSON, text and `slog` output, and
  credentials files that are still sops-encrypted are rejected.
- Token and lock files are written with mode 0600 in a 0700 directory, and a second process
  using the same `state_dir` refuses to start.
- A wrong method on a protected `/v1` path answers `401` without the API key, so the `Allow`
  list is not revealed to unauthenticated callers.
- Steam Guard codes are held as redacted secrets in the Steam client credentials.
