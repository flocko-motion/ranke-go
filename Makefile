# Makefile — ranke-go
#
# Library-only for now: `go build ./...` verifies compilation but
# produces no binary. The bin/ directory is reserved for future
# tools (e.g. a conformance-suite runner) and currently empty.

.PHONY: all build test test-verbose vet fmt tidy clean

# Default target: run the tests.
all: test

# Verify the library compiles. No binary output.
build:
	go build ./...

# Run user-perspective tests in /tests. The fs integration test
# preserves its on-disk layout for inspection — Make picks the dir
# (so it's visible without `-v`) and echoes it after the run.
test:
	@dir=$$(mktemp -d -t ranke-test-fs.XXXXXX) && \
	RANKE_FS_DIR=$$dir go test ./tests/... && \
	echo "" && \
	echo "fs archive directory (preserved for inspection):" && \
	echo "  $$dir"

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
