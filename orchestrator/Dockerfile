# syntax=docker/dockerfile:1

# --- build stage: static binary, no CGO ---
FROM golang:1.25-alpine AS build
WORKDIR /src

# Cache modules first.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
# CGO disabled -> fully static binary that runs in a scratch/distroless image.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-s -w -X github.com/cnjack/jcloud/internal/version.Version=${VERSION} -X github.com/cnjack/jcloud/internal/version.Commit=${COMMIT}" \
    -o /out/orchestrator ./cmd/orchestrator
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-s -w" \
    -o /out/plugin-runtime ./cmd/plugin-runtime

# --- runtime stage: alpine, non-root ---
#
# Tradeoff (M3): the control plane now runs `git` on the user's behalf — it
# bare-clones a service repo to build a source bundle (GET .../source) and
# fetches+pushes a runner-produced bundle to open a draft PR. That needs the git
# CLI at runtime, which the previous `distroless/static` image cannot provide
# (no shell, no package manager, no git binary). We switch to a minimal alpine
# base with only `git` + `ca-certificates` added, and STILL run as a non-root
# user (uid 65532) — the security posture (non-root, minimal packages, static Go
# binary) is preserved; we trade the distroless "no userland at all" property for
# the single git dependency the bundle/push path requires.
FROM alpine:3.20
ARG TARGETARCH
ARG GH_VERSION=2.94.0
ARG GLAB_VERSION=1.109.0
ARG TEA_VERSION=0.10.1

RUN apk add --no-cache git ca-certificates curl \
    && adduser -D -u 65532 -h /home/nonroot nonroot

# Canonical Provider runtime bundle. These assets belong to the Orchestrator
# release and are copied into a run-scoped tmpfs by the plugin-runtime init
# container. They are never present in the generic Runner image.
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
    mkdir -p /opt/jcloud/plugin-runtime/bin; \
    curl -fsSL -o /tmp/gh.tgz "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_${TARGETARCH}.tar.gz"; \
    echo "${gh_sha}  /tmp/gh.tgz" | sha256sum -c -; \
    tar -xzf /tmp/gh.tgz -C /tmp; \
    install -m 0755 "/tmp/gh_${GH_VERSION}_linux_${TARGETARCH}/bin/gh" /opt/jcloud/plugin-runtime/bin/gh; \
    curl -fsSL -o /tmp/glab.tgz "https://gitlab.com/gitlab-org/cli/-/releases/v${GLAB_VERSION}/downloads/glab_${GLAB_VERSION}_linux_${TARGETARCH}.tar.gz"; \
    echo "${glab_sha}  /tmp/glab.tgz" | sha256sum -c -; \
    tar -xzf /tmp/glab.tgz -C /tmp; \
    install -m 0755 /tmp/bin/glab /opt/jcloud/plugin-runtime/bin/glab; \
    curl -fsSL -o /tmp/tea "https://gitea.com/gitea/tea/releases/download/v${TEA_VERSION}/tea-${TEA_VERSION}-linux-${TARGETARCH}"; \
    echo "${tea_sha}  /tmp/tea" | sha256sum -c -; \
    install -m 0755 /tmp/tea /opt/jcloud/plugin-runtime/bin/tea; \
    rm -rf /tmp/gh.tgz /tmp/glab.tgz /tmp/tea "/tmp/gh_${GH_VERSION}_linux_${TARGETARCH}" /tmp/bin

COPY --from=build /out/orchestrator /orchestrator
COPY --from=build /out/plugin-runtime /plugin-runtime
COPY runtime/skills/ /opt/jcloud/plugin-runtime/skills/
# A stable, writable HOME so `git` (used for source bundles / branch pushes) has
# a config dir and never falls back to an unwritable path.
ENV HOME=/home/nonroot
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/orchestrator"]
