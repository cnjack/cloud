# Runner runtime profiles

Runtime profiles keep language toolchains outside the minimal default Runner.
They are cluster-admin artifacts: Services select an allowlisted profile name,
while the Orchestrator resolves that name to a configured OCI image.

Build the base Runner first, then from the repository root run:

```bash
runner/profiles/build.sh
runner/profiles/verify.sh
```

The named Dockerfile targets are `go-node`, `python`, `rust`, and `polyglot`.
For a release, pin `BASE_IMAGE`, `GO_SOURCE_IMAGE`, `NODE_SOURCE_IMAGE`,
`PYTHON_SOURCE_IMAGE`, and `RUST_SOURCE_IMAGE` to immutable digests, scan/sign
the outputs, push them, and configure only output digests:

```json
{
  "go-node": "registry.example/jcode/runner-go-node@sha256:…",
  "python": "registry.example/jcode/runner-python@sha256:…",
  "rust": "registry.example/jcode/runner-rust@sha256:…",
  "polyglot": "registry.example/jcode/runner-polyglot@sha256:…"
}
```

Set that object as `RUNNER_PROFILES_JSON`. `default` is reserved and always
maps to `RUNNER_IMAGE`. Unknown names fail a Run visibly before scheduling; a
task, Card, webhook, or agent prompt cannot supply an image reference.
