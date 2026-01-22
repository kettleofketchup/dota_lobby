# Serve docs locally with live reload
[group('docs')]
serve:
    docker build -f Dockerfile.docs -t dota-lobby-docs .
    docker run --rm -p 8000:8000 -v {{env_var("PWD")}}:/docs dota-lobby-docs serve --dev-addr=0.0.0.0:8000

# Build docs to site/ directory
[group('docs')]
build:
    docker build -f Dockerfile.docs -t dota-lobby-docs .
    docker run --rm -v {{env_var("PWD")}}:/docs dota-lobby-docs build

# Build docs without Docker (requires uv)
[group('docs')]
build-local:
    uv run mkdocs build

# Serve docs without Docker (requires uv)
[group('docs')]
serve-local:
    uv run mkdocs serve
