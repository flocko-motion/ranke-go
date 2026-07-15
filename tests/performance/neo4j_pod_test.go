package performance

import (
	"errors"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNeo4j validates the neo4j test infrastructure end to end: obtain a real
// Neo4j — an external/host instance when RANKE_NEO4J_BOLT/_HTTP is set, else an
// ephemeral podman pod — confirm it serves, and release it. Opt-in: it runs
// only when pointed at a host instance (RANKE_NEO4J_BOLT/_HTTP) or asked to
// spawn a pod (RANKE_PERF_NEO4J), since Neo4j is slow to boot. Skips cleanly
// otherwise, so it never fails in a podman-less sandbox with no host instance.
func TestNeo4j(t *testing.T) {
	external := os.Getenv("RANKE_NEO4J_BOLT") != "" || os.Getenv("RANKE_NEO4J_HTTP") != ""
	if !external && os.Getenv("RANKE_PERF_NEO4J") == "" {
		t.Skip("point at a host Neo4j via RANKE_NEO4J_BOLT/_HTTP, or set RANKE_PERF_NEO4J=1 to boot a pod")
	}
	conn, cleanup, err := neo4jConn()
	if errors.Is(err, ErrUnavailable) {
		t.Skipf("neo4j unavailable: %v", err)
	}
	require.NoError(t, err)
	defer cleanup()

	require.NotEmpty(t, conn.BoltURI)
	require.Equal(t, neo4jUser, conn.User)

	// It answered readiness during neo4jPod; confirm the HTTP endpoint is live.
	resp, err := http.Get(conn.HTTPURI + "/")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}
