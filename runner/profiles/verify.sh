#!/usr/bin/env bash
set -euo pipefail

registry_prefix="${PROFILE_REGISTRY:-jcloud}"
docker_bin="${DOCKER_BIN:-docker}"

"$docker_bin" run --rm --entrypoint /bin/sh "$registry_prefix/runner-go-node:dev" \
  -c 'go version && node --version && npm --version && corepack --version'
"$docker_bin" run --rm --entrypoint /bin/sh "$registry_prefix/runner-python:dev" \
  -c 'python --version && pip --version'
"$docker_bin" run --rm --entrypoint /bin/sh "$registry_prefix/runner-rust:dev" \
  -c 'rustc --version && cargo --version'
"$docker_bin" run --rm --entrypoint /bin/sh "$registry_prefix/runner-polyglot:dev" \
  -c 'go version && node --version && npm --version && python --version && rustc --version'
