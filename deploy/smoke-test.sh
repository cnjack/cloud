#!/usr/bin/env bash
# smoke-test.sh — end-to-end smoke test against the live OrbStack cluster.
#
# Assumes `make up` has already succeeded (all Deployments Ready). Talks to
# the orchestrator via a background `kubectl port-forward` on a scratch local
# port (does not collide with `make port-forward`'s :8080, so both can run
# concurrently... though you normally wouldn't need to).
#
# Steps:
#   1. curl /healthz
#   2. POST /api/v1/projects (name only), then POST /api/v1/projects/{id}/services
#      (repo_url = in-cluster git-seed)
#   3. POST /api/v1/services/{id}/runs
#   4. poll GET /api/v1/runs/{id} until terminal, watching the k8s Job appear
#   5. print final status + (if present) artifact/events summary
#   6. clean up: delete the test project (cascades runs) and kill the
#      port-forward
#
# Exit non-zero on any hard failure. Designed to be safe to re-run.

set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$HERE"

NAMESPACE=jcloud
KCTX=orbstack
LOCAL_PORT=18080
CONSOLE_TOKEN="${CONSOLE_TOKEN:-dev-console-token}"
BASE="http://127.0.0.1:${LOCAL_PORT}"

cur="$(kubectl config current-context 2>/dev/null || true)"
if [ "$cur" != "$KCTX" ]; then
  echo "refusing to run: current kubectl context is '$cur', expected '$KCTX'" >&2
  exit 1
fi

PF_PID=""
cleanup() {
  set +e
  if [ -n "$PROJECT_ID" ]; then
    echo "[smoke] cleaning up test project $PROJECT_ID"
    curl -s -o /dev/null -X DELETE "$BASE/api/v1/projects/$PROJECT_ID" \
      -H "Authorization: Bearer $CONSOLE_TOKEN"
  fi
  if [ -n "$PF_PID" ]; then
    kill "$PF_PID" 2>/dev/null || true
    wait "$PF_PID" 2>/dev/null || true
  fi
}
PROJECT_ID=""
trap cleanup EXIT

echo "[smoke] starting port-forward on :$LOCAL_PORT"
kubectl -n "$NAMESPACE" port-forward svc/orchestrator "$LOCAL_PORT:8080" >/tmp/jcloud-smoke-pf.log 2>&1 &
PF_PID=$!

for i in $(seq 1 30); do
  if curl -s -o /dev/null "$BASE/healthz"; then break; fi
  sleep 1
  if [ "$i" = 30 ]; then echo "[smoke] port-forward never became ready" >&2; cat /tmp/jcloud-smoke-pf.log >&2; exit 1; fi
done

echo "[smoke] 1) healthz"
code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/healthz")
[ "$code" = "200" ] || { echo "healthz returned $code" >&2; exit 1; }
echo "    ok (200)"

echo "[smoke] 2) create project, then attach the git-seed repo as a service"
PROJECT_JSON=$(curl -s -X POST "$BASE/api/v1/projects" \
  -H "Authorization: Bearer $CONSOLE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"smoke-demo"}')
echo "    $PROJECT_JSON"
PROJECT_ID=$(echo "$PROJECT_JSON" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])' 2>/dev/null || true)
[ -n "$PROJECT_ID" ] || { echo "no project id in response" >&2; exit 1; }
echo "    project_id=$PROJECT_ID"

SERVICE_JSON=$(curl -s -X POST "$BASE/api/v1/projects/$PROJECT_ID/services" \
  -H "Authorization: Bearer $CONSOLE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"default","repo_url":"git://git.jcloud.svc.cluster.local/seed.git","default_branch":"main"}')
echo "    $SERVICE_JSON"
SERVICE_ID=$(echo "$SERVICE_JSON" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])' 2>/dev/null || true)
[ -n "$SERVICE_ID" ] || { echo "no service id in response" >&2; exit 1; }
echo "    service_id=$SERVICE_ID"

echo "[smoke] 3) create run (service-scoped)"
RUN_JSON=$(curl -s -X POST "$BASE/api/v1/services/$SERVICE_ID/runs" \
  -H "Authorization: Bearer $CONSOLE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"prompt":"Create a file called HELLO.txt with the text hello-from-smoke-test"}')
echo "    $RUN_JSON"
RUN_ID=$(echo "$RUN_JSON" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])' 2>/dev/null || true)
[ -n "$RUN_ID" ] || { echo "no run id in response" >&2; exit 1; }
echo "    run_id=$RUN_ID"

echo "[smoke] 4) waiting for the k8s Job to appear (jcloud-run-$RUN_ID)"
for i in $(seq 1 30); do
  if kubectl -n "$NAMESPACE" get job "jcloud-run-$RUN_ID" >/dev/null 2>&1; then
    echo "    job observed"
    break
  fi
  sleep 1
  if [ "$i" = 30 ]; then echo "    WARNING: job never observed within 30s (reconciler tick / capacity?)" >&2; fi
done

echo "[smoke] 5) polling run status until terminal (timeout 180s)"
STATUS=""
for i in $(seq 1 90); do
  RUN_GET=$(curl -s "$BASE/api/v1/runs/$RUN_ID" -H "Authorization: Bearer $CONSOLE_TOKEN")
  STATUS=$(echo "$RUN_GET" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("status",""))' 2>/dev/null || true)
  echo "    [$i] status=$STATUS"
  case "$STATUS" in
    succeeded|failed|canceled) break ;;
  esac
  sleep 2
done

echo "[smoke] final run object: $RUN_GET"
kubectl -n "$NAMESPACE" get job "jcloud-run-$RUN_ID" -o wide 2>/dev/null || true
kubectl -n "$NAMESPACE" logs "job/jcloud-run-$RUN_ID" --tail=100 2>/dev/null || true

case "$STATUS" in
  succeeded)
    echo "[smoke] run succeeded; checking artifact endpoint"
    ART_CODE=$(curl -s -o /tmp/jcloud-smoke-artifact.json -w '%{http_code}' \
      "$BASE/api/v1/runs/$RUN_ID/artifact" -H "Authorization: Bearer $CONSOLE_TOKEN")
    echo "    GET .../artifact -> $ART_CODE"
    if [ "$ART_CODE" = "200" ]; then
      cat /tmp/jcloud-smoke-artifact.json
    else
      echo "    NOTE: artifact not found (404 expected if runner->orchestrator ingest callback isn't wired yet;"
      echo "    the entrypoint currently prints the diff to stdout/Job logs only -- see Job logs above)."
    fi
    ;;
  failed)
    echo "[smoke] run FAILED -- see failure_reason/failure_message above and Job logs."
    ;;
  *)
    echo "[smoke] run did not reach a terminal state within timeout (last status=$STATUS)" >&2
    exit 1
    ;;
esac

echo "[smoke] done."
