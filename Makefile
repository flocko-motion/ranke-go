# Makefile — ranke-go
#
# Library-only for now: `go build ./...` verifies compilation but
# produces no binary. The bin/ directory is reserved for future
# tools (e.g. a conformance-suite runner) and currently empty.

.PHONY: all build test test-verbose vet fmt tidy clean scenarios verify-scenarios

SCENARIO_DIRS := $(wildcard conformance/scenarios/*)

# Default target: run the tests.
all: test

# Build the ranke CLI into bin/ranke.
build:
	@mkdir -p bin
	go build -o bin/ranke ./cmd/ranke

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
	@for d in $(SCENARIO_DIRS); do \
		echo "--- $$d ---"; \
		"$$d/run.sh"; \
	done

# Re-run each scenario; assert outputs are byte-identical to what's
# checked in (= the cross-implementation conformance promise — see
# conformance/README.md). Fails on any drift.
verify-scenarios:
	@for d in $(SCENARIO_DIRS); do \
		echo "--- verify $$d ---"; \
		tmp=$$(mktemp -d); \
		cp -r "$$d/archive" "$$tmp/before-archive"; \
		cp "$$d/ids.txt"   "$$tmp/before-ids.txt"; \
		"$$d/run.sh" >/dev/null; \
		diff -r "$$tmp/before-archive" "$$d/archive" > /dev/null \
			&& diff "$$tmp/before-ids.txt" "$$d/ids.txt" > /dev/null \
			&& echo "$$d: DETERMINISTIC ✓" \
			|| { echo "$$d: DRIFT — re-run produced different bytes"; exit 1; }; \
		rm -rf "$$tmp"; \
	done

clean:
	rm -rf bin/
	@for d in $(SCENARIO_DIRS); do \
		rm -rf "$$d/archive" "$$d/ids.txt"; \
	done
