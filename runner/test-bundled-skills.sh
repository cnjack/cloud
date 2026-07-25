#!/usr/bin/env bash
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
test_root="$(mktemp -d)"
trap 'rm -rf "$test_root"' EXIT

mkdir -p "$test_root/source" "$test_root/home/.jcode/skills/custom"
cp -a "$here/skills/." "$test_root/source/"
printf 'preserve\n' >"$test_root/home/.jcode/skills/custom/SKILL.md"
mkdir -p "$test_root/home/.jcode/skills/github"
printf 'stale\n' >"$test_root/home/.jcode/skills/github/stale.txt"

HOME="$test_root/home" \
JCODE_BUNDLED_SKILLS_DIR="$test_root/source" \
bash "$here/install-bundled-skills.sh"

for skill in github gitlab gitea; do
  test -f "$test_root/home/.jcode/skills/$skill/SKILL.md"
done
test ! -e "$test_root/home/.jcode/skills/github/stale.txt"
test -f "$test_root/home/.jcode/skills/custom/SKILL.md"

printf 'bundled skills install: ok\n'
