# Company release deployment

The company overlay deploys the jcode Cloud stack using `latest` image tags
from ghcr.io. Every push to `main` triggers the `images` workflow, which
auto-derives the next release version from git tags (bumps the patch number),
builds all four images, pushes `v<version>` + `latest` + `sha-<commit>` tags,
and creates the git tag only after every image has been published.

There is no `VERSION` file in the repo — the version is fully derived from
the latest `v<X.Y.Z>` git tag at build time.

After the workflow for `main` succeeds:

```sh
kubectl apply -k deploy/overlays/company
kubectl -n jcode rollout status deploy/postgres --timeout=120s
kubectl -n jcode rollout status deploy/orchestrator --timeout=180s
kubectl -n jcode rollout status deploy/console --timeout=120s
```

The company overlay deliberately deletes the base development
`orchestrator-secret` from its rendered resources. Applying the overlay
therefore preserves the real, out-of-band Secret already installed in the
cluster.
