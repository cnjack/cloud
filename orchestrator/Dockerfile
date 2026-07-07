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
RUN apk add --no-cache git ca-certificates \
    && adduser -D -u 65532 -h /home/nonroot nonroot
COPY --from=build /out/orchestrator /orchestrator
# A stable, writable HOME so `git` (used for source bundles / branch pushes) has
# a config dir and never falls back to an unwritable path.
ENV HOME=/home/nonroot
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/orchestrator"]
