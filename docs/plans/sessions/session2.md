# Session 2: MkDocs Documentation Setup

**Date:** 2026-01-22
**Status:** In Progress

## Objective

Set up MkDocs Material documentation with GitHub Pages deployment, using:
- uv for Python dependency management
- Docker for building docs
- just command runner for task automation

## Decisions Made

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Audience | Both end users and contributors | Comprehensive docs as single source of truth |
| Theme | MkDocs Material | Modern, feature-rich, dark mode, good search |
| Scope | Comprehensive from start | Go-to place for documentation |
| Python deps | uv | Fast, modern Python package manager |
| Build | Docker | Isolated, reproducible builds |
| Task runner | just | Replace Makefile, better syntax |

## Directory Structure

```
dota_lobby/
├── dev                     # Bootstrap script (installs just)
├── justfile                # Root: imports and modules
├── just/
│   ├── dev.just            # Dev setup (imported)
│   ├── go.just             # Go module (go::build, go::test, go::lint)
│   └── docs.just           # Docs module (docs::serve, docs::build)
├── mkdocs.yml              # MkDocs configuration
├── pyproject.toml          # uv/Python deps for docs
├── Dockerfile.docs         # Docker image for building docs
├── docs/
│   ├── index.md
│   ├── getting-started/
│   │   ├── installation.md
│   │   ├── configuration.md
│   │   └── quickstart.md
│   ├── api/
│   │   ├── overview.md
│   │   ├── authentication.md
│   │   └── endpoints.md
│   ├── guides/
│   │   ├── deployment.md
│   │   └── troubleshooting.md
│   ├── development/
│   │   ├── architecture.md
│   │   ├── contributing.md
│   │   └── testing.md
│   └── reference/
│       └── configuration-reference.md
└── .github/workflows/
    └── docs.yml            # GitHub Pages deployment
```

## Files Created

### Just Bootstrap

- [x] `dev` - Bootstrap script
- [x] `justfile` - Root justfile
- [x] `just/dev.just` - Dev setup recipes
- [x] `just/go.just` - Go build/test/lint recipes (migrated from Makefile)
- [x] `just/docs.just` - Docs build/serve recipes

### Docker & uv

- [ ] `Dockerfile.docs` - Python 3.12-slim with uv and mkdocs-material
- [ ] `pyproject.toml` - Dependencies: mkdocs-material>=9.5, mkdocs-minify-plugin

### MkDocs Config

- [ ] `mkdocs.yml` - Material theme, dark/light toggle, mermaid diagrams, code highlighting

### GitHub Actions

- [ ] `.github/workflows/docs.yml` - Build with Docker, deploy to GitHub Pages

### Documentation Content

- [ ] `docs/index.md` - Home with feature cards and architecture diagram
- [ ] `docs/getting-started/installation.md` - Install methods (just, go install, release)
- [ ] `docs/getting-started/configuration.md` - config.yaml and secrets.yaml setup
- [ ] `docs/getting-started/quickstart.md` - 5-minute getting started
- [ ] `docs/api/overview.md` - API summary and response format
- [ ] `docs/api/authentication.md` - API key setup and usage
- [ ] `docs/api/endpoints.md` - Full endpoint documentation
- [ ] `docs/guides/deployment.md` - nginx/Traefik, Docker, systemd
- [ ] `docs/guides/troubleshooting.md` - Common issues and fixes
- [ ] `docs/development/architecture.md` - Code structure
- [ ] `docs/development/contributing.md` - How to contribute
- [ ] `docs/development/testing.md` - Running tests
- [ ] `docs/reference/configuration-reference.md` - All config options

## Remaining Work

Continue this session to create:
1. Docker and uv configuration files
2. mkdocs.yml with Material theme
3. GitHub Actions workflow
4. All documentation content files
5. Remove old Makefile

## Commands Reference

```bash
# Bootstrap development
./dev

# Go commands
just go::build      # Build binary
just go::run        # Build and run
just go::test       # Run tests
just go::lint       # Run linter

# Docs commands
just docs::serve    # Serve locally (Docker)
just docs::build    # Build site (Docker)
just docs::serve-local  # Serve locally (requires uv)

# Cleanup
just clean          # Remove all artifacts
just rebuild        # Clean and rebuild everything
```
