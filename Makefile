# Makefile — ranke-go
#
# Library-only for now: `go build ./...` verifies compilation but
# produces no binary. The bin/ directory is reserved for future
# tools (e.g. a conformance-suite runner) and currently empty.

.PHONY: all build test test-verbose vet fmt tidy clean

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

# Remove future binary outputs (none yet).
clean:
	rm -rf bin/
