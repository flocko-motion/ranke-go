# Makefile — ranke-go
#
# `make build` compiles the repo's binaries into bin/: the ranke CLI
# (cmd/ranke), the ranke-test harness (cmd/test), and the scenariodoc
# generator (cmd/scenariodoc).

.PHONY: all build install uninstall check/full test test/full full-intended test/core test/core/coverage test/vectors test/integration test/matrix test/concurrency test/performance test-verbose coverage coverage-gaps vet fmt tidy lint rule-citations rql-schema verify check clean scenarios verify-scenarios update-references scenarios-docs verify-docs conformance-bundle docs docs-current docs-clean release major minor patch breaking feature fix

# "The library" for coverage purposes = the root package plus the mem
# storage adapter. mem is the fundamental, always-present, dependency-free
# Universe — root behaviour can only be exercised through some adapter,
# and mem is the one that ships everywhere. Other adapters are verified
# against the Universe contract via adapter/storage/adaptertest, not counted.
MODULE  := github.com/flocko-motion/ranke-go
COVERPKG := $(MODULE),$(MODULE)/adapter/storage/mem
# Test packages that drive the number — explicitly enumerated, NOT a
# ./adapter/... glob, so a new adapter is opted into the core test
# deliberately rather than swept in by accident:
#   ./tests/...                    the library's feature suite
#   ./adapter/storage/mem/...      fundamental adapter (also counted in COVERPKG)
#   ./adapter/storage/fs/...       exercises root through a second real backend
#   ./adapter/storage/sqlite/...   pure-Go SQLite backend (modernc, no cgo)
#   ./adapter/storage/s3/...       S3 backend against an in-process gofakes3 server
#   ./adapter/storage/minimal/...  smallest possible adapter (map behind BlobStore)
#   ./adapter/storage/rest/...     HTTP blob backend against an in-process server
#   ./adapter/sequencer/...        BranchTableHead backends (mem/file/func)
# Every adapter here runs with NO special infrastructure. Adapter
# statements are NOT in COVERPKG — they're verified against their contract,
# not counted. conformance scenarios are deliberately excluded.
COVERDRIVERS := ./tests/... ./adapter/storage/mem/... ./adapter/storage/fs/... ./adapter/storage/sqlite/... ./adapter/storage/s3/... ./adapter/storage/minimal/... ./adapter/storage/rest/... ./adapter/storage/stack/... ./adapter/storage/partition/... ./adapter/sequencer/...

BINDIR ?= $(HOME)/.local/bin

# Every test target goes through GOTEST, so one override reaches all of them:
# `make test GOTEST="go test -count=1"` to defeat the cache, for instance.
# Packages run in parallel — internal/exclusive locks the shared services.
GOTEST ?= go test

# The rows the fast gate asks for: the backends that need no service, so it has
# nothing to skip. RANKE_ROWS carries the set to the Go layer
# (-> tests/backends.Requested); test/full leaves it unset, which means all of them.
FAST_ROWS ?= mem,fs,sqlite

# The benchmark's size under test/full — the claim count per backend. Bigger is a
# deliberate run: `make test/performance/2000`.
FULL_PERF_SIZE ?= 800

# Foundational papers live in the ranke-graph repo. `make docs` pulls a
# fresh copy into docs/papers/ for local reference; the directory is
# gitignored and never committed — always fetched, never vendored.
RANKE_GRAPH_REPO ?= https://github.com/flocko-motion/ranke-graph
RANKE_GRAPH_REF  ?= main
PAPERS_DIR       := docs/papers

SCENARIO_DIRS := $(wildcard conformance/scenarios/*)

# Default target: the static gates (build, gofmt, lint, rule citations, scenario
# bundles) plus the fast test gate. The full suite is a deliberate ask —
# `make test/full` — since it needs every service up.
all: verify test

# Build all binaries into bin/: the ranke CLI, the ranke-test harness,
# and the scenariodoc generator.
build:
	@mkdir -p bin
	go build -o bin/ranke ./cmd/ranke
	go build -o bin/ranke-test ./cmd/test
	go build -o bin/scenariodoc ./cmd/scenariodoc

# Copy the built CLI to $(BINDIR)/ranke (default: ~/.local/bin).
# No sudo needed; just make sure $(BINDIR) is on your PATH.
install: build
	@mkdir -p $(BINDIR)
	install -m 0755 bin/ranke $(BINDIR)/ranke

uninstall:
	rm -f $(BINDIR)/ranke

# test/core — the datatype unit layer (root package): claims, codec,
# content, id, signing, graph, guarantees. No infrastructure, no fs, no
# adapters. Fast; the correctness of the datatype itself lives here.
test/core:
	$(GOTEST) .

# test/core/coverage — the datatype layer with statement coverage. Prints a
# per-file breakdown (from the raw profile, statement-weighted) and the core
# total. Drill into one file's functions with:
#   go tool cover -func=coverage-core.out | grep node.go
# or open the annotated source with:
#   go tool cover -html=coverage-core.out
test/core/coverage:
	@$(GOTEST) . -covermode=atomic -coverprofile=coverage-core.out
	@echo ""
	@echo "coverage by file:"
	@awk 'NR>1 { split($$1,a,":"); f=a[1]; sub(/.*\//,"",f); t[f]+=$$2; if ($$3>0) c[f]+=$$2 } \
		END { for (f in t) printf "  %5.1f%%  %s\n", 100*c[f]/t[f], f }' coverage-core.out | sort -k2
	@echo ""
	@printf "core "; go tool cover -func=coverage-core.out | tail -1

# test/integration — the blackbox suite in /tests: the Archive/Sequencer
# layer driven across adapters. The fs test uses a fixed directory
# (RANKE_FS_DIR, default /tmp/ranke-go-test) that TestMain wipes and
# recreates each run; the path is echoed so it's visible without `-v`.
RANKE_FS_DIR ?= /tmp/ranke-go-test
test/integration:
	@RANKE_FS_DIR=$(RANKE_FS_DIR) $(GOTEST) ./tests/... && \
	echo "" && \
	echo "fs archive directory (preserved for inspection):" && \
	echo "  $(RANKE_FS_DIR)"

# test/matrix — the cross-backend conformance matrix: build the same
# deterministic archive into every backend the run asks for and assert each one
# answers the whole RQL corpus exactly as the mem reference does. RANKE_ROWS names
# the set (default: all of them), and the services they need come up with:
#   services/neo4j.sh native up    # the neo4j/mem row
#   services/redis.sh native up    # the redis row
#   services/s3.sh native up       # the s3 row, and the neo4j/redis/s3 stack
# Verbose so the per-row, per-query sub-tests are visible.
test/matrix:
	$(GOTEST) ./tests/matrix/ -v -count=1

# test/concurrency/N — N writers contributing at once, over every (Sequencer,
# storage) pair the run asks for. The suite runs this at a modest count already;
# this target is for the massive one, which is where a race that hides at 64
# writers shows itself. E.g.
#   make test/concurrency/1000
# RANKE_ROWS narrows the storage half, and the services come up as for test/matrix.
# The serialised Sequencer is capped internally — its count is sequential work.
test/concurrency/%:
	@RANKE_CONCURRENCY=$* $(GOTEST) ./tests/ -run TestConcurrentContributionsLoseNothing \
		-v -count=1 -timeout 30m

# test/performance/N — the backend matrix: generate the same deterministic
# size-N archive into each storage backend and time build + verify, one row
# per backend. N is the generator size knob (SpecForSize); ~5*N claims. E.g.
#   make test/performance/2000   # a 10k+-claim archive per backend
# Verbose so the per-backend timing rows print; generous timeout for large N.
test/performance/%:
	@RANKE_PERF_SIZE=$* $(GOTEST) ./tests/performance/ -run TestPerformanceMatrix -v -count=1 -timeout 30m

# test/vectors — the spec's own conformance artifacts, fetched from the latest
# ranke-graph release. The gate that catches an encoder change: verification hashes
# stored bytes, so existing claims keep verifying while newly built ones drift, and
# nothing else here would notice. Point RANKE_TESTDATA_DIR at an extracted set to
# run it offline. Unreachable is a failure, here as in CI: not checking conformance
# is worse than a red run. The bundle is cached and revalidated, so a run that finds
# the release unchanged transfers nothing.
test/vectors:
	go test ./tests/ -run TestPublished -v -count=1

# test — the fast gate, and one pass over every package: the datatype, the feature
# suite, every adapter, and cross-backend agreement over the rows that need no
# service. Each package runs once, so the cache works and an untouched tree re-runs
# in seconds. It asks for nothing it cannot have — no service rows, so there is
# nothing to skip and no green covering a backend that never ran.
#
# What it deliberately does not do: the performance benchmark, the 10k-claim scale
# set, and the service rows. `make test/full` is where those live.
test:
	@RANKE_FS_DIR=$(RANKE_FS_DIR) RANKE_ROWS=$(FAST_ROWS) $(GOTEST) ./...

# test/full — everything, and the target CI runs, so the gate and the local run are
# the same thing. Every backend row is REQUIRED: RANKE_ROWS is unset, so the matrix
# and the benchmark ask for all of them, and a row that cannot open fails the run
# rather than skipping. Also the benchmark, the scale set, and the scenario bundles
# with their docs.
#
# Needs the services up (services/{neo4j,redis,s3}.sh native up) and the spec
# fetched (make docs). The 30m timeout is per package: the matrix and the benchmark
# are minutes each against live services, well past go test's 10m default.
#
# WRITES to the tree: verify-scenarios regenerates conformance/scenarios/*/data/,
# which `make clean` owns and .gitignore covers.
# Guarded because it is minutes, CI runs it on every push, and it was being reached
# for during ordinary work where `make test` was the answer. CI passes the guard by
# being CI; a person passes it by saying so.
test/full: full-intended
	@RANKE_FS_DIR=$(RANKE_FS_DIR) RANKE_PERF_SIZE=$(FULL_PERF_SIZE) RANKE_SCALE=1 \
		$(GOTEST) -timeout 30m ./...
	@$(MAKE) verify-scenarios verify-docs

# full-intended stops a slow run nobody meant to start. GITHUB_ACTIONS and CI are
# set by a runner; RANKE_FULL is a person saying they mean it.
full-intended:
	@if [ -z "$$GITHUB_ACTIONS$$CI$$RANKE_FULL" ]; then \
		echo ""; \
		echo "  make test/full takes MINUTES: every backend row, the benchmark, the"; \
		echo "  10k-claim scale set, the scenario bundles and their docs."; \
		echo ""; \
		echo "  During regular work you want:   make test      (seconds)"; \
		echo "  CI runs test/full on every push, so the slow run happens anyway."; \
		echo ""; \
		echo "  Run it yourself ONLY when you touched what the fast gate leaves out —"; \
		echo "  a service-backed row, the benchmark, the scale set, a scenario bundle."; \
		echo "  Then say so, on whichever target you meant:"; \
		echo ""; \
		echo "      RANKE_FULL=1 make $(firstword $(MAKECMDGOALS))"; \
		echo ""; \
		exit 1; \
	fi

test-verbose:
	$(GOTEST) -v ./tests/...

# Merged library coverage from `go test ./...`. -coverpkg attributes
# coverage to the /tests package, which imports the library. (Packages
# without _test.go files, like conformance, are skipped by go test.)
coverage:
	@RANKE_FS_DIR=$(RANKE_FS_DIR) $(GOTEST) -coverpkg=$(COVERPKG) \
		-covermode=atomic -coverprofile=coverage.out $(COVERDRIVERS)
	@go tool cover -func=coverage.out | tail -1

# Map of where coverage is missing: every library function below 100%,
# worst first. Refreshes the profile via `coverage` first.
coverage-gaps: coverage
	@echo ""
	@echo "Functions below 100% coverage (worst first):"
	@go tool cover -func=coverage.out \
		| awk '$$3+0<100 && $$1 ~ /\.go:/ { print $$3"\t"$$0 }' \
		| sort -n \
		| sed 's#$(MODULE)/##'

# Narrative output: scenarios print what they are doing at every step.
# Useful for understanding what the integration suite covers.
test-debug:
	$(GOTEST) -v -run "TestIntegration|TestProvenance" ./...

# Static checks.
vet:
	go vet ./...

fmt:
	gofmt -w .

# Fail if any Go file is not gofmt-clean (lists the offenders). The check half
# of `fmt`; wired into `verify`. Skips .worktrees (sibling agent checkouts).
fmt-check:
	@out="$$(find . -path ./.worktrees -prune -o -name '*.go' -print | xargs gofmt -l)"; \
	if [ -n "$$out" ]; then \
		echo "gofmt needed (run 'make fmt'):"; echo "$$out"; exit 1; \
	fi

tidy:
	go mod tidy

# Cut a release: verify → rebase onto the default branch → merge via PR → tag the
# merged tip → push the tag → watch the release workflow, failing here if it fails.
# Usage: make release <major|minor|patch> (aliases: breaking|feature|fix).
release: verify
	@./scripts/release.sh $(filter major minor patch breaking feature fix,$(MAKECMDGOALS))

# Absorb the positional bump word in `make release <bump>` so it isn't treated
# as a missing target.
major minor patch breaking feature fix:
	@:

# Run every conformance scenario from a clean state.
scenarios:
	@conformance/run.sh

# Run each scenario fresh and diff the produced bundle against the committed
# reference: same claims under the same ids, same branch heads at the same heights.
# Wired into `verify`, so anything that moves an id fails here first — the ids are
# signatures, and the reference bundle is the only thing holding them to a value.
#
# B_h is compared on its id and height columns only. Its third column is the wall
# clock at which a head was committed, so a byte diff of it can never pass — the
# timeline records when, which is exactly the part that does not reproduce.
#
# Update after an intentional change: `make update-references`. Regenerating is
# not the same as checking: the bundle is self-generated, so promote it only
# after reading what changed and confirming each scenario still verifies clean.
verify-scenarios:
	@for d in $(SCENARIO_DIRS); do \
		echo "--- verify $$d ---"; \
		(cd "$$d" && rm -rf data && go run . > /dev/null); \
		want=$$(mktemp); got=$$(mktemp); \
		awk '{print $$1, $$2}' "$$d/data_reference/branches/B_h" > "$$want"; \
		awk '{print $$1, $$2}' "$$d/data/branches/B_h" > "$$got"; \
		if diff -r --exclude=B_h "$$d/data_reference" "$$d/data" > /dev/null \
			&& diff "$$want" "$$got" > /dev/null; then \
			echo "$$d: matches reference ✓"; \
		else \
			echo "$$d: DRIFT — differs from checked-in reference"; \
			rm -f "$$want" "$$got"; exit 1; \
		fi; \
		rm -f "$$want" "$$got"; \
	done

# Replace each scenario's archive_reference/ + ids_reference.txt
# with its current generated outputs. Run after an intentional
# scenario change, review the diff, then commit.
update-references:
	@for d in $(SCENARIO_DIRS); do \
		echo "--- update $$d ---"; \
		(cd "$$d" && rm -rf data && go run . > /dev/null); \
		rm -rf "$$d/data_reference"; \
		cp -r "$$d/data" "$$d/data_reference"; \
	done
	@echo "References updated. Review with: git diff conformance/scenarios/"

clean:
	rm -rf bin/ dist/
	@for d in $(SCENARIO_DIRS); do \
		rm -rf "$$d/data"; \
	done

# Regenerate each scenario.md from comments in main.go + the
# template at conformance/helpers/scenario.md.tmpl.
scenarios-docs:
	@go run ./cmd/scenariodoc

# Verify scenario.md files are in sync with main.go comments.
# Regenerates and diffs against checked-in state — fails on drift.
verify-docs: scenarios-docs
	@git diff --exit-code conformance/scenarios/*/scenario.md \
		|| { echo "scenario.md out of sync — run 'make scenarios-docs' and commit"; exit 1; }

# Build a self-contained conformance bundle suitable for downstream
# variant implementations (Python, ...) to verify against. Output:
# dist/ranke-conformance-<VERSION>.tar.gz.
#
# VERSION defaults to the current git describe (tag if on one, else
# short SHA). Override with `make conformance-bundle VERSION=v0.2.0`.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
conformance-bundle: verify-scenarios scenarios-docs
	@mkdir -p dist
	@BUNDLE=ranke-conformance-$(VERSION); \
	WORK=$$(mktemp -d); \
	mkdir -p "$$WORK/$$BUNDLE"; \
	(cd "$$WORK/$$BUNDLE" && mkdir conformance); \
	git ls-files conformance/ | tar -cf - -T - | tar -xf - -C "$$WORK/$$BUNDLE/"; \
	[ -f specification.txt ] && cp specification.txt "$$WORK/$$BUNDLE/" || true; \
	cp README.md "$$WORK/$$BUNDLE/" 2>/dev/null || true; \
	tar -C "$$WORK" -czf "dist/$$BUNDLE.tar.gz" "$$BUNDLE"; \
	rm -rf "$$WORK"; \
	echo "wrote dist/$$BUNDLE.tar.gz"

# Pull the latest ranke-graph papers into docs/papers/ for reference.
# Not committed — fetched fresh (see .gitignore).
docs:
	@RANKE_GRAPH_REPO=$(RANKE_GRAPH_REPO) RANKE_GRAPH_REF=$(RANKE_GRAPH_REF) \
		PAPERS_DIR=$(PAPERS_DIR) ./scripts/fetch-papers.sh

# The freshness check the gates run: `git ls-remote` against the stamp
# scripts/fetch-papers.sh writes, cloning only when the ref has moved — 40 bytes on
# the common path, 1.8 MB when there is something new to read.
#
# `verify` depends on this because the papers directory is gitignored and never
# expires: a copy fetched last week reads exactly like one fetched a minute ago, so
# every gate over it was reporting green against whatever happened to be on disk.
# That is how ranke-ts came to gate on six-day-old vectors, and the release path is
# where it costs most — `release` runs `verify`, so a stale spec ships.
#
# It needs the network. RANKE_DOCS_OFFLINE=1 keeps the copy on disk instead, and
# RANKE_SPEC / RANKE_RQL_SCHEMA point the individual gates at a copy of your own.
docs-current:
	@RANKE_GRAPH_REPO=$(RANKE_GRAPH_REPO) RANKE_GRAPH_REF=$(RANKE_GRAPH_REF) \
		PAPERS_DIR=$(PAPERS_DIR) ./scripts/fetch-papers.sh --if-moved

# Remove the pulled paper references.
docs-clean:
	rm -rf $(PAPERS_DIR)

# brokkr static-analysis gate: canonical headers, exported-doc coverage,
# deadcode (with --test), and the line-count limit.
lint:
	brokkr lint

# Rule-citation gate: every backticked `V-…`/`R-…` id a comment cites is one the
# spec declares, and every declared rule is either cited or listed in
# scripts/rule-citations.allow with a reason. It says nothing about whether a
# citation is TRUE — only that the ids exist and are accounted for.
#
# The spec comes from $(PAPERS_DIR), or from RANKE_SPEC when you are working
# against a copy of your own:  make rule-citations RANKE_SPEC=path/to/spec.typ
rule-citations:
	@./scripts/rule-citations.sh

# Rule-coverage gate for the published reference vectors: every ADT (V-*) rule the
# spec declares either has a case that BREAKS it, or is listed in
# scripts/rule-vectors.allow with a reason. It generates the set and reads the
# manifest, so what is gated is the artifact downstream receives.
#
# Coverage is per RULE, not per clause — see the script's header for what that misses.
# Needs the spec, like rule-citations; RANKE_SPEC points it at a copy.
rule-vectors:
	@./scripts/rule-vectors.sh

# rql-schema: the machine-readable projection of the query language against the Go
# constants that implement it. Every constraint the schema states is probed against
# DecodeQuery, and a keyword the gate cannot check FAILS rather than passing silently.
# Needs the schema from gitignored docs/papers/, which `verify` fetches through
# `docs-current` before this runs; on its own it fails on a bare checkout rather than
# passing blind. RANKE_RQL_SCHEMA points it elsewhere.
rql-schema:
	@go run ./scripts/rqlgate

# The static gates: build the binaries, check formatting, run the lint gate, check
# rule citations, and reproduce the scenario bundles. Everything that reads the tree
# rather than running the suite — the tests are `test` (fast) and `test/full`
# (everything). Around 2s on top of the build.
#
# Needs the spec, and now fetches it: `docs-current` brings $(PAPERS_DIR) up to the
# remote ref before any gate reads it, so a bare clone no longer fails here and a
# stale copy no longer passes. A gate that cannot see the spec cannot check it, and
# one reading a copy of unknown age is blind in the same way — a skip would turn
# green exactly there.
#
# WRITES to the tree: verify-scenarios regenerates conformance/scenarios/*/data/,
# which `make clean` owns and .gitignore covers — no hand-written file is touched,
# but a bundle you were reading is rebuilt under you. It is here because the
# scenario references are checked nowhere a change is made: CI checks them
# (.github/workflows/ci.yml), which is a verdict arriving after the work has left
# the desk, and that is how the 0.18.0 signature framing landed against a bundle
# it had invalidated. `release` depends on this target, so an id-moving release
# now stops here rather than shipping a reference that reproduces nothing.
verify: docs-current build fmt-check lint rule-citations rule-vectors rql-schema verify-scenarios

# One-shot "is everything green", and the name people reach for, so it costs what
# that name promises: the static gates, vet, and the fast suite. Seconds. The full
# suite against live services is `RANKE_FULL=1 make test/full`, which CI runs on
# every push.
check: verify vet test

# check/full — check with the full suite instead of the fast one: the gate CI runs,
# and what to run by hand before a release. Guarded through test/full, so it needs
# GITHUB_ACTIONS, CI or RANKE_FULL.
check/full: verify vet test/full
