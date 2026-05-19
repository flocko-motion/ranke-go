#!/usr/bin/env bash
# Run scenario 01 from a clean state. cd into the scenario directory
# so main.go can use plain relative paths (../../fixtures/...,
# ./archive/, ./ids.txt) — no repo-root walking, no env vars.
#
# Usage:
#   conformance/scenarios/01_personal_graph/run.sh   # from anywhere

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
rm -rf archive ids.txt
go run .
