// package: tests/backends / integration
// type:    tool
// job:     spawn a real Neo4j instance in a podman pod for the neo4j matrix row, tear it down after
// limits:  requires podman on PATH and network to pull the image; returns ErrUnavailable when
// podman is absent. Neo4j boots slowly, so the timeouts are generous (and cover a
// first-run image pull).
package backends

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	neo4jImage = "docker.io/library/neo4j:5"
	neo4jUser  = "neo4j"
	neo4jPass  = "rankeperfpass" // Neo4j 5 requires ≥ 8 chars
	// Neo4j is slow to start; the run may also pull the image on first use.
	neo4jRunTimeout   = 5 * time.Minute
	neo4jReadyTimeout = 2 * time.Minute
)

// Neo4jConn is what a Neo4j-backed Universe needs to connect. The exact shape
// the adapter's constructor wants may differ slightly; adjust when it lands.
type Neo4jConn struct {
	BoltURI  string // bolt://127.0.0.1:PORT — the adapter connects here
	HTTPURI  string // http://127.0.0.1:PORT — used only for readiness probing
	User     string
	Password string
}

// neo4jConn prefers an already-running Neo4j and leaves it standing, else spawns an
// ephemeral pod. ErrUnavailable when neither is possible.
func neo4jConn() (*Neo4jConn, func(), error) {
	if forceNativeServices || neo4jRequested() || neo4jReachable() {
		return neo4jExternal()
	}
	return neo4jPod()
}

// neo4jRequested reports whether an external Neo4j was named explicitly via env.
func neo4jRequested() bool {
	return os.Getenv("RANKE_NEO4J_BOLT") != "" || os.Getenv("RANKE_NEO4J_HTTP") != ""
}

// neo4jReachable probes the default (or env-named) HTTP endpoint once, so tests find
// a running instance with no env var set.
func neo4jReachable() bool {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(envOr("RANKE_NEO4J_HTTP", "http://127.0.0.1:7474") + "/")
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// neo4jExternal points at a running Neo4j, verifying it serves first. Cleanup is a
// no-op, so its graph stays browsable after the run.
func neo4jExternal() (*Neo4jConn, func(), error) {
	conn := &Neo4jConn{
		BoltURI:  envOr("RANKE_NEO4J_BOLT", "bolt://127.0.0.1:7687"),
		HTTPURI:  envOr("RANKE_NEO4J_HTTP", "http://127.0.0.1:7474"),
		User:     envOr("RANKE_NEO4J_USER", neo4jUser),
		Password: envOr("RANKE_NEO4J_PASS", neo4jPass),
	}
	if err := waitNeo4j(conn.HTTPURI); err != nil {
		return nil, nil, fmt.Errorf("external neo4j at %s: %w", conn.HTTPURI, err)
	}
	return conn, func() {}, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// neo4jPod starts Neo4j in a podman pod, waits for it to serve, and returns its
// connection plus a cleanup that removes the pod. ErrUnavailable without podman.
func neo4jPod() (*Neo4jConn, func(), error) {
	if _, err := exec.LookPath("podman"); err != nil {
		return nil, nil, fmt.Errorf("%w: podman not on PATH (neo4j needs a real graph-db pod)", ErrUnavailable)
	}

	boltPort, err := freePort()
	if err != nil {
		return nil, nil, err
	}
	httpPort, err := freePort()
	if err != nil {
		return nil, nil, err
	}
	name := fmt.Sprintf("ranke-perf-neo4j-%d", boltPort)

	runCtx, cancel := context.WithTimeout(context.Background(), neo4jRunTimeout)
	defer cancel()
	out, err := exec.CommandContext(runCtx, "podman", "run", "-d", "--rm",
		"--name", name,
		"-p", fmt.Sprintf("127.0.0.1:%d:7687", boltPort),
		"-p", fmt.Sprintf("127.0.0.1:%d:7474", httpPort),
		"-e", "NEO4J_AUTH="+neo4jUser+"/"+neo4jPass,
		neo4jImage,
	).CombinedOutput()
	if err != nil {
		return nil, nil, fmt.Errorf("podman run neo4j: %v: %s", err, strings.TrimSpace(string(out)))
	}
	cleanup := func() { removePod(name) }

	conn := &Neo4jConn{
		BoltURI:  fmt.Sprintf("bolt://127.0.0.1:%d", boltPort),
		HTTPURI:  fmt.Sprintf("http://127.0.0.1:%d", httpPort),
		User:     neo4jUser,
		Password: neo4jPass,
	}
	if err := waitNeo4j(conn.HTTPURI); err != nil {
		cleanup()
		return nil, nil, err
	}
	return conn, cleanup, nil
}

// waitNeo4j polls Neo4j's HTTP discovery endpoint until it answers 200, which
// signals the database is up and serving.
func waitNeo4j(httpURI string) error {
	deadline := time.Now().Add(neo4jReadyTimeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(httpURI + "/")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("neo4j not ready at %s within %s", httpURI, neo4jReadyTimeout)
}
