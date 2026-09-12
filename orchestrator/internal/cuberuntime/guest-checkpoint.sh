#!/bin/bash
set -euo pipefail
control=/run/jcloud/control
exec 8>"$control/checkpoint.lock"
flock -n 8 || exit 0
[ "$(cat "$control/state")" != complete ] || exit 0
. "$control/checkpoint.env"
# Keep trusted network configuration for storage access without exposing the
# checkpoint URLs in the runner's environment or persistent HOME.
. "$control/env.sh"
scratch=/var/lib/jcloud-checkpoint
mkdir -p "$scratch"
chmod 700 "$scratch"
echo checkpointing > "$control/state"
trap 'echo checkpoint_failed > "$control/state"' EXIT
for path in /workspace /home/jcode/.jcode; do
  [ -d "$path" ] && [ ! -L "$path" ] || exit 65
done
tar -C / --exclude='home/jcode/.jcode/config.json' \
  --exclude='home/jcode/.jcode/mcp.json' \
  --exclude='home/jcode/.jcode/skills/github' \
  --exclude='home/jcode/.jcode/skills/gitlab' \
  --exclude='home/jcode/.jcode/skills/gitea' \
  -czf "$scratch/checkpoint.tgz" workspace home/jcode/.jcode
expected=$(sha256sum "$scratch/checkpoint.tgz" | cut -d' ' -f1)
curl --fail --silent --show-error --max-time 900 --upload-file "$scratch/checkpoint.tgz" "$UPLOAD_URL"
actual=$(curl --fail --silent --show-error --max-time 900 "$VERIFY_URL" | sha256sum | cut -d' ' -f1)
[ "$actual" = "$expected" ]
rm -f "$scratch/checkpoint.tgz" "$control/checkpoint.env"
trap - EXIT
echo complete > "$control/state"
