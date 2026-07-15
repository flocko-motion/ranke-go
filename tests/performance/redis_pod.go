// package: tests/performance / integration
// type:    tool
// job:     obtain a Redis for the matrix — a host/local instance via RANKE_REDIS_ADDR (services/redis.sh), else an ephemeral podman pod — with connection details for the redis adapter
// limits:  requires RANKE_REDIS_ADDR or podman; returns ErrUnavailable otherwise. Never tears down an instance it did not start.
package performance

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	redisImage = "docker.io/library/redis:7-alpine"
	redisPass  = "rankeperfpass" // matches services/redis.sh default
	redisReady = 30 * time.Second
)

// redisConn yields a Redis to test against. RANKE_REDIS_ADDR points at an
// already-running instance (e.g. one from services/redis.sh, reached at
// 127.0.0.1 in-container) and is never torn down; otherwise an ephemeral pod is
// spawned (needs podman). Returns ErrUnavailable when neither is possible.
func redisConn() (addr, pass string, cleanup func(), err error) {
	if a := os.Getenv("RANKE_REDIS_ADDR"); a != "" {
		p := redisPass
		if v := os.Getenv("RANKE_REDIS_PASS"); v != "" {
			p = v
		}
		return a, p, func() {}, nil
	}
	return redisPod()
}

// redisPod starts a Redis container in a podman pod, waits for it to accept
// connections, and returns its address plus a cleanup that removes the pod.
func redisPod() (addr, pass string, cleanup func(), err error) {
	if _, e := exec.LookPath("podman"); e != nil {
		return "", "", nil, fmt.Errorf("%w: podman not on PATH (redis needs a pod, or set RANKE_REDIS_ADDR)", ErrUnavailable)
	}
	port, e := freePort()
	if e != nil {
		return "", "", nil, e
	}
	name := fmt.Sprintf("ranke-perf-redis-%d", port)

	runCtx, cancel := context.WithTimeout(context.Background(), redisReady)
	defer cancel()
	out, e := exec.CommandContext(runCtx, "podman", "run", "-d", "--rm",
		"--name", name,
		"-p", fmt.Sprintf("127.0.0.1:%d:6379", port),
		redisImage, "redis-server", "--requirepass", redisPass,
	).CombinedOutput()
	if e != nil {
		return "", "", nil, fmt.Errorf("podman run redis: %v: %s", e, strings.TrimSpace(string(out)))
	}
	cleanup = func() { _ = exec.Command("podman", "rm", "-f", name).Run() }

	addr = fmt.Sprintf("127.0.0.1:%d", port)
	if e := waitTCP(addr); e != nil {
		cleanup()
		return "", "", nil, e
	}
	return addr, redisPass, cleanup, nil
}

// waitTCP blocks until addr accepts a TCP connection, or the deadline passes.
func waitTCP(addr string) error {
	deadline := time.Now().Add(redisReady)
	for time.Now().Before(deadline) {
		if c, e := net.DialTimeout("tcp", addr, time.Second); e == nil {
			_ = c.Close()
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("redis not accepting connections at %s within %s", addr, redisReady)
}
