#!/usr/bin/env bash
# conformance/run.sh — run one or all scenarios.
#
#   ./run.sh       run every scenario in scenarios/
#   ./run.sh 1     run scenarios/01_*
#   ./run.sh 02    run scenarios/02_*
#
# The numeric argument is zero-padded to two digits, so "1" and "01"
# resolve to the same scenario.

set -euo pipefail
cd "$(dirname "$0")"

run_scenario() {
  local dir="$1"
  echo "--- ${dir%/} ---"
  (cd "$dir" && rm -rf archive ids.txt && go run .)
}

if [[ $# -eq 0 ]]; then
  for d in scenarios/*/; do
    run_scenario "$d"
  done
  exit 0
fi

prefix=$(printf "%02d" "$1")
matches=(scenarios/${prefix}_*/)
if [[ ! -d "${matches[0]}" ]]; then
  echo "run.sh: no scenario matching ${prefix}_*" >&2
  exit 1
fi
run_scenario "${matches[0]}"
