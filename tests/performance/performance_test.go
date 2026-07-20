// package: tests/performance / integration
// type:    test
// job:     the go-test entrypoint into the performance matrix — runs RunMatrix with a Config from env (RANKE_PERF_SIZE / RANKE_PERF_ACCESS), asserting every backend verifies
// limits:  the matrix itself lives in harness.go (shared with cmd/test); this file only adapts it to `go test` (env config, t.Log output, failure assertion)
package performance

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPerformanceMatrix runs the matrix at the env-configured size and fails if
// any backend fails to verify. `make test/performance/N` sets RANKE_PERF_SIZE.
func TestPerformanceMatrix(t *testing.T) {
	cfg := Config{
		Size:        envInt(t, "RANKE_PERF_SIZE", 800), // target claims (SpecForNodes)
		Seed:        1,
		Access:      envInt(t, "RANKE_PERF_ACCESS", 50),
		Correctness: true, // the test's whole point: every backend matches the mem reference
	}
	if err := RunMatrix(cfg, &testWriter{t: t}, nil); err != nil {
		t.Fatal(err)
	}
}

func envInt(t *testing.T, key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	require.NoError(t, err, "%s must be an integer", key)
	require.Positive(t, n, "%s must be positive", key)
	return n
}

// testWriter routes the matrix report through t.Log, one call per line so the
// test framework's formatting stays intact.
type testWriter struct {
	t   *testing.T
	buf string
}

func (w *testWriter) Write(p []byte) (int, error) {
	w.buf += string(p)
	for {
		i := strings.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.t.Log(w.buf[:i])
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}
