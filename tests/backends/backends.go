// package: tests/backends / integration
// type:    tool
// job:     the shared backend matrix — one named opener per storage configuration, each spinning up a FRESH, EMPTY local instance (podman pods for s3/redis/neo4j, or host-native services)
// limits:  wiring only — no test logic and no assertions; the rows are consumed by the conformance matrix (-> tests/matrix) and the timing harness (-> tests/performance)
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
)

// ErrUnavailable is returned by a Backend.Open when the backend cannot run in
// this environment (e.g. s3 with no podman). A caller reports it as skipped
// rather than a failure.
var ErrUnavailable = errors.New("backend unavailable")

// forceNativeServices routes the pod-based services (neo4j, redis) to a
// host-native instance on localhost instead of an ephemeral podman pod. Set via
// UseNativeServices; a run under go test leaves it false.
var forceNativeServices bool

// UseNativeServices routes neo4j and redis to host-native instances on
// localhost rather than spawning podman pods — the --native mode of the CLI
// harness. Call before opening any backend.
func UseNativeServices(on bool) { forceNativeServices = on }

// Backend is one matrix row: a named factory that spins up a FRESH, EMPTY
// local instance and returns it plus a cleanup func. Open returns
// ErrUnavailable when the backend can't run here.
type Backend struct {
	Name string
	Open func() (ranke.Universe, func(), error)
}

// Reference is the row every other backend is compared against: the in-memory
// store, which answers reads through the library's reference executor
// (ranke.DefaultQuery). Always available, so a matrix always has a baseline.
func Reference() Backend { return Backend{Name: "mem", Open: openMem} }

// All is the matrix. Durable byte-stores run standalone (mem, fs, sqlite, s3,
// redis). Neo4j is a graph-native cache — it holds no canonical CBOR and drops
// content over its cap, so it appears ONLY in stacks over a durable tier that
// holds the bytes and serves content misses (neo4j/mem; the production
// neo4j/redis/s3). Each row starts empty.
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

// Select filters All by name (empty = all), preserving matrix order. Unknown
// names are reported via the returned error.
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
// ErrUnavailable when it can't run here. Composed into standalone rows and,
// via Stacked(), into cache stacks. ---

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
	// A key prefix isolates a run's keys from any other tenant of the same
	// redis; a generous TTL lets a run's keys expire on their own; the bulk ops
	// fan out at moderate concurrency.
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
	driver, err := neo4jdriver.NewDriverWithContext(
		conn.BoltURI, neo4jdriver.BasicAuth(conn.User, conn.Password, ""))
	if err != nil {
		connCleanup()
		return nil, nil, err
	}
	// Flush at the START, not teardown: each run wants a clean slate, but the
	// graph is left in place afterwards so it stays browsable (Neo4j Browser,
	// http://127.0.0.1:7474). This also clears any data a prior kept run left.
	if _, err := neo4jdriver.ExecuteQuery(context.Background(), driver,
		"MATCH (n) DETACH DELETE n", nil,
		neo4jdriver.EagerResultTransformer, neo4jdriver.ExecuteQueryWithDatabase("neo4j")); err != nil {
		_ = driver.Close(context.Background())
		connCleanup()
		return nil, nil, fmt.Errorf("flush neo4j: %w", err)
	}
	// Target the default database explicitly, and declare neo4j's inline
	// content cap (~4 KiB) so a stack over it can descend to a durable tier
	// for anything larger — the cap-aware read path the stack relies on.
	u := neo4jstore.New(driver,
		neo4jstore.WithDatabase("neo4j"),
		neo4jstore.WithContentCap(4096))
	return u, func() {
		_ = driver.Close(context.Background())
		connCleanup()
	}, nil
}

// Stacked composes openers top→bottom into one Universe. Each adapter already
// carries its own write tier (Capabilities.Tier) — neo4j eager, redis lazy,
// mem/s3 authoritative — so the stack routes by what the layers report; there
// is nothing to wrap here. If any component is unavailable (ErrUnavailable) the
// whole stack is, and any already-opened components are cleaned up.
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
