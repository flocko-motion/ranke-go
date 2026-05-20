# Makefile — ranke-go
#
# Library-only for now: `go build ./...` verifies compilation but
# produces no binary. The bin/ directory is reserved for future
# tools (e.g. a conformance-suite runner) and currently empty.

.PHONY: all build install uninstall test test-verbose vet fmt tidy clean scenarios verify-scenarios update-references

BINDIR ?= $(HOME)/.local/bin

SCENARIO_DIRS := $(wildcard conformance/scenarios/*)

# Default target: build, run unit tests, run every scenario, and
# assert the scenarios are byte-deterministic.
all: build test verify-scenarios

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

# Run user-perspective tests in /tests. The fs integration test
# uses a fixed directory (RANKE_FS_DIR, default /tmp/ranke-go-test)
# that TestMain wipes and recreates each run. Path echoed at end so
# it's visible on plain `make test` without `-v`.
RANKE_FS_DIR ?= /tmp/ranke-go-test
test:
	@RANKE_FS_DIR=$(RANKE_FS_DIR) go test ./tests/... && \
	echo "" && \
	echo "fs archive directory (preserved for inspection):" && \
	echo "  $(RANKE_FS_DIR)"

test-verbose:
	go test -v ./tests/...

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
	rm -rf bin/
	@for d in $(SCENARIO_DIRS); do \
		rm -rf "$$d/data"; \
	done
