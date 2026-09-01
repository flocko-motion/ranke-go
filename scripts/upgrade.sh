#!/usr/bin/env sh
# upgrade.sh — bring the module's dependencies up to date in one shot: every Go
# dependency (direct + indirect + test) and brokkr, the lint tool. Then tidy and
# run the green gate, so a breaking upgrade shows up here instead of in the next
# build.
#
# The Go directive is NOT swept along. It is the floor every contributor and CI
# image has to install, so it only moves when someone says so: the script asks,
# defaults to no, and puts back what `go get -u` raises behind its back (go and
# toolchain are pseudo-modules in the module graph, so -u upgrades them too).
#
# Skip the question, decide up front:
#   GO_VERSION=1.26.5 make upgrade   # take that floor, no prompt
#   GO_VERSION=keep make upgrade     # leave the floor alone, no prompt
set -eu

GO_VERSION="${GO_VERSION:-ask}"

installed_go="$(go env GOVERSION)"

# Read the directive out of go.mod directly. `go list -m` would need to load the
# module, which fails the moment the floor sits above the running toolchain —
# exactly the state we have to detect and undo.
go_directive() { awk '$1 == "go" && NF == 2 { print $2; exit }' go.mod; }

# go.mod as it stands now: the exact artifact this script rewrites, so diffing it
# afterwards reports what will be committed — no module-graph noise, and immune
# to whatever else is already dirty in the tree.
before="$(mktemp)"
trap 'rm -f "$before"' EXIT
cp go.mod "$before"

go_before="$(go_directive)"
go_want=""

case "$GO_VERSION" in
keep)
	echo ">> go directive: keeping $go_before (GO_VERSION=keep)"
	;;
ask)
	go_latest="$(go list -m -f '{{.Version}}' go@latest 2>/dev/null || true)"
	if [ -z "$go_latest" ] || [ "$go_latest" = "$go_before" ]; then
		echo ">> go directive: $go_before, nothing newer to take"
	elif [ ! -t 0 ]; then
		echo ">> go directive: keeping $go_before (go $go_latest is out; GO_VERSION=$go_latest to take it)"
	else
		printf '>> go directive: on %s, go %s is out. Raise the floor for every contributor? [y/N] ' "$go_before" "$go_latest"
		read -r answer
		case "$answer" in
		y | Y | yes | YES) go_want="$go_latest" ;;
		*) echo ">> go directive: keeping $go_before" ;;
		esac
	fi
	;;
*)
	go_want="$GO_VERSION"
	;;
esac

# Only an explicit yes may pull a newer toolchain down, and only for this run.
if [ -n "$go_want" ]; then
	GOTOOLCHAIN="${GOTOOLCHAIN:-auto}"
	export GOTOOLCHAIN
	echo ">> go directive: $go_before → $go_want (GOTOOLCHAIN=$GOTOOLCHAIN, installed: $installed_go)"
	go get "go@$go_want"
fi

# Undo an unasked-for floor bump. Called after every `go get`, because the next
# one cannot even load the module while the floor sits above the toolchain.
hold_go_directive() {
	now="$(go_directive)"
	target="${go_want:-$go_before}"
	if [ "$now" != "$target" ]; then
		echo ">> go directive: raised to $now by the upgrade — putting it back to $target"
		go mod edit -go="$target" -toolchain=none
	fi
}

echo ">> dependencies: go get -u -t ./..."
if ! go get -u -t ./...; then
	# When -u raises the floor past the running toolchain, go reports that as an
	# error — but only after writing the dependency upgrades, which are what we
	# came for. A failure with the floor untouched is a real one.
	if [ "$(go_directive)" = "$go_before" ]; then
		echo ">> dependencies: go get -u failed" >&2
		exit 1
	fi
	echo ">> dependencies: upgrades landed; $installed_go cannot load the floor -u wrote"
fi
hold_go_directive

# brokkr is a standalone binary, not a go.mod dependency, so `go get` never
# touches it — `make tools` checks its own release against bin/tools/brokkr.
echo ">> tools: brokkr"
"${MAKE:-make}" -s tools

go mod tidy
hold_go_directive

if cmp -s "$before" go.mod; then
	echo ">> already up to date — go.mod unchanged"
	exit 0
fi

# Every changed require/go line, without diff's file headers or hunk marks.
echo ">> go.mod changes:"
diff -u "$before" go.mod | grep -E '^[-+][^-+]' | sed 's/^/     /'

echo ">> verifying the upgrade"
if "${MAKE:-make}" -s check; then
	echo ">> upgrade OK — review the diff, then commit go.mod/go.sum"
else
	echo ">> upgrade BREAKS the gate — fix the callers, or revert: git checkout go.mod go.sum" >&2
	exit 1
fi
