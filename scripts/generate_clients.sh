#!/usr/bin/env bash
# Generate the client SDKs under clients/ from the document this service serves.
#
# The generator input is the derived document (clients/openapi.json), not the
# served copy: the served one keeps the reference's branding because it is the
# HTTP contract, while the SDKs are this project's artifacts. Rebuild the derived
# document with scripts/derive_client_spec.py whenever the served one changes
# (this script does that first, always).
#
# One generator per language, each pinned so regeneration is reproducible:
#
#   go          openapi-generator v7.10.0, `go` generator, package `wenyuan`
#   python      openapi-generator v7.10.0, `python` generator, asyncio library
#   typescript  @hey-api/openapi-ts (version pinned in clients/typescript)
#   rust        progenitor, driven by clients/rust/build.rs at build time
#
# A language's generator owns only part of its directory: packaging metadata,
# the thin wrapper and the tests are maintained by hand (see clients/README.md),
# so an invocation regenerates the generated part and leaves the rest alone.
#
# Usage:
#   scripts/generate_clients.sh            # every language
#   scripts/generate_clients.sh go         # one language
#   scripts/generate_clients.sh spec       # only rebuild the derived document
#
# Environment:
#   OPENAPI_GENERATOR_VERSION  the docker tag to use (default v7.10.0)

set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
GENERATOR_VERSION=${OPENAPI_GENERATOR_VERSION:-v7.10.0}
CLIENTS="$ROOT/clients"
SPEC="$CLIENTS/openapi.json"
IMAGE="openapitools/openapi-generator-cli:$GENERATOR_VERSION"

# The generator's own git metadata and its boilerplate output.
GENERATOR_BOILERPLATE=(docs git_push.sh .travis.yml .gitlab-ci.yml .openapi-generator-ignore .openapi-generator)

spec() {
  python3 "$ROOT/scripts/derive_client_spec.py"
}

run_generator() { # <language dir> <generator args...>
  local dir="$1"; shift
  docker run --rm \
    --platform linux/amd64 \
    --user "$(id -u):$(id -g)" \
    -v "$SPEC:/local/openapi.json" \
    -v "$CLIENTS/$dir:/local/out" \
    "$IMAGE" generate \
    -i /local/openapi.json \
    -o /local/out \
    "$@"
  local name
  for name in "${GENERATOR_BOILERPLATE[@]}"; do
    rm -rf "$CLIENTS/$dir/$name"
  done
}

# The go generator emits a few warts that make the client wrong out of the box.
# They are corrected here, mechanically, right after generation.
patch_go_generated() { # <dir>
  local dir="$1"

  # anyOf/oneOf union types get a pointer-receiver MarshalJSON. encoding/json
  # only honours MarshalJSON when it is in the value's method set, and the
  # models declare these unions as plain struct fields (not pointers), so
  # without this the union would marshal as its Go struct shape. The bodies
  # never dereference the receiver, so a value receiver is equivalent.
  sed -i -E 's/^func \((src|o) \*([A-Za-z0-9_]+)\) MarshalJSON\(\)/func (\1 \2) MarshalJSON()/' "$dir"/model_*.go

  # The generated decoders call DisallowUnknownFields, which makes the client
  # reject any field the server adds later. The API is expected to keep
  # growing, so unknown fields are ignored, as encoding/json does by default.
  sed -i '/decoder\.DisallowUnknownFields()/d' "$dir"/model_*.go

  # The file-upload service references os.File but the generator leaves out
  # the import; gofmt below sorts it into place.
  local f
  for f in "$dir"/api_*.go; do
    if grep -q '\*os\.File' "$f" && ! grep -q '"os"' "$f"; then
      sed -i '0,/^import (/{s/^import (/import (\n\t"os"/}' "$f"
    fi
  done
}

generate_go() {
  # The generated API/model files are rewritten in place, but the generator also
  # emits its own go.mod, README.md and go.sum. Those, the wrapper and the tests
  # are maintained by hand, so stash them and restore them after the run instead
  # of letting the generator overwrite our files.
  local dir="$CLIENTS/go"
  local stash maintained f
  stash=$(mktemp -d)
  maintained=(go.mod go.sum README.md wenyuan_client.go wenyuan_client_test.go)
  for f in "${maintained[@]}"; do
    [ -e "$dir/$f" ] && cp -a "$dir/$f" "$stash/$f"
  done

  rm -f "$dir"/api_*.go "$dir"/model_*.go
  rm -f "$dir"/client.go "$dir"/configuration.go \
        "$dir"/response.go "$dir"/utils.go
  run_generator go -g go \
    --package-name wenyuan \
    --git-user-id studyzy \
    --git-repo-id wenyuan/clients/go \
    --additional-properties=isGoSubmodule=true,enumClassPrefix=true,structPrefix=true,generateInterfaces=true \
    --global-property apiDocs=false,apiTests=false,modelDocs=false,modelTests=false

  # The generated api tests are stale boilerplate; this client does not ship them.
  rm -rf "$dir/test"

  for f in "${maintained[@]}"; do
    [ -e "$stash/$f" ] && cp -a "$stash/$f" "$dir/$f"
  done
  rm -rf "$stash"

  patch_go_generated "$dir"
  # gofmt does not recurse into directories, so name the files explicitly.
  # A single gofmt pass is not always enough for the generator's doc comments
  # (its reformatting is not idempotent in one pass), so repeat until stable.
  local pass=0
  while [ "$pass" -lt 5 ] && [ -n "$(gofmt -l "$dir"/*.go)" ]; do
    gofmt -w "$dir"/*.go
    pass=$((pass + 1))
  done
}

generate_python() {
  # openapi-generator writes the package plus its own packaging files; only the
  # package is kept, because the distribution metadata and the wrapper are ours.
  local tmp
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' RETURN
  docker run --rm \
    --platform linux/amd64 \
    --user "$(id -u):$(id -g)" \
    -v "$SPEC:/local/openapi.json" \
    -v "$tmp:/local/out" \
    -v "$CLIENTS/python/openapi-generator-config.yaml:/local/config.yaml" \
    "$IMAGE" generate \
    -i /local/openapi.json \
    -g python \
    -o /local/out \
    -c /local/config.yaml
  rm -rf "$CLIENTS/python/wenyuan_client_api"
  cp -R "$tmp/wenyuan_client_api" "$CLIENTS/python/"
  touch "$CLIENTS/python/wenyuan_client_api/py.typed"
  # Two generated files cannot ship as-is; the patch lives in the python tree so
  # regeneration re-applies it (see clients/python/scripts/patch_generated.py).
  python3 "$CLIENTS/python/scripts/patch_generated.py"
}

generate_typescript() {
  # The generator version is pinned in clients/typescript/package.json, so the
  # local install is the one that runs.
  (cd "$CLIENTS/typescript" && npm run generate)
}

generate_rust() {
  # progenitor runs from build.rs, so generation is a build.
  (cd "$CLIENTS/rust" && cargo build)
}

case "${1:-all}" in
  spec)       spec ;;
  go)         spec; generate_go ;;
  python)     spec; generate_python ;;
  typescript) spec; generate_typescript ;;
  rust)       spec; generate_rust ;;
  all)
    spec
    generate_go
    generate_python
    generate_typescript
    generate_rust
    ;;
  *)
    echo "usage: $0 [all|spec|go|python|typescript|rust]" >&2
    exit 2
    ;;
esac