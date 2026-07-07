#!/usr/bin/env bash
# e2e.sh — repeatable end-to-end suite for the jcode Cloud Agent MVP, run
# against the LIVE local OrbStack k8s cluster (context `orbstack`, ns `jcloud`).
#
# It exercises PRD (cloud/docs/10-prd.md §5) journeys J1/J2/J3 over the
# orchestrator's real HTTP/SSE API (cloud/docs/11-api.md), asserting each PRD
# step (Jx-Sn) and printing a per-assertion PASS/FAIL table with the PRD ids.
# Non-zero exit if any assertion FAILs. SKIPs (documented degradations) do not
# fail the run.
#
# What it does:
#   1. guard the kubectl context (orbstack only)
#   2. verify the stack is Ready (orchestrator/postgres/mockllm/git-seed)
#   3. read CONSOLE_TOKEN from the orchestrator-secret
#   4. start a background port-forward to the orchestrator on a scratch port
#   5. run J1, J2, J3 (each maps assertions to PRD step ids)
#   6. informational SSE latency spot-check (PRD §8; never gates)
#   7. print the assertion table + totals
#   8. tear down: delete every test project (cascades runs/events/artifacts),
#      reap leftover runner Jobs, kill the port-forward. The stack stays up.
#
# Prereqerequisites: OrbStack running with `make -C cloud/deploy up` already
# applied (the integrated images built + rolled out). Tools: bash, curl, jq,
# kubectl. See README.md.
#
# Usage:
#   ./e2e.sh                 # full suite (J1-J3; J4 draft-PR runs if Gitea is up)
#   ONLY=j1 ./e2e.sh         # a single journey (j1|j2|j3|j4)
#   LOCAL_PORT=18099 ./e2e.sh

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

KCTX="${KCTX:-orbstack}"
NAMESPACE="${NAMESPACE:-jcloud}"
LOCAL_PORT="${LOCAL_PORT:-18080}"
export KCTX NAMESPACE
export BASE="http://127.0.0.1:${LOCAL_PORT}"

# --- 0. context guard (orbstack only; mirrors deploy/Makefile _ctx-guard) ---
cur="$(kubectl config current-context 2>/dev/null || true)"
if [ "$cur" != "$KCTX" ]; then
  echo "refusing to run: current kubectl context is '$cur', expected '$KCTX'" >&2
  echo "run: kubectl config use-context $KCTX" >&2
  exit 2
fi

# Fresh results/cleanup files for this invocation (exported so sourced journeys
# and lib helpers share them).
RESULTS_FILE="$(mktemp -t jcloud-e2e-results.XXXXXX)"
CLEANUP_FILE="$(mktemp -t jcloud-e2e-cleanup.XXXXXX)"
export RESULTS_FILE CLEANUP_FILE

# shellcheck source=lib.sh
source "$HERE/lib.sh"

PF_PID=""
teardown() {
  set +e
  cleanup_projects
  if [ -n "$PF_PID" ]; then
    kill "$PF_PID" 2>/dev/null
    wait "$PF_PID" 2>/dev/null
  fi
  rm -f "$RESULTS_FILE" "$CLEANUP_FILE" 2>/dev/null
}
trap teardown EXIT

# --- 1. stack readiness -----------------------------------------------------
section "Preflight: verify the jcloud stack is Ready (context=$KCTX ns=$NAMESPACE)"
for dep in postgres mockllm git-seed orchestrator; do
  if kubectl --context "$KCTX" -n "$NAMESPACE" rollout status "deploy/$dep" --timeout=60s >/dev/null 2>&1; then
    info "  deploy/$dep Ready"
  else
    echo "preflight FAILED: deploy/$dep not Ready. Run: make -C ../deploy up" >&2
    exit 3
  fi
done

# migration 0002 sanity: run_events must have the server-seq columns.
PG_POD="$(kubectl --context "$KCTX" -n "$NAMESPACE" get pod -l app.kubernetes.io/name=postgres -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)"
if [ -n "$PG_POD" ]; then
  cols="$(kubectl --context "$KCTX" -n "$NAMESPACE" exec "$PG_POD" -- \
    psql -U jcloud -d jcloud -tAc \
    "SELECT string_agg(column_name,',') FROM information_schema.columns WHERE table_name='run_events'" 2>/dev/null)"
  case "$cols" in
    *source*client_seq*|*client_seq*source*) info "  migration 0002 applied (run_events has source, client_seq)";;
    *) echo "preflight WARNING: run_events missing source/client_seq — migration 0002 may not be applied (cols: $cols)" >&2;;
  esac
fi

# --- 2. token from the k8s secret ------------------------------------------
TOKEN="$(kubectl --context "$KCTX" -n "$NAMESPACE" get secret orchestrator-secret \
  -o jsonpath='{.data.CONSOLE_TOKEN}' 2>/dev/null | base64 -d)"
if [ -z "$TOKEN" ]; then
  echo "could not read CONSOLE_TOKEN from secret/orchestrator-secret" >&2
  exit 4
fi
export TOKEN
info "console token loaded from secret/orchestrator-secret (${#TOKEN} chars)"

# --- 3. port-forward --------------------------------------------------------
section "Port-forward orchestrator :8080 -> localhost:$LOCAL_PORT"
kubectl --context "$KCTX" -n "$NAMESPACE" port-forward svc/orchestrator "$LOCAL_PORT:8080" \
  >/tmp/jcloud-e2e-pf.log 2>&1 &
PF_PID=$!
# wait for /healthz
ready="false"
for _ in $(seq 1 30); do
  if curl -sS -o /dev/null "$BASE/healthz" 2>/dev/null; then ready="true"; break; fi
  sleep 1
done
if [ "$ready" != "true" ]; then
  echo "port-forward never became healthy; log:" >&2; cat /tmp/jcloud-e2e-pf.log >&2; exit 5
fi
info "orchestrator reachable at $BASE (healthz 200)"

# --- 4. run journeys --------------------------------------------------------
ONLY="${ONLY:-all}"
J1_RUN_ID=""

# shellcheck source=j1.sh
source "$HERE/j1.sh"
# shellcheck source=j2.sh
source "$HERE/j2.sh"
# shellcheck source=j3.sh
source "$HERE/j3.sh"
# shellcheck source=j4-gitea.sh
source "$HERE/j4-gitea.sh"

case "$ONLY" in
  j1)  j1_run ;;
  j2)  j2_run ;;
  j3)  j3_run ;;
  j4)  j4_run ;;
  all) j1_run; j2_run; j3_run; j4_run ;;
  *)   echo "unknown ONLY=$ONLY (want j1|j2|j3|j4|all)" >&2; exit 6 ;;
esac

# --- 5. latency spot-check (informational) ----------------------------------
# Use a dedicated throwaway project pointed at the good seed repo so the sample
# run actually streams agent events (a failing run would only stream statuses).
LAT_PC="$(create_project "e2e-latency" "$SEED_REPO")"
LAT_PID="${LAT_PC%%$'\t'*}"
if [ -n "$LAT_PID" ]; then
  register_project "$LAT_PID"
  latency_spotcheck "$LAT_PID"
else
  info "skipping latency spot-check (could not create latency project)"
fi

# --- 6. summary + exit code -------------------------------------------------
if print_summary; then
  section "RESULT: all assertions passed (SKIPs are documented degradations)"
  RC=0
else
  section "RESULT: one or more assertions FAILED"
  RC=1
fi

# teardown runs via trap; propagate RC.
exit "$RC"
