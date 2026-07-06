# syntax=docker/dockerfile:1
#
# jcode headless runner image (P0).
#
# Build strategy: binaries are compiled on the HOST (see build.sh / test.sh) and
# COPYied in here, rather than compiling jcode from source inside the image.
# Rationale:
#   - jcode's module graph pulls a large set of deps; a clean in-image `go build`
#     needs network access and re-downloads the whole graph on every cache miss.
#     The host already has a warm module cache, so host-compile is far faster and
#     hermetic against transient registry failures.
#   - The final image stays minimal: no Go toolchain, no source tree.
# Tradeoff: the build.sh/test.sh host must have Go + the jcode source checkout.
#   The extension point for a fully self-contained build (multi-stage `FROM golang`
#   compiling from a COPYied source tree) is documented in README.md.
#
# The image expects three binaries in the build context (produced by build.sh):
#   ./bin/jcode      linux/<arch> static, built with `-tags jcode_headless` CGO off
#   ./bin/acpdrive   linux/<arch> ACP client that drives one headless jcode turn
#   ./bin/mockllm    linux/<arch> (optional; only bundled for self-contained tests)
#
# P0 keeps the base minimal: git + ca-certificates only. Language toolchains
# (go/java/python/node) are intentionally NOT installed — see README "Deferred".

FROM debian:bookworm-slim

# git: clone the target repo and produce the diff.
# ca-certificates: HTTPS clones.
RUN apt-get update \
    && apt-get install -y --no-install-recommends git ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Prebuilt static binaries (compiled on host for the image's target arch).
COPY bin/jcode    /usr/local/bin/jcode
COPY bin/acpdrive /usr/local/bin/acpdrive
# mockllm is bundled so the container can run fully self-contained in tests; in
# production the MODEL_BASE_URL points at a real (or sidecar) model instead.
COPY bin/mockllm  /usr/local/bin/mockllm

COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/jcode /usr/local/bin/acpdrive /usr/local/bin/mockllm /usr/local/bin/entrypoint.sh

# jcode writes its config + logs under $HOME/.jcode. Keep it explicit.
ENV HOME=/root
WORKDIR /workspace

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
