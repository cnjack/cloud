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
#   - The final image carries no jcode source tree and no jcode build cache.
#     (A Go toolchain IS installed for the agent's own use on target repos —
#     see the toolchain section below; it just isn't used to build jcode.)
# Tradeoff: the build.sh/test.sh host must have Go + the jcode source checkout.
#   The extension point for a fully self-contained build (multi-stage `FROM golang`
#   compiling from a COPYied source tree) is documented in README.md.
#
# The image expects these binaries in the build context (produced by build.sh):
#   ./bin/jcode      linux/<arch> static, built with `-tags jcode_headless` CGO off
#   ./bin/acpdrive   linux/<arch> ACP client that drives one headless jcode turn
#                    AND streams agent text/tool events to the orchestrator
#   ./bin/orchclient linux/<arch> stdlib-only helper the entrypoint uses to POST
#                    run.failure events and upload the diff artifact
#   ./bin/mockllm    linux/<arch> (optional; only bundled for self-contained tests)
#
# P0 keeps the base minimal: git + ca-certificates + curl + zstd + ripgrep (see
# the install comment below). Language toolchains (python3/node/go/rust) ARE
# installed — the agent works on arbitrary repos and must be able to run their
# builds and tests, not just read code. See the toolchain section below.

FROM debian:bookworm-slim

# git: clone the target repo and produce the diff.
# ca-certificates: HTTPS clones.
# ripgrep: jcode's search/grep tool hard-requires `rg` in PATH — without it
#   every workspace search fails with "ripgrep (rg) is required but not found".
# curl + zstd: the persistent-workspace archive layer (F10 / D23 ③). entrypoint.sh
#   in archive mode (RUN_ARCHIVE=1) tars the PVC and PUTs it to a presigned S3/
#   MinIO URL with curl, compressing with zstd (gzip fallback); the restore path
#   (RESTORE_ARCHIVE_URL) curl-downloads and untars. tar/gzip are in the base
#   image; curl and zstd are not, so add them. (tar autodetects zstd on extract
#   only when the zstd binary is present.)
RUN apt-get update \
    && apt-get install -y --no-install-recommends git ca-certificates curl zstd ripgrep \
    && rm -rf /var/lib/apt/lists/*

# --- Language toolchains ---------------------------------------------------
# The agent works on arbitrary repos: it must be able to run their tests and
# scripts (pytest, npm test, go test, cargo test …), not just read code.
# Downloads are version-pinned and sha256-verified; bump a version together
# with its hashes.
ARG GO_VERSION=1.26.5
ARG NODE_VERSION=24.18.0
# TARGETARCH is a BuildKit built-in (amd64|arm64), driven by --platform.
ARG TARGETARCH

# Python (bookworm ships 3.11) + a C toolchain: pip/node-gyp native builds
# and the Rust linker all need gcc/make.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
       build-essential python3 python3-pip python3-venv python3-dev \
    && rm -rf /var/lib/apt/lists/*

# This is a throwaway CI-style container, not a managed host: let pip install
# system-wide instead of forcing every run through PEP 668 venv ceremony.
ENV PIP_BREAK_SYSTEM_PACKAGES=1

# Go — official tarball, sha256-verified per arch.
RUN set -eux; \
    case "$TARGETARCH" in \
      amd64) sha=5c2c3b16caefa1d968a94c1daca04a7ca301a496d9b086e17ad77bb81393f053 ;; \
      arm64) sha=fe4789e92b1f33358680864bbe8704289e7bb5fc207d80623c308935bd696d49 ;; \
      *) echo "unsupported TARGETARCH $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    curl -fsSLo /tmp/go.tgz "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz"; \
    echo "$sha  /tmp/go.tgz" | sha256sum -c -; \
    tar -C /usr/local -xzf /tmp/go.tgz; \
    rm /tmp/go.tgz

# Node.js LTS — official tarball, sha256-verified per arch (npm/npx bundled).
RUN set -eux; \
    case "$TARGETARCH" in \
      amd64) narch=x64;   sha=55aa7153f9d88f28d765fcdad5ae6945b5c0f98a36881703817e4c450fa76742 ;; \
      arm64) narch=arm64; sha=58c9520501f6ae2b52d5b210444e24b9d0c029a58c5011b797bc1fe7105886f6 ;; \
      *) echo "unsupported TARGETARCH $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    curl -fsSLo /tmp/node.tar.xz "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-${narch}.tar.xz"; \
    echo "$sha  /tmp/node.tar.xz" | sha256sum -c -; \
    tar -C /usr/local --strip-components=1 -xJf /tmp/node.tar.xz; \
    rm /tmp/node.tar.xz

# Rust — rustup minimal profile (rustc + cargo), sha256-verified installer,
# system-wide homes so any invoking user can run them.
ENV RUSTUP_HOME=/usr/local/rustup CARGO_HOME=/usr/local/cargo
RUN set -eux; \
    case "$TARGETARCH" in \
      amd64) rarch=x86_64-unknown-linux-gnu; sha=4acc9acc76d5079515b46346a485974457b5a79893cfb01112423c89aeb5aa10 ;; \
      arm64) rarch=aarch64-unknown-linux-gnu;  sha=9732d6c5e2a098d3521fca8145d826ae0aaa067ef2385ead08e6feac88fa5792 ;; \
      *) echo "unsupported TARGETARCH $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    curl -fsSLo /tmp/rustup-init "https://static.rust-lang.org/rustup/dist/${rarch}/rustup-init"; \
    echo "$sha  /tmp/rustup-init" | sha256sum -c -; \
    chmod +x /tmp/rustup-init; \
    /tmp/rustup-init -y --no-modify-path --profile minimal --default-toolchain stable; \
    rm /tmp/rustup-init; \
    chmod -R a+w "$RUSTUP_HOME" "$CARGO_HOME"

ENV PATH="/usr/local/cargo/bin:/usr/local/go/bin:${PATH}"

# Fail at build time, not mid-run, if any toolchain came out broken.
RUN go version && node --version && npm --version && python3 --version && cargo --version && rustc --version

# Prebuilt static binaries (compiled on host for the image's target arch).
COPY bin/jcode     /usr/local/bin/jcode
COPY bin/acpdrive  /usr/local/bin/acpdrive
COPY bin/orchclient /usr/local/bin/orchclient
# mockllm is bundled so the container can run fully self-contained in tests; in
# production the MODEL_BASE_URL points at a real (or sidecar) model instead.
COPY bin/mockllm   /usr/local/bin/mockllm

COPY entrypoint.sh /usr/local/bin/entrypoint.sh
# turn-hook.sh (F7a / D22): the per-turn diff/commit/bundle logic, extracted
# from entrypoint.sh so acpdrive can exec it directly as --turn-hook in
# multi-turn session mode (RUN_SESSION=1). Lands alongside entrypoint.sh so
# entrypoint.sh's SCRIPT_DIR-relative lookup finds it in the image too.
COPY turn-hook.sh  /usr/local/bin/turn-hook.sh
RUN chmod +x /usr/local/bin/jcode /usr/local/bin/acpdrive /usr/local/bin/orchclient /usr/local/bin/mockllm /usr/local/bin/entrypoint.sh /usr/local/bin/turn-hook.sh

# jcode writes its config + logs under $HOME/.jcode. Keep it explicit.
ENV HOME=/root
WORKDIR /workspace

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
