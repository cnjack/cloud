#!/usr/bin/env bash
set -euo pipefail

# The production cluster mounts a persistent volume over $HOME/.jcode. Keep the
# canonical Plugin Skills outside that mount in the image and install a fresh
# managed copy for every task, while preserving unrelated user-created Skills.
source_root="${JCODE_BUNDLED_SKILLS_DIR:-/usr/local/share/jcloud/skills}"
destination_root="${JCODE_SKILLS_DIR:-${HOME:?HOME is required}/.jcode/skills}"

[ -d "$source_root" ] || exit 0
mkdir -p "$destination_root"
chmod 700 "$destination_root"

for skill in github gitlab gitea; do
  source_dir="$source_root/$skill"
  [ -f "$source_dir/SKILL.md" ] || {
    printf '[skills] missing bundled %s/SKILL.md\n' "$skill" >&2
    exit 1
  }
  target_dir="$destination_root/$skill"
  rm -rf "$target_dir"
  mkdir -p "$target_dir"
  cp -a "$source_dir/." "$target_dir/"
done
