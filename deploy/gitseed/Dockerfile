# syntax=docker/dockerfile:1
#
# jcloud/gitseed:dev — tiny in-cluster git:// server for e2e seed repos.
#
# `alpine/git` (used for the seed initContainer) does NOT bundle git-daemon —
# Alpine splits it into a separate `git-daemon` apk package. Rather than `apk
# add` it at Pod startup (network dependency + non-reproducible), we bake it
# into a dedicated image built once, locally, at `make build`.
FROM alpine:3.20
RUN apk add --no-cache git git-daemon
ENTRYPOINT ["git"]
