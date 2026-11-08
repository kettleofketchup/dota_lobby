- `Secret` values are redacted in every `fmt` verb, JSON, text and `slog` output, and
  credentials files that are still sops-encrypted are rejected.
- Token and lock files are written with mode 0600 in a 0700 directory, and a second process
  using the same `state_dir` refuses to start.
