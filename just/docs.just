# Serve docs locally with live reload
[group('docs')]
serve:
    cd "{{justfile_directory()}}" && docker build -f Dockerfile.docs -t dota-lobby-docs .
    cd "{{justfile_directory()}}" && docker run --rm -p 8000:8000 -v "{{justfile_directory()}}":/docs dota-lobby-docs serve --dev-addr=0.0.0.0:8000

# Build docs to site/ directory
[group('docs')]
build:
    cd "{{justfile_directory()}}" && docker build -f Dockerfile.docs -t dota-lobby-docs .
    cd "{{justfile_directory()}}" && docker run --rm -v "{{justfile_directory()}}":/docs dota-lobby-docs build --strict

# Build docs without Docker (requires uv)
[group('docs')]
build-local:
    cd "{{justfile_directory()}}" && uv run mkdocs build --strict

# Serve docs without Docker (requires uv)
[group('docs')]
serve-local:
    cd "{{justfile_directory()}}" && uv run mkdocs serve
