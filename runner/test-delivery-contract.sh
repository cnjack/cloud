#!/usr/bin/env bash
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=delivery-contract.sh
. "$HERE/delivery-contract.sh"

prompt="$(build_delivery_prompt "fix the race" draft_pr 1 trunk)"
grep -q "platform managed" <<<"$prompt"
grep -q "Do not run git push" <<<"$prompt"
grep -q "runner intentionally has no Git-provider credential" <<<"$prompt"
grep -q "git_mode=draft_pr; session=1; base_branch=trunk" <<<"$prompt"
grep -q "User task:" <<<"$prompt"
grep -q "fix the race" <<<"$prompt"

printf '\033[32mPASS\033[0m delivery contract is explicit and preserves the user task\n'
