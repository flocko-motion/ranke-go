#!/usr/bin/env bash
# Compare the rule ids the spec declares against the ids the Go sources cite.
#
# WHAT A GREEN RUN MEANS: every id cited in the code is declared by the spec, and
# every declared rule is either cited or listed in scripts/rule-citations.allow
# with a reason. That is all. Whether a citation is TRUE — whether the rule says
# what the code says it says — is beyond a text comparison, and has already gone
# wrong here: the two diff rules were paraphrased back from memory as their own
# opposite, and the ids were spelled correctly throughout. Read a green run as
# "the ids exist and are accounted for", never as "the citations are correct".
#
# The gate has two directions, and only one can be hard:
#
#   cited but undeclared  — an error, always: a typo'd or invented id points
#                           confidently at nothing, and nothing else surfaces it.
#   declared but uncited  — a ratchet. Many rules belong to another layer, so
#                           failing on "uncited" would buy fake citations, which
#                           is the first error deliberately introduced. Instead
#                           the allowlist may only shrink: an unlisted uncited
#                           rule fails, and so does a listed rule that has since
#                           been cited.
#
# It is the only gate for this: the Go test that checked direction 1 was folded in
# here, since two gates for one job with two definitions of a citation — one
# backticked, one a bare word — is worse than either alone.
#
# It needs the spec, which `make docs` fetches into docs/papers/ (gitignored), so
# it fails on a bare checkout rather than passing blind — as does `make verify`
# through it. RANKE_SPEC points it at a copy of your own, for working offline or
# against a spec not published yet.
#
# Usage: scripts/rule-citations.sh   (from any directory; `make verify` runs it)
#   RANKE_SPEC=<path>  a spec to read instead; a relative path is repo-relative.
set -euo pipefail

cd "$(dirname "$0")/.."

allow="scripts/rule-citations.allow"

# A citation is a rule id in backticks — one definition, both directions. Prose
# reaching for a word of the same shape (a V-SHAPED curve) is not a citation, and
# a bare-word match reads it as one: see spec_citation_canary_test.go.
id_re='[VR]-[A-Z0-9]+'
declared_re="#rule\\(\"$id_re\""
cited_re="\`$id_re\`"

# The spec: RANKE_SPEC, the copy `make docs` fetches, else the local one
# .gitignore reserves at the repo root.
spec=""
for candidate in "${RANKE_SPEC:-}" "docs/papers/spec/ranke-spec.typ" "specification.typ"; do
	if [ -n "$candidate" ] && [ -f "$candidate" ]; then
		spec="$candidate"
		break
	fi
done
if [ -z "$spec" ]; then
	echo "rule citations: no spec found — run 'make docs', or point RANKE_SPEC at a copy" >&2
	exit 1
fi
if [ ! -f "$allow" ]; then
	echo "rule citations: $allow is absent — the uncited-rule list is part of the gate" >&2
	exit 1
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# 1. Declared: the ids of the spec's #rule() declarations. An empty set means the
#    declaration format moved and every comparison below is vacuous.
grep -oE "$declared_re" "$spec" | grep -oE "$id_re" | sort -u > "$work/declared" || true
if [ ! -s "$work/declared" ]; then
	echo "rule citations: no #rule declarations in $spec — the extraction is blind, not passing" >&2
	exit 1
fi

# 2. Cited: ids in this checkout's Go sources. Directories named .worktrees hold
#    sibling agent checkouts at other commits, and a directory with its own go.mod
#    is another module — a match in either is not this module's code, and reading
#    one reports ids that were fixed here as still present.
prune=(-name .git -prune -o -name .worktrees -prune)
while IFS= read -r mod; do
	prune+=(-o -path "$(dirname "$mod")" -prune)
done < <(find . -mindepth 2 -name go.mod -not -path './.worktrees/*' -print)

find . "${prune[@]}" -o -name '*.go' -print0 \
	| xargs -0 --no-run-if-empty grep -hoE "$cited_re" \
	| grep -oE "$id_re" \
	| sort -u > "$work/cited" || true

# 3. Listed: the allowlist, one rule per line as "<id> <reason>". A comment line
#    or a blank one is skipped; an id without a reason is not a listing.
: > "$work/listed"
while read -r id reason; do
	case "$id" in '#'* | '') continue ;; esac
	if [ -z "$reason" ]; then
		echo "rule citations: $allow lists $id with no reason — say why it is uncited" >&2
		exit 1
	fi
	echo "$id" >> "$work/listed"
done < "$allow"
sort -u "$work/listed" -o "$work/listed"

status=0

# 4. Hard gate: an id the code cites that the spec does not declare.
unknown="$(comm -23 "$work/cited" "$work/declared")"
if [ -n "$unknown" ]; then
	echo "rule citations: cited but not declared by the spec —" >&2
	for id in $unknown; do
		echo "  $id" >&2
		grep -rnF --include='*.go' --exclude-dir=.worktrees -- "\`$id\`" . >&2 || true
	done
	status=1
fi

# 5. Ratchet, first half: a declared rule cited nowhere and not listed.
comm -13 "$work/cited" "$work/declared" > "$work/uncited"
unlisted="$(comm -23 "$work/uncited" "$work/listed")"
if [ -n "$unlisted" ]; then
	echo "rule citations: declared, cited nowhere, and not in $allow — cite it, or list it with a reason:" >&2
	for id in $unlisted; do echo "  $id" >&2; done
	status=1
fi

# 6. Ratchet, second half: what keeps the list shrinking. A listed rule that has
#    since been cited must leave the list, or the list rots into a permanent
#    exemption and step 5 goes quiet.
stale="$(comm -12 "$work/listed" "$work/cited")"
if [ -n "$stale" ]; then
	echo "rule citations: cited now, so remove it from $allow:" >&2
	for id in $stale; do echo "  $id" >&2; done
	status=1
fi

# 7. A listed id the spec no longer declares: the listing outlived its rule.
gone="$(comm -23 "$work/listed" "$work/declared")"
if [ -n "$gone" ]; then
	echo "rule citations: listed in $allow but no longer declared by the spec:" >&2
	for id in $gone; do echo "  $id" >&2; done
	status=1
fi

if [ "$status" -eq 0 ]; then
	printf 'rule citations: %s declared, %s cited, %s listed as uncited — ids accounted for (not their correctness)\n' \
		"$(wc -l < "$work/declared" | tr -d ' ')" \
		"$(wc -l < "$work/cited" | tr -d ' ')" \
		"$(wc -l < "$work/listed" | tr -d ' ')"
fi
exit "$status"
