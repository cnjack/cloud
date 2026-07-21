# Browser-driven e2e rig

Real-browser e2e for flows the curl-based journeys can't cover (the console
`/device` authorize page today). Uses Playwright against the local rig:

- orbstack orchestrator behind a port-forward on `:18080`
- console vite dev on `:5173` with `VITE_API_PROXY_TARGET=http://localhost:18080`

## Setup

```sh
kubectl --context orbstack -n jcloud port-forward svc/orchestrator 18080:8080 &
cd console && VITE_API_PROXY_TARGET=http://localhost:18080 pnpm dev &
cd e2e/browser && npm install   # playwright (browsers already cached on dev machines)
```

## Run

```sh
# headed (watch the browser) or headless
node browser-device-auth.mjs
node browser-device-auth.mjs --headless
```

Env overrides: `BASE` (orchestrator), `WEB` (console), `JCODE_BIN`, `SHOTS_DIR`.

## Gotchas encoded here (read before writing new rigs)

- **psql through `kubectl exec` eats quotes.** Dollar-quote SQL string
  literals (`$$...$$`) — single quotes terminate the nested `sh -c '...'`.
- **`psql -t -A` still appends the command tag** to `INSERT ... RETURNING`
  output (`id\nINSERT 0 1`). Take the first line.
- **The seeded session is a cookie, not a bearer.** Set `jcloud_session` on
  the *vite origin* (`localhost:5173`); the SPA proxies `/api` with cookies.
- **`jcode login` pops a browser** unless `JCODE_NO_BROWSER=1` — set it in
  rigs and drive your own Playwright browser instead.
