// package: tests/performance / integration
// type:    test
// job:     the go-test entrypoint into the performance matrix — runs RunMatrix with a Config from env (RANKE_PERF_SIZE / RANKE_PERF_ACCESS / RANKE_ROWS), asserting every requested backend verifies
// limits:  the matrix itself lives in harness.go (shared with cmd/test); this file only adapts it to `go test` (env config, t.Log output, failure assertion). It times and verifies; whether backends AGREE on what they read is tests/matrix
package performance

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rankegraph/ranke-go/tests/backends"
)

// sizeEnv is the benchmark's size knob and its gate in one: timing an archive into
// every backend is minutes of work that answers no correctness question, so it runs
// only where it was asked for — `make test/full` and `make test/performance/N`.
const sizeEnv = "RANKE_PERF_SIZE"

// TestPerformanceMatrix runs the matrix at the env-configured size and fails if any
// backend fails to verify, or if fewer rows complete than were requested. A requested
// row that cannot open makes the run wrong, not smaller. It asserts each backend can
// hold and verify the archive, not that the backends answer reads identically — that
// is the conformance matrix (tests/matrix).
func TestPerformanceMatrix(t *testing.T) {
	if os.Getenv(sizeEnv) == "" {
		t.Skipf("%s unset: the benchmark runs under `make test/full` or `make test/performance/N`", sizeEnv)
	}
	rows, err := backends.Requested()
	require.NoErrorf(t, err, "the row set named by %s", backends.RowsEnv)
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, row.Name)
	}
	cfg := Config{
		Size:     envInt(t, sizeEnv, 800), // target claims (SpecForNodes)
		Seed:     1,
		Access:   envInt(t, "RANKE_PERF_ACCESS", 50),
		Backends: names,
	}
	var completed []string
	onResult := func(backend string, _ int) error {
		completed = append(completed, backend)
		return nil
	}
	if err := RunMatrix(cfg, &testWriter{t: t}, onResult); err != nil {
		t.Fatal(err)
	}
	require.ElementsMatchf(t, names, completed,
		"%d of %d requested rows completed — the rest could not open, and a row asked for is a row required",
		len(completed), len(names))
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
