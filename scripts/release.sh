#!/usr/bin/env bash
# Dispatches `make release` (real release) to ranke-graph's shared
# scripts/release-cycle.sh — fetched to $(RELEASE_CYCLER), see the Makefile —
# and keeps the one thing genuinely specific to this repo: `make release pre
# <bump>`, a PRERELEASE that tags the branch without merging.
#
# `make release pre <bump>` cuts a PRERELEASE instead: it pushes the branch and tags
# it vX.Y.Z-rc.N, merging nothing. That gives a version the module proxy resolves —
# which is what regenerating the cross-implementation vectors in ranke-graph needs —
# without a tag on the default branch. It exists because those vectors are generated
# BY a released ranke-go and checked BY its own suite, so an encoder change cannot go
# green until a version of it exists: tag an rc, regenerate, publish, and the real
# release then merges a branch CI has actually passed, which is what ci.yml promises.
#
# An rc tag can dangle. It points at a branch commit, and a later squash or rebase
# leaves it outside the default branch's history. That is fine for scaffolding and
# wrong for anything archival: regenerate from the real tag once it exists.
#
# WHY NOT A release-cycle.sh HOOK: prerelease is a different shape of command
# entirely (no merge, no PR, a `pre` sub-word before the bump), not a variation
# release-cycle.sh's fixed sequence can express through release-next-version.sh
# or release-pretag.sh. Forcing it into that shape would cost more than the
# duplication of the tag/push/wait tail this file still carries below.
set -euo pipefail

if [ "${1:-}" != "pre" ]; then
	exec "${RELEASE_CYCLER:-bin/release-cycle.sh}" "$@"
fi
shift

bump="${1:-}"
case "$bump" in
	major | breaking) bump=major ;; # incompatible change
	minor | feature)  bump=minor ;; # backwards-compatible feature
	patch | fix)      bump=patch ;; # backwards-compatible fix
	*)
		echo "usage: make release pre <major|breaking | minor|feature | patch|fix>" >&2
		exit 1
		;;
esac
shift || true

# A word left over means an extra argument followed the bump, which taking only
# the first would silently drop.
if [ "$#" -gt 0 ]; then
	echo "unexpected argument '$1' — usage: make release pre <bump>" >&2
	exit 1
fi

git fetch --tags --force origin >/dev/null 2>&1 || true
default="$(git symbolic-ref --quiet --short refs/remotes/origin/HEAD 2>/dev/null | sed 's@^origin/@@')"
default="${default:-main}"
start="$(git rev-parse --abbrev-ref HEAD)"

# The point of a prerelease is a resolvable version WITHOUT touching the default
# branch, so the branch is pushed and nothing is merged. From the default branch
# there is nothing to be a candidate for — release instead.
if [ "$start" = "$default" ]; then
	echo "on '$default' — a prerelease is cut from a branch; release from here instead" >&2
	exit 1
fi

# Always end back on the branch we started on — never park on the default branch.
trap 'git checkout --quiet "$start" 2>/dev/null || true' EXIT

echo "pushing '$start'…"
git push --force-with-lease -u origin "$start"
target="HEAD"

# Bump from the latest RELEASE tag (ignore non-semver / prerelease tags), then
# number past whatever candidates that version already has, so a second attempt
# does not collide with the first.
# `|| true`: on the first release there are no tags, so grep matches nothing and
# exits 1; under `set -o pipefail` that aborts the assignment before the
# `:-v0.0.0` fallback can apply. Swallow it so the fallback works.
latest="$(git tag --list 'v*' --sort=-v:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -n1 || true)"
latest="${latest:-v0.0.0}"
IFS=. read -r maj min pat <<<"${latest#v}"
case "$bump" in
	major) maj=$((maj + 1)); min=0; pat=0 ;;
	minor) min=$((min + 1)); pat=0 ;;
	patch) pat=$((pat + 1)) ;;
esac
next="v${maj}.${min}.${pat}"
n=1
while git rev-parse --quiet --verify "refs/tags/${next}-rc.${n}" >/dev/null; do
	n=$((n + 1))
done
next="${next}-rc.${n}"

echo "tagging ${next} on '${start}' — no merge, nothing on ${default}"
git tag -a "$next" "$target" -m "release $next"
git push origin "$next"

# Wait for the tag-triggered release workflow, so a failed build or publish
# surfaces here instead of silently. Match the run by the tagged commit's SHA
# (reliable for tag pushes, where headBranch is unset).
if command -v gh >/dev/null; then
	sha="$(git rev-parse "$target")"
	echo "waiting for the release workflow…"
	run_id=""
	for _ in $(seq 1 30); do
		run_id="$(gh run list --workflow=release.yml --json databaseId,headSha \
			--jq "map(select(.headSha == \"$sha\"))[0].databaseId" 2>/dev/null || true)"
		[ -n "$run_id" ] && [ "$run_id" != "null" ] && break
		sleep 2
	done
	if [ -z "$run_id" ] || [ "$run_id" = "null" ]; then
		echo "  tag pushed, but no release run appeared — check: gh run list --workflow=release.yml" >&2
	elif gh run watch "$run_id" --exit-status; then
		echo "release ${next} published ✓ (back on '$start')"
		exit 0
	else
		echo "release ${next} FAILED in CI — see: gh run view $run_id --log-failed" >&2
		exit 1
	fi
fi
echo "pushed ${next} — the release workflow triggers on the tag. Back on '$start'."
