# syntax=docker/dockerfile:1
#
# jcloud/mockllm:dev — standalone image for the in-cluster scripted LLM.
#
# This does NOT modify cloud/runner/mockllm source (read-only input). It is a
# separate multi-stage build from source so the resulting Service can be
# reused by orchestrator-scheduled runner Jobs (MODEL_BASE_URL pointed at
# http://mockllm.jcloud.svc.cluster.local:8081/v1) without bundling a copy of
# mockllm inside every runner image.
#
# Build context MUST be cloud/ (see deploy/Makefile: `docker build -f
# deploy/mockllm/Dockerfile cloud/`) so we can COPY the runner/mockllm module.
FROM golang:1.23-alpine AS build
WORKDIR /src/runner/mockllm
COPY runner/mockllm/go.mod ./
RUN go mod download 2>/dev/null || true
COPY runner/mockllm/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags "-s -w" -o /out/mockllm .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mockllm /mockllm
USER nonroot:nonroot
ENV MOCK_ADDR=:8081
ENV MOCK_SCENARIO=write_file
EXPOSE 8081
ENTRYPOINT ["/mockllm"]
