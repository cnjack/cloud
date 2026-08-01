# syntax=docker/dockerfile:1.7
#
# Optional, administrator-built Runner environments. The final image is always
# the reviewed jcloud Runner plus a fixed toolchain set; task input never chooses
# an OCI reference directly. Pin BASE_IMAGE and every *_SOURCE_IMAGE to digests
# in release automation, then put the resulting output digests in
# RUNNER_PROFILES_JSON.

ARG BASE_IMAGE=jcloud/runner:dev
ARG GO_SOURCE_IMAGE=golang:1.24-bookworm
ARG NODE_SOURCE_IMAGE=node:22-bookworm-slim
ARG PYTHON_SOURCE_IMAGE=python:3.13-bookworm
ARG RUST_SOURCE_IMAGE=rust:1.85-bookworm

FROM ${GO_SOURCE_IMAGE} AS go-source
FROM ${NODE_SOURCE_IMAGE} AS node-source
FROM ${PYTHON_SOURCE_IMAGE} AS python-source
FROM ${RUST_SOURCE_IMAGE} AS rust-source
FROM ${BASE_IMAGE} AS runner-base

FROM runner-base AS go-node
USER root
RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential pkg-config \
    && rm -rf /var/lib/apt/lists/*
COPY --from=go-source /usr/local/go /usr/local/go
COPY --from=node-source /usr/local/bin/node /usr/local/bin/node
COPY --from=node-source /usr/local/lib/node_modules /usr/local/lib/node_modules
ENV PATH=/usr/local/go/bin:/home/jcode/go/bin:${PATH} \
    GOPATH=/home/jcode/go
RUN mkdir -p /home/jcode/go /home/jcode/.cache/go-build /home/jcode/.npm \
    && ln -sf ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -sf ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx \
    && ln -sf ../lib/node_modules/corepack/dist/corepack.js /usr/local/bin/corepack \
    && chown -R 10001:10001 /home/jcode/go /home/jcode/.cache /home/jcode/.npm
USER 10001:10001

FROM runner-base AS python
USER root
RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential pkg-config \
    && rm -rf /var/lib/apt/lists/*
COPY --from=python-source /usr/local /usr/local
RUN mkdir -p /home/jcode/.cache/pip \
    && chown -R 10001:10001 /home/jcode/.cache
USER 10001:10001

FROM runner-base AS rust
USER root
RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential pkg-config \
    && rm -rf /var/lib/apt/lists/*
COPY --from=rust-source /usr/local/cargo /usr/local/cargo
COPY --from=rust-source /usr/local/rustup /usr/local/rustup
ENV CARGO_HOME=/usr/local/cargo \
    RUSTUP_HOME=/usr/local/rustup \
    PATH=/usr/local/cargo/bin:${PATH}
RUN mkdir -p /home/jcode/.cargo/registry /home/jcode/.cargo/git \
    && chown -R 10001:10001 /home/jcode/.cargo
USER 10001:10001

FROM runner-base AS polyglot
USER root
RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential pkg-config \
    && rm -rf /var/lib/apt/lists/*
COPY --from=go-source /usr/local/go /usr/local/go
COPY --from=node-source /usr/local/bin/node /usr/local/bin/node
COPY --from=node-source /usr/local/lib/node_modules /usr/local/lib/node_modules
COPY --from=python-source /usr/local /usr/local
COPY --from=rust-source /usr/local/cargo /usr/local/cargo
COPY --from=rust-source /usr/local/rustup /usr/local/rustup
ENV GOPATH=/home/jcode/go \
    CARGO_HOME=/usr/local/cargo \
    RUSTUP_HOME=/usr/local/rustup \
    PATH=/usr/local/go/bin:/home/jcode/go/bin:/usr/local/cargo/bin:${PATH}
RUN mkdir -p /home/jcode/go /home/jcode/.cache/go-build /home/jcode/.cache/pip \
      /home/jcode/.npm /home/jcode/.cargo/registry /home/jcode/.cargo/git \
    && ln -sf ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -sf ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx \
    && ln -sf ../lib/node_modules/corepack/dist/corepack.js /usr/local/bin/corepack \
    && chown -R 10001:10001 /home/jcode/go /home/jcode/.cache /home/jcode/.npm /home/jcode/.cargo
USER 10001:10001
