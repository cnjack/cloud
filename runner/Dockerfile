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
# The image expects these binaries in the build context (produced by build.sh):
#   ./bin/jcode      linux/<arch> static, built with `-tags jcode_headless` CGO off
#   ./bin/acpdrive   linux/<arch> ACP client that drives one headless jcode turn
#                    AND streams agent text/tool events to the orchestrator
#   ./bin/orchclient linux/<arch> stdlib-only helper the entrypoint uses to POST
#                    run.failure events and upload the diff artifact
#   ./bin/mockllm    linux/<arch> (optional; only bundled for self-contained tests)
#
# P0 keeps the base minimal: git + ca-certificates + curl + zstd + ripgrep (see
# the install comment below). Language toolchains (go/java/python/node) are
# intentionally NOT installed — see README "Deferred".

FROM debian:bookworm-slim

# BuildKit supplies TARGETARCH from --platform. Do not give it a default here:
# a default would override the automatic value and can create a mixed-arch image
# when a developer validates the runner on arm64.
ARG TARGETARCH
ARG GH_VERSION=2.94.0
ARG GLAB_VERSION=1.109.0
ARG TEA_VERSION=0.10.1

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

# Provider command bundle. Versions and checksums are pinned as part of the
# jcloud release so tasks never download or silently upgrade their tooling.
RUN set -eux; \
    case "$TARGETARCH" in \
      amd64) \
        gh_sha="a757f1ba6db18f4de8cbadb244843a5f89bc75b5e7c6fc127d2bd77fbd12ed62"; \
        glab_sha="67b4a8557727a058f44e0839babdcf214ea6f6f829062cf0bae02f7b25814e5d"; \
        tea_sha="2d19aa860aa5f97b31f6d880f0b44455874197dc12bc3d48e0ccb2cac8f16469" ;; \
      arm64) \
        gh_sha="705a23b70b0f1b7ba4c302fdcef392ce3edaacfa7ce8e85e4d93d72ea800a538"; \
        glab_sha="288155229b7a0824aaca5fbdc36000d0c41fe5d8b4a4c3cbe9908e75f4e4ec2e"; \
        tea_sha="d162dc4bbeb8a7869963fdc118243d9689ffc8a9d6b28bdf025966bf1c1e9d93" ;; \
      *) echo "unsupported TARGETARCH=$TARGETARCH" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /tmp/gh.tgz "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_${TARGETARCH}.tar.gz"; \
    echo "${gh_sha}  /tmp/gh.tgz" | sha256sum -c -; \
    tar -xzf /tmp/gh.tgz -C /tmp; \
    install -m 0755 "/tmp/gh_${GH_VERSION}_linux_${TARGETARCH}/bin/gh" /usr/local/bin/gh; \
    curl -fsSL -o /tmp/glab.tgz "https://gitlab.com/gitlab-org/cli/-/releases/v${GLAB_VERSION}/downloads/glab_${GLAB_VERSION}_linux_${TARGETARCH}.tar.gz"; \
    echo "${glab_sha}  /tmp/glab.tgz" | sha256sum -c -; \
    tar -xzf /tmp/glab.tgz -C /tmp; \
    install -m 0755 /tmp/bin/glab /usr/local/bin/glab; \
    curl -fsSL -o /tmp/tea "https://gitea.com/gitea/tea/releases/download/v${TEA_VERSION}/tea-${TEA_VERSION}-linux-${TARGETARCH}"; \
    echo "${tea_sha}  /tmp/tea" | sha256sum -c -; \
    install -m 0755 /tmp/tea /usr/local/bin/tea; \
    rm -rf /tmp/gh.tgz /tmp/glab.tgz /tmp/tea "/tmp/gh_${GH_VERSION}_linux_${TARGETARCH}" /tmp/bin

# Prebuilt static binaries (compiled on host for the image's target arch).
COPY bin/jcode     /usr/local/bin/jcode
COPY bin/acpdrive  /usr/local/bin/acpdrive
COPY bin/orchclient /usr/local/bin/orchclient
# mockllm is bundled so the container can run fully self-contained in tests; in
# production the MODEL_BASE_URL points at a real (or sidecar) model instead.
COPY bin/mockllm   /usr/local/bin/mockllm

COPY entrypoint.sh /usr/local/bin/entrypoint.sh
COPY install-bundled-skills.sh /usr/local/bin/install-bundled-skills.sh
# turn-hook.sh (F7a / D22): the per-turn diff/commit/bundle logic, extracted
# from entrypoint.sh so acpdrive can exec it directly as --turn-hook in
# multi-turn session mode (RUN_SESSION=1). Lands alongside entrypoint.sh so
# entrypoint.sh's SCRIPT_DIR-relative lookup finds it in the image too.
COPY turn-hook.sh  /usr/local/bin/turn-hook.sh
RUN chmod +x /usr/local/bin/jcode /usr/local/bin/acpdrive /usr/local/bin/orchclient /usr/local/bin/mockllm /usr/local/bin/entrypoint.sh /usr/local/bin/install-bundled-skills.sh /usr/local/bin/turn-hook.sh

COPY skills/ /usr/local/share/jcloud/skills/
RUN groupadd --gid 10001 jcode \
    && useradd --uid 10001 --gid 10001 --create-home --home-dir /home/jcode --shell /bin/bash jcode \
    && mkdir -p /workspace /out /home/jcode/.jcode /run/jcloud \
    && chown -R 10001:10001 /workspace /out /home/jcode /run/jcloud

# Public-comment tasks execute untrusted agent commands. Keep the runtime user
# fixed and non-root; Kubernetes applies the same UID/GID and additional
# capability/seccomp hardening.
ENV HOME=/home/jcode
WORKDIR /workspace
USER 10001:10001

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
