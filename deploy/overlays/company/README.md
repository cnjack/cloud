# Company release deployment

The company overlay deploys an immutable jcode Cloud release. `VERSION` at the
repository root is the release source of truth:

- Git tag: `v<version>`
- orchestrator, runner, console, and mock LLM image tag: `v<version>`
- Console and orchestrator displayed version: `v<version>`
- Mobile package version: `<version>`
- this overlay's orchestrator, runner, and console images: `v<version>`

Before merging a release commit, bump `VERSION` and the package/overlay
references together. Run:

```sh
node scripts/check-release-version.mjs
kubectl kustomize deploy/overlays/company >/dev/null
```

The `images` workflow repeats the version check before publishing. It creates
the Git tag only after every image has been pushed.

After the workflow for `main` succeeds:

```sh
kubectl apply -k deploy/overlays/company
kubectl -n jcode rollout status deploy/postgres --timeout=120s
kubectl -n jcode rollout status deploy/orchestrator --timeout=180s
kubectl -n jcode rollout status deploy/console --timeout=120s
```

Because the manifests use immutable version tags, `kubectl apply` records
exactly which release is running. Do not replace them with `latest`.

The company overlay deliberately deletes the base development
`orchestrator-secret` from its rendered resources. Applying the overlay
therefore preserves the real, out-of-band Secret already installed in the
cluster.
