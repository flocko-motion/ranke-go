#!/usr/bin/env bash
# Fetch the ranke-graph papers into the gitignored papers directory, and stamp the
# commit they came from.
#
# WHY THE STAMP: three gates read this directory — rule-citations and rule-vectors
# read the spec, rqlgate reads rql.schema.json — and a copy fetched days ago looks
# exactly like one fetched a minute ago. Before the stamp there was no way to ask
# which spec a green run was measured against, which is how ranke-ts shipped a gate
# reading six-day-old vectors. The stamp turns "is this current?" into a comparison.
#
# TWO MODES:
#
#   (default)    fetch unconditionally — `make docs`, for when you want the copy
#                replaced whatever the stamp says.
#   --if-moved   fetch only when the remote ref has moved off the stamp, which is
#                one 40-byte `git ls-remote` against a 1.8 MB clone. This is the
#                mode `make verify` runs, so a gate cannot read a stale cache.
#
# FAILING RATHER THAN GUESSING: --if-moved with no reachable remote cannot establish
# freshness, so it fails instead of passing blind on whatever is on disk — the same
# stance the gates take on a missing spec. Working offline is a deliberate ask:
# RANKE_DOCS_OFFLINE=1 keeps the copy you have, and RANKE_SPEC / RANKE_RQL_SCHEMA
# point the gates at a copy of your own.
#
# Usage: scripts/fetch-papers.sh [--if-moved]   (from any directory)
#   RANKE_GRAPH_REPO     the papers repo (the Makefile passes its own default)
#   RANKE_GRAPH_REF      the branch or tag to read
#   PAPERS_DIR           where the copy lands, repo-relative
#   RANKE_DOCS_OFFLINE   non-empty: keep the copy on disk, check nothing
set -euo pipefail

cd "$(dirname "$0")/.."

repo="${RANKE_GRAPH_REPO:-https://github.com/flocko-motion/ranke-graph}"
ref="${RANKE_GRAPH_REF:-main}"
dir="${PAPERS_DIR:-docs/papers}"
stamp="$dir/.ranke-graph-sha"

if_moved=""
case "${1:-}" in
	--if-moved) if_moved=1 ;;
	"") ;;
	*) echo "fetch-papers: unknown argument '$1' — the only one is --if-moved" >&2; exit 2 ;;
esac

# The files the gates open. A stamp matching the remote says nothing about a copy
# half-deleted since, so freshness means the stamp AND these.
gated=("$dir/spec/ranke-spec.typ" "$dir/spec/rql.schema.json")

have_gated() {
	local f
	for f in "${gated[@]}"; do
		[ -f "$f" ] || return 1
	done
}

if [ -n "${RANKE_DOCS_OFFLINE:-}" ]; then
	if have_gated; then
		echo ">> papers: offline, keeping $dir at $(cat "$stamp" 2>/dev/null || echo 'an unstamped copy')"
		exit 0
	fi
	echo "fetch-papers: RANKE_DOCS_OFFLINE is set and $dir holds no spec — there is nothing to keep" >&2
	exit 1
fi

# One ref, so one line: "<sha>\trefs/heads/main". An empty result means the ref is
# gone rather than the network being down, and both are fatal here.
remote_sha=""
if ! remote_sha=$(git ls-remote "$repo" "$ref" 2>/dev/null | awk 'NR==1 {print $1}') || [ -z "$remote_sha" ]; then
	echo "fetch-papers: cannot resolve $ref at $repo — unreachable remote, or the ref is gone" >&2
	echo "  offline: RANKE_DOCS_OFFLINE=1 keeps the copy on disk; RANKE_SPEC / RANKE_RQL_SCHEMA point the gates elsewhere" >&2
	exit 1
fi

if [ -n "$if_moved" ] && have_gated && [ "$(cat "$stamp" 2>/dev/null)" = "$remote_sha" ]; then
	echo ">> papers: $dir is current at ${remote_sha:0:12} ($ref)"
	exit 0
fi

echo ">> fetching ranke-graph papers into $dir/ at ${remote_sha:0:12} ($ref)"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
git clone --depth 1 --branch "$ref" "$repo" "$tmp" >/dev/null 2>&1

# Replaced whole rather than merged: a paper withdrawn upstream must disappear here
# too, or a gate keeps citing what no longer exists.
rm -rf "$dir"
mkdir -p "$dir"
cp -r "$tmp"/[0-9]*-* "$dir"/
for d in shared spec glossary; do
	[ -d "$tmp/$d" ] && cp -r "$tmp/$d" "$dir"/
done
cp "$tmp/LICENSE" "$dir/LICENSE" 2>/dev/null || true

# Last, so an interrupted copy leaves no stamp claiming it is complete.
if ! have_gated; then
	echo "fetch-papers: $ref carries no spec/ranke-spec.typ and spec/rql.schema.json — the gates have nothing to read" >&2
	exit 1
fi
echo "$remote_sha" > "$stamp"
echo ">> pulled $(find "$dir" -name '*.typ' | wc -l | tr -d ' ') paper(s), stamped ${remote_sha:0:12}"
