- Container image: a multi-stage `Dockerfile` that builds a static binary into a distroless
  `nonroot` image, published to `ghcr.io/kettleofketchup/dota_lobby` for `linux/amd64` and
  `linux/arm64` on every `v*` tag.
- `compose.yaml` for a single-machine deployment that mounts `config/` read-only.
- `just` task runner (`just go::build`, `just go::test`, `just docs::serve`, ...) and a
  `./dev` bootstrap script that installs `just` and checks the toolchain.
