package performance

import (
	"errors"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNeo4j validates the neo4j test infrastructure end to end: obtain a real
// Neo4j — a host instance already serving (named by RANKE_NEO4J_BOLT/_HTTP or
// simply reachable at the default local address), else an ephemeral podman pod —
// confirm it serves, and release it. It runs against any reachable instance with
// no env var; a pod is opt-in (RANKE_PERF_NEO4J) since Neo4j is slow to boot.
// Skips cleanly otherwise, so it never fails in a podman-less sandbox.
func TestNeo4j(t *testing.T) {
	if !neo4jRequested() && !neo4jReachable() && os.Getenv("RANKE_PERF_NEO4J") == "" {
		t.Skip("no neo4j reachable (services/neo4j.sh native up), or set RANKE_PERF_NEO4J=1 to boot a pod")
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
