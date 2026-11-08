- Container image: a multi-stage `Dockerfile` that builds a static binary into a distroless
  `nonroot` image, published to `ghcr.io/kettleofketchup/dota_lobby` for `linux/amd64` and
  `linux/arm64` on every `v*` tag.
- `compose.yaml` for a single-machine deployment that mounts `config/` read-only.
- `just docs::serve` and `just docs::build` work (the `Dockerfile.docs` and `pyproject.toml` they
  need were missing), and the `just go::*` recipes run from the repo root wherever `just` is
  called from.
