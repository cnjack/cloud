# Company release deployment

The company overlay deploys the jcode Cloud stack using `latest` image tags
from the Shanghai Aliyun ACR mirror. Every push to `main` triggers the `images`
workflow, which publishes the same immutable tags to GHCR and Aliyun ACR and
auto-derives the next release version from git tags (bumps the patch number),
builds all four images, pushes `v<version>` + `latest` + `sha-<commit>` tags,
and creates the git tag only after every image has been published.

The workflow requires the repository secrets
`ALIYUN_DOCKER_REGISTRY_USERNAME` and `ALIYUN_DOCKER_REGISTRY_SECRET`. The
namespace ServiceAccounts do not carry an image pull secret, so the three
`jcode-cloud` repositories must either allow anonymous pulls or an ACR pull
secret must be attached before deployment.

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
`orchestrator-secret` and `postgres-secret` from its rendered resources.
Applying the overlay therefore preserves the real, out-of-band Secrets already
installed in the cluster.

The overlay also registers the optional `go-node`, `python`, `rust`, and
`polyglot` Runner Profiles by immutable Aliyun ACR manifest digest. The
`images` workflow publishes and verifies those images. When intentionally
refreshing their toolchains, update all four digests here from one successful
release, render the overlay, and run one real toolchain smoke Pod per profile
before making the new names available to Services.
