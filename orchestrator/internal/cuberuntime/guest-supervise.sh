#!/bin/bash
set -uo pipefail
control=/run/jcloud/control
exec 9>"$control/start.lock"
flock -n 9 || exit 0
[ ! -f "$control/started" ] || exit 0
[ -f "$control/prepared" ] || exit 65
: > "$control/started"
. "$control/env.sh"
unset CUBE_RESTORE_URL RESTORE_ARCHIVE_URL
if [ "${CUBE_PLUGIN_ENABLED:-0}" = 1 ]; then
  "$control/plugin-runtime" sync-credentials --providers "$CUBE_PLUGIN_PROVIDERS" --reader-gid 10001 --dir /run/jcloud/plugins --stop-file /run/jcloud/lifecycle/runner-finished &
  sync_pid=$!
fi
echo running > "$control/state"
# A separate process group allows cancel and timeout to terminate descendants.
setsid timeout --signal=TERM --kill-after=15 "${CUBE_JOB_TIMEOUT}s" \
  setpriv --reuid=10001 --regid=10001 --clear-groups --bounding-set=-all \
  --inh-caps=-all --ambient-caps=-all --no-new-privs \
  /bin/bash --noprofile --norc -c '
    set -eu
    . /run/jcloud/config/runner-env.sh
    export PATH="/run/jcloud/runtime/bin:/usr/local/go/bin:/home/jcode/go/bin:/usr/local/cargo/bin:$PATH"
    # envd process sessions do not inherit all OCI image environment entries.
    # Keep compiler installations read-only and caches in the runner HOME.
    if [ -d /usr/local/rustup ]; then
      export RUSTUP_HOME="${RUSTUP_HOME:-/usr/local/rustup}"
      export CARGO_HOME="${CARGO_HOME:-$HOME/.cargo}"
      mkdir -p "$CARGO_HOME"
    fi
    if [ -d /usr/local/go ]; then
      export GOPATH="${GOPATH:-$HOME/go}"
    fi
    cd /workspace
    exec /usr/local/bin/entrypoint.sh
  ' &
child=$!
echo "$child" > "$control/child-pid"
wait "$child"
code=$?
if [ -f "$control/deadline-exceeded" ]; then code=124; fi
# Do not checkpoint while daemonized agent children can still mutate files.
pkill -KILL -u 10001 2>/dev/null || true
echo stopping > "$control/state"
# SIGKILL delivery is asynchronous. Wait until no process can still write the
# workspace; zombies have already exited and cannot mutate it.
while ps -eo uid=,stat= | awk '$1 == 10001 && $2 !~ /^Z/ { live=1 } END { exit !live }'; do
  pkill -KILL -u 10001 2>/dev/null || true
  sleep 0.05
done
if [ -n "${sync_pid:-}" ]; then kill "$sync_pid" 2>/dev/null || true; wait "$sync_pid" 2>/dev/null || true; fi
echo "$code" > "$control/exit-code"
echo exited > "$control/state"
