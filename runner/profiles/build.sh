#!/usr/bin/env bash
set -euo pipefail

base_image="${BASE_IMAGE:-jcloud/runner:dev}"
registry_prefix="${PROFILE_REGISTRY:-jcloud}"
docker_bin="${DOCKER_BIN:-docker}"

for profile in go-node python rust polyglot; do
  "$docker_bin" build \
    --file runner/profiles/Dockerfile \
    --target "$profile" \
    --build-arg "BASE_IMAGE=$base_image" \
    --tag "$registry_prefix/runner-$profile:dev" \
    .
done

"$docker_bin" image inspect \
  "$registry_prefix/runner-go-node:dev" \
  "$registry_prefix/runner-python:dev" \
  "$registry_prefix/runner-rust:dev" \
  "$registry_prefix/runner-polyglot:dev" \
  --format '{{index .RepoTags 0}} {{.Id}}'
