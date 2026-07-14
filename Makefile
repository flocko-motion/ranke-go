# Makefile — ranke-go
#
# Library-only for now: `go build ./...` verifies compilation but
# produces no binary. The bin/ directory is reserved for future
# tools (e.g. a conformance-suite runner) and currently empty.

.PHONY: all build install uninstall test test-unit test-verbose coverage coverage-gaps vet fmt tidy lint check clean scenarios verify-scenarios update-references scenarios-docs verify-docs conformance-bundle docs docs-clean release major minor patch breaking feature fix

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

# Build the ranke CLI into bin/ranke.
build:
	@mkdir -p bin
	go build -o bin/ranke ./cmd/ranke

# Copy the built CLI to $(BINDIR)/ranke (default: ~/.local/bin).
# No sudo needed; just make sure $(BINDIR) is on your PATH.
install: build
	@mkdir -p $(BINDIR)
	install -m 0755 bin/ranke $(BINDIR)/ranke

uninstall:
	rm -f $(BINDIR)/ranke

# Unit layer: the root package's atom-level tests (claims, codec,
# content, signing) — no infrastructure, no fs. Fast; runs first so a
# broken foundation fails before the feature suite spins up.
test-unit:
	go test .

# Run user-perspective tests in /tests, preceded by the unit layer so
# `make test` covers both. The fs integration test uses a fixed directory
# (RANKE_FS_DIR, default /tmp/ranke-go-test) that TestMain wipes and
# recreates each run. Path echoed at end so it's visible on plain
# `make test` without `-v`.
RANKE_FS_DIR ?= /tmp/ranke-go-test
test: test-unit
	@RANKE_FS_DIR=$(RANKE_FS_DIR) go test ./tests/... && \
	echo "" && \
	echo "fs archive directory (preserved for inspection):" && \
	echo "  $(RANKE_FS_DIR)"

test-verbose:
	go test -v ./tests/...

# Merged library coverage from `go test ./...`. -coverpkg attributes
# coverage to the /tests package, which imports the library. (Packages
# without _test.go files, like conformance, are skipped by go test.)
coverage:
	@RANKE_FS_DIR=$(RANKE_FS_DIR) go test -coverpkg=$(COVERPKG) \
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
	go test -v -run "TestIntegration|TestProvenance" ./...

# Static checks.
vet:
	go vet ./...

fmt:
	gofmt -w .

tidy:
	go mod tidy

# Cut a release: clean tree → merge to the default branch via PR → tag the
# merged tip → push the tag → return to your branch. The merged-PR CI is the
# test gate (no local e2e). Usage: make release <major|minor|patch>
# (aliases: breaking|feature|fix).
release:
	@./scripts/release.sh $(filter major minor patch breaking feature fix,$(MAKECMDGOALS))

# Absorb the positional bump word in `make release <bump>` so it isn't treated
# as a missing target.
major minor patch breaking feature fix:
	@:

# Run every conformance scenario from a clean state.
scenarios:
	@conformance/run.sh

# Run each scenario fresh, diff the produced archive + ids.txt
# against the committed archive_reference/ + ids_reference.txt.
# Fails on any drift — the cross-implementation conformance promise.
# To update the references after an intentional change:
# `make update-references`.
verify-scenarios:
	@for d in $(SCENARIO_DIRS); do \
		echo "--- verify $$d ---"; \
		(cd "$$d" && rm -rf data && go run . > /dev/null); \
		diff -r "$$d/data_reference" "$$d/data" > /dev/null \
			&& echo "$$d: matches reference ✓" \
			|| { echo "$$d: DRIFT — differs from checked-in reference"; exit 1; }; \
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
		{ [ -d $$tmp/shared ] && cp -r $$tmp/shared $(PAPERS_DIR)/ || true; } && \
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

# One-shot "is everything green": compile all packages, vet, lint, and
# run the FULL test suite (feature suite + every adapter's conformance
# test), not just ./tests/... like `make test`.
check:
	go build ./...
	go vet ./...
	brokkr lint
	@RANKE_FS_DIR=$(RANKE_FS_DIR) go test ./...
