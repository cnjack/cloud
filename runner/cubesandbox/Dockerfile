# syntax=docker/dockerfile:1
# A template boots only envd. Orchestrator starts each task after configuring
# confinement, per-run Plugins, model settings, attachments and its workspace.
ARG RUNNER_IMAGE
FROM ${RUNNER_IMAGE}
USER root
ENV HOME=/root
RUN apt-get update \
    && apt-get install -y --no-install-recommends iptables procps \
    && rm -rf /var/lib/apt/lists/*
# The same official envd artifact used in the migration PoC; immutable source.
COPY --from=cube-sandbox-cn.tencentcloudcr.com/cube-sandbox/sandbox-code@sha256:467494c38f3c335e42d590e23d5cbb00dc15c2627c9da4f7e7f3bd9f4ec18c5e /usr/bin/envd /usr/local/bin/cube-envd
RUN echo '4c71212238cea9a2071554b115948f1da817ca6c00376ac2e1cfbb324b89ae9a  /usr/local/bin/cube-envd' | sha256sum -c -
EXPOSE 49983
ENTRYPOINT ["/usr/local/bin/cube-envd", "-port", "49983", "-isnotfc"]
CMD []
