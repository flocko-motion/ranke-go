#!/usr/bin/env bash
# Compare the ADT rules the spec declares against the rules the published reference
# vectors actually exercise.
#
# WHAT A GREEN RUN MEANS: every rule id a case names is declared by the spec, and
# every declared V-* rule either has a case that BREAKS it or is listed in
# scripts/rule-vectors.allow with a reason. That is all.
#
# What it does NOT mean, and the limit worth knowing: coverage is counted per RULE,
# not per clause. `V-CONTENT` states three things — content_size and encoding
# together, H(c) = content_hash, and the two slots being exclusive — and one case
# against any of them marks the rule covered. That is exactly how the both-slots gap
# survived 0.19.1 while a content_hash case sat in the set. A gate cannot split a
# rule the spec declares as one, so a NEW rule is what this catches; a new clause on
# an old rule still needs a reader.
#
# Scope is V-* only. The spec calls those FORCED — portable to any implementation of
# the ADT — while R-* are RankeDB's own FREE choices, which another conformant
# implementation may decide differently. A cross-implementation vector set that
# demanded R-* would be publishing policy as conformance.
#
# Coverage is read from the generated manifest, not from the source, so what is gated
# is the artifact a downstream implementation would actually receive.
#
# It needs the spec, which `make docs` fetches into docs/papers/ (gitignored), so it
# fails on a bare checkout rather than passing blind — as does `make verify` through
# it. RANKE_SPEC points it at a copy of your own.
#
# Usage: scripts/rule-vectors.sh   (from any directory; `make verify` runs it)
#   RANKE_SPEC=<path>  a spec to read instead; a relative path is repo-relative.
set -euo pipefail

cd "$(dirname "$0")/.."

allow="scripts/rule-vectors.allow"

id_re='V-[A-Z0-9]+'
declared_re="#rule\\(\"$id_re\""

spec=""
for candidate in "${RANKE_SPEC:-}" "docs/papers/spec/ranke-spec.typ" "specification.typ"; do
	if [ -n "$candidate" ] && [ -f "$candidate" ]; then
		spec="$candidate"
		break
	fi
done
if [ -z "$spec" ]; then
	echo "rule vectors: no spec found — run 'make docs', or point RANKE_SPEC at a copy" >&2
	exit 1
fi
if [ ! -f "$allow" ]; then
	echo "rule vectors: $allow is absent — the uncovered-rule list is part of the gate" >&2
	exit 1
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# 1. Declared: the ADT rules. An empty set means the declaration format moved and
#    every comparison below is vacuous.
grep -oE "$declared_re" "$spec" | grep -oE "$id_re" | sort -u > "$work/declared" || true
if [ ! -s "$work/declared" ]; then
	echo "rule vectors: no V-* #rule declarations in $spec — the extraction is blind, not passing" >&2
	exit 1
fi

# 2. Covered: the rules the generated set's rejected cases break. Generating it is
#    what makes this a gate on the artifact rather than on the code that writes it.
if ! go run ./cmd/vectors -out "$work/set" > "$work/gen.log" 2>&1; then
	echo "rule vectors: the generator failed, so there is no set to check —" >&2
	cat "$work/gen.log" >&2
	exit 1
fi
jq -r '[.claims[], .content[]] | map(select(.verify == false)) | .[].violates // [] | .[]' \
	"$work/set/manifest.json" | sort -u > "$work/covered"
if [ ! -s "$work/covered" ]; then
	echo "rule vectors: no case names a rule it breaks — the manifest's violates field is empty" >&2
	exit 1
fi

# 3. Listed: the allowlist, one rule per line as "<id> <reason>".
: > "$work/listed"
while read -r id reason; do
	case "$id" in '#'* | '') continue ;; esac
	if [ -z "$reason" ]; then
		echo "rule vectors: $allow lists $id with no reason — say why it has no case" >&2
		exit 1
	fi
	echo "$id" >> "$work/listed"
done < "$allow"
sort -u "$work/listed" -o "$work/listed"

status=0

# 4. Hard gate: a case naming a rule the spec does not declare — a typo points
#    confidently at nothing, and the manifest is published.
unknown="$(comm -23 "$work/covered" "$work/declared")"
if [ -n "$unknown" ]; then
	echo "rule vectors: named by a case but not declared by the spec —" >&2
	for id in $unknown; do echo "  $id" >&2; done
	status=1
fi

# 5. Ratchet, first half: a declared rule no case breaks and the allowlist omits.
#    This is the one that fires when a spec release adds a rule.
comm -13 "$work/covered" "$work/declared" > "$work/uncovered"
unlisted="$(comm -23 "$work/uncovered" "$work/listed")"
if [ -n "$unlisted" ]; then
	echo "rule vectors: declared, no case breaks it, and not in $allow — add a case, or list it with a reason:" >&2
	for id in $unlisted; do echo "  $id" >&2; done
	status=1
fi

# 6. Ratchet, second half: a listed rule that has since gained a case must leave the
#    list, or the list rots into a permanent exemption and step 5 goes quiet.
stale="$(comm -12 "$work/listed" "$work/covered")"
if [ -n "$stale" ]; then
	echo "rule vectors: covered now, so remove it from $allow:" >&2
	for id in $stale; do echo "  $id" >&2; done
	status=1
fi

# 7. A listed id the spec no longer declares: the listing outlived its rule.
gone="$(comm -23 "$work/listed" "$work/declared")"
if [ -n "$gone" ]; then
	echo "rule vectors: listed in $allow but no longer declared by the spec:" >&2
	for id in $gone; do echo "  $id" >&2; done
	status=1
fi

if [ "$status" -eq 0 ]; then
	printf 'rule vectors: %s ADT rules declared, %s with a case, %s listed as uncovered\n' \
		"$(wc -l < "$work/declared" | tr -d ' ')" \
		"$(wc -l < "$work/covered" | tr -d ' ')" \
		"$(wc -l < "$work/listed" | tr -d ' ')"
fi
exit "$status"
