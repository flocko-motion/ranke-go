# Makefile — ranke-go
#
# `make build` compiles the repo's binaries into bin/: the ranke CLI
# (cmd/ranke), the ranke-test harness (cmd/test), and the scenariodoc
# generator (cmd/scenariodoc).

.PHONY: all build install uninstall test test/core test/core/coverage test/vectors test/integration test/matrix test/performance test-verbose coverage coverage-gaps vet fmt tidy lint rule-citations verify check clean scenarios verify-scenarios update-references scenarios-docs verify-docs conformance-bundle docs docs-clean release major minor patch breaking feature fix

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

# Foundational papers live in the ranke-graph repo. `make docs` pulls a
# fresh copy into docs/papers/ for local reference; the directory is
# gitignored and never committed — always fetched, never vendored.
RANKE_GRAPH_REPO ?= https://github.com/flocko-motion/ranke-graph
RANKE_GRAPH_REF  ?= main
PAPERS_DIR       := docs/papers

SCENARIO_DIRS := $(wildcard conformance/scenarios/*)

# Default target: build, run unit tests, run every scenario, and
# assert the scenarios are byte-deterministic + docs are in sync.
all: build test verify-scenarios verify-docs

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
# deterministic archive into every backend that can run here and assert each
# one answers the whole RQL corpus exactly as the mem reference does. Rows
# needing a service that is not up skip themselves, so this is green on a bare
# checkout and grows teeth as services come up:
#   services/neo4j.sh native up    # adds the neo4j/mem row
#   services/redis.sh native up    # adds the redis row
# Verbose so the per-row, per-query sub-tests are visible.
test/matrix:
	$(GOTEST) ./tests/matrix/ -v -count=1

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

# test — the layers in order of foundation: the datatype, then the spec's artifacts,
# then the feature suite, then cross-backend agreement (which skips the rows whose
# services aren't up).
test: test/core test/vectors test/integration test/matrix

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
#
# B_h is compared on its id and height columns only. Its third column is the wall
# clock at which a head was committed, so a byte diff of it can never pass — the
# timeline records when, which is exactly the part that does not reproduce.
#
# Update after an intentional change: `make update-references`.
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
	@echo ">> fetching ranke-graph papers into $(PAPERS_DIR)/"
	@tmp=$$(mktemp -d) && \
		git clone --depth 1 --branch $(RANKE_GRAPH_REF) $(RANKE_GRAPH_REPO) $$tmp >/dev/null 2>&1 && \
		rm -rf $(PAPERS_DIR) && mkdir -p $(PAPERS_DIR) && \
		cp -r $$tmp/[0-9]*-* $(PAPERS_DIR)/ && \
		for d in shared spec glossary; do \
			[ -d $$tmp/$$d ] && cp -r $$tmp/$$d $(PAPERS_DIR)/; \
		done; \
		cp $$tmp/LICENSE $(PAPERS_DIR)/LICENSE 2>/dev/null || true; \
		rm -rf $$tmp; \
		echo ">> pulled $$(find $(PAPERS_DIR) -name '*.typ' | wc -l | tr -d ' ') paper(s)"

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

# Quick quality gate: build the binaries, check formatting, run the lint gate,
# and check rule citations — the fast "does it compile, is it gofmt-clean, does
# it pass lint" without vet or the full test suite (-> check).
#
# Needs the spec: rule-citations reads $(PAPERS_DIR), which is gitignored, so on
# a fresh clone `make verify` fails until `make docs` has fetched the papers. A
# gate that cannot see the spec cannot check it, and a skip would turn green
# exactly where it is blind.
verify: build fmt-check lint rule-citations

# One-shot "is everything green": compile all packages, vet, lint, and
# run the FULL test suite (feature suite + every adapter's conformance
# test), not just ./tests/... like `make test`.
check:
	go build ./...
	go vet ./...
	brokkr lint
	@RANKE_FS_DIR=$(RANKE_FS_DIR) $(GOTEST) ./...
