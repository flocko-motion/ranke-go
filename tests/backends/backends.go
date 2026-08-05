// package: tests/backends / integration
// type:    tool
// job:     the shared backend matrix — one named opener per storage configuration, each spinning up
// a FRESH, EMPTY local instance (podman pods for s3/redis/neo4j, or host-native services)
// limits:  wiring only — no test logic and no assertions; the rows are consumed by the conformance
// matrix (-> tests/matrix) and the timing harness (-> tests/performance)
package backends

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	neo4jdriver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
	goredis "github.com/redis/go-redis/v9"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/adapter/storage/fs"
	"github.com/flocko-motion/ranke-go/adapter/storage/mem"
	neo4jstore "github.com/flocko-motion/ranke-go/adapter/storage/neo4j"
	redisstore "github.com/flocko-motion/ranke-go/adapter/storage/redis"
	"github.com/flocko-motion/ranke-go/adapter/storage/s3"
	"github.com/flocko-motion/ranke-go/adapter/storage/sqlite"
	"github.com/flocko-motion/ranke-go/adapter/storage/stack"
	"github.com/flocko-motion/ranke-go/internal/exclusive"
)

// ErrUnavailable reports that a backend cannot run in this environment (e.g. s3
// with no podman); a caller reports the row as skipped.
var ErrUnavailable = errors.New("backend unavailable")

// forceNativeServices routes the pod-based services (neo4j, redis) to localhost.
var forceNativeServices bool

// UseNativeServices routes neo4j and redis to host-native instances on localhost
// rather than podman pods — the harness's --native mode. Call before opening a backend.
func UseNativeServices(on bool) { forceNativeServices = on }

// Backend is one matrix row: a named factory yielding a FRESH, EMPTY local instance
// plus a cleanup func. Open returns ErrUnavailable when the backend can't run here.
type Backend struct {
	Name string
	Open func() (ranke.Universe, func(), error)
}

// Reference is the row every other backend is compared against: the in-memory store,
// answering reads through ranke.DefaultQuery. Always available, so a matrix has a baseline.
func Reference() Backend { return Backend{Name: "mem", Open: openMem} }

// All is the matrix, each row starting empty. Durable byte-stores run standalone;
// neo4j holds no CBOR and caps content, so it appears only stacked over one.
func All() []Backend {
	return []Backend{
		{"mem", openMem},
		{"fs", openFS},
		{"sqlite", openSqlite},
		{"s3", openS3},
		{"redis", openRedis},
		{"neo4j/mem", Stacked(openNeo4j, openMem)},
		{"neo4j/redis/s3", Stacked(openNeo4j, openRedis, openS3)},
	}
}

// Select filters All by name (empty = all) in matrix order; unknown names error.
func Select(names []string) ([]Backend, error) {
	all := All()
	if len(names) == 0 {
		return all, nil
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var out []Backend
	for _, be := range all {
		if want[be.Name] {
			out = append(out, be)
			delete(want, be.Name)
		}
	}
	if len(want) > 0 {
		var unknown []string
		for n := range want {
			unknown = append(unknown, n)
		}
		var have []string
		for _, be := range all {
			have = append(have, be.Name)
		}
		return nil, fmt.Errorf("unknown backend(s): %v (have %v)", unknown, have)
	}
	return out, nil
}

// --- component openers: each yields a fresh, empty instance + cleanup, or
// ErrUnavailable when it can't run here. Composed into rows and, via Stacked(), stacks. ---

func openMem() (ranke.Universe, func(), error) { return mem.New(), func() {}, nil }

func openFS() (ranke.Universe, func(), error) {
	dir, err := os.MkdirTemp("", "ranke-backend-fs-")
	if err != nil {
		return nil, nil, err
	}
	u, err := fs.New(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, nil, err
	}
	return u, func() { _ = os.RemoveAll(dir) }, nil
}

func openSqlite() (ranke.Universe, func(), error) {
	dir, err := os.MkdirTemp("", "ranke-backend-sqlite-")
	if err != nil {
		return nil, nil, err
	}
	u, err := sqlite.New(filepath.Join(dir, "backend.db"))
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, nil, err
	}
	return u, func() { _ = os.RemoveAll(dir) }, nil
}

func openS3() (ranke.Universe, func(), error) {
	client, bucket, cleanup, err := minioPod()
	if err != nil {
		return nil, nil, err
	}
	u, err := s3.New(client, bucket, s3.WithConcurrency(8))
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return u, cleanup, nil
}

func openRedis() (ranke.Universe, func(), error) {
	addr, pass, cleanup, err := redisConn()
	if err != nil {
		return nil, nil, err
	}
	client := goredis.NewClient(&goredis.Options{Addr: addr, Password: pass})
	// The key prefix isolates a run from other tenants of the same redis; the TTL expires its keys.
	u, err := redisstore.New(client,
		redisstore.WithKeyPrefix("rankeperf"),
		redisstore.WithTTL(time.Hour),
		redisstore.WithConcurrency(8))
	if err != nil {
		_ = client.Close()
		cleanup()
		return nil, nil, err
	}
	return u, func() { _ = client.Close(); cleanup() }, nil
}

func openNeo4j() (ranke.Universe, func(), error) {
	conn, connCleanup, err := neo4jConn()
	if err != nil {
		return nil, nil, err
	}
	// One live instance serves every package and each flushes it at the start, so
	// access is held exclusive until cleanup — that is what keeps two packages under
	// `go test ./...` off each other's graph.
	release := exclusive.Lock(exclusive.Neo4j)
	driver, err := neo4jdriver.NewDriverWithContext(
		conn.BoltURI, neo4jdriver.BasicAuth(conn.User, conn.Password, ""))
	if err != nil {
		release()
		connCleanup()
		return nil, nil, err
	}
	// Flush at the START: each run wants a clean slate, and the graph is left in place
	// afterwards so it stays browsable (Neo4j Browser, http://127.0.0.1:7474).
	if _, err := neo4jdriver.ExecuteQuery(context.Background(), driver,
		"MATCH (n) DETACH DELETE n", nil,
		neo4jdriver.EagerResultTransformer, neo4jdriver.ExecuteQueryWithDatabase("neo4j")); err != nil {
		_ = driver.Close(context.Background())
		release()
		connCleanup()
		return nil, nil, fmt.Errorf("flush neo4j: %w", err)
	}
	// Target the default database explicitly, and declare neo4j's inline content cap
	// (~4 KiB) so a stack over it descends to a durable tier for anything larger.
	u := neo4jstore.New(driver,
		neo4jstore.WithDatabase("neo4j"),
		neo4jstore.WithContentCap(4096))
	return u, func() {
		_ = driver.Close(context.Background())
		release()
		connCleanup()
	}, nil
}

// Stacked composes openers top→bottom; each adapter reports its own write tier,
// so the stack routes itself. One unavailable component makes the whole stack so.
func Stacked(openers ...func() (ranke.Universe, func(), error)) func() (ranke.Universe, func(), error) {
	return func() (ranke.Universe, func(), error) {
		var cleanups []func()
		cleanupAll := func() {
			for i := len(cleanups) - 1; i >= 0; i-- {
				cleanups[i]()
			}
		}
		layers := make([]ranke.Universe, 0, len(openers))
		for _, open := range openers {
			u, cl, err := open()
			if err != nil {
				cleanupAll()
				return nil, nil, err
			}
			cleanups = append(cleanups, cl)
			layers = append(layers, u)
		}
		st, err := stack.NewStack(layers...)
		if err != nil {
			cleanupAll()
			return nil, nil, err
		}
		return st, cleanupAll, nil
	}
}
