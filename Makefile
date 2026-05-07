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

# Run user-perspective tests in /tests.
test:
	go test ./tests/...

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
