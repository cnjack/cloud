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

# --- runtime stage: distroless, non-root ---
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/orchestrator /orchestrator
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/orchestrator"]
