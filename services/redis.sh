#!/usr/bin/env bash
# services/redis.sh — an easy, ephemeral Redis instance for the cache tier /
# perf suite, in one of two modes:
#
#   pod      run Redis in a podman pod (needs podman); mirrors the s3/neo4j pods.
#   native   compile + run Redis directly in this container (no podman, no root);
#            reached at 127.0.0.1 — use this where there is no podman.
#
# Both start Redis with the credentials the tests expect, configured as a cache
# (maxmemory + LRU eviction), wait until it serves, and print the RANKE_REDIS_*
# env to point the tests at it.
#
# Usage:
#   services/redis.sh pod    {up|down|status|env}
#   services/redis.sh native {up|down|status|env|purge}
#
# Shared overrides: RANKE_REDIS_{PASS,PORT,MAXMEMORY,MAXMEMORY_POLICY,READY_TIMEOUT}.
# Pod:    RANKE_REDIS_{NAME,IMAGE}.   Native: RANKE_REDIS_{DIR,VERSION}.
set -euo pipefail

# ── shared config ──────────────────────────────────────────────────────
PASS="${RANKE_REDIS_PASS:-rankeperfpass}" # matches the test default
PORT="${RANKE_REDIS_PORT:-6379}"
MAXMEMORY="${RANKE_REDIS_MAXMEMORY:-256mb}"
MAXMEMORY_POLICY="${RANKE_REDIS_MAXMEMORY_POLICY:-allkeys-lru}"
READY_TIMEOUT="${RANKE_REDIS_READY_TIMEOUT:-60}"

test_env_hint() {
  cat <<EOF

  Point the perf tests at it:
    RANKE_REDIS_ADDR=127.0.0.1:${PORT} RANKE_REDIS_PASS=${PASS} \\
      go test ./tests/performance/ -run TestRedis -v
EOF
}

# wait_tcp polls the host-local port until it accepts a connection.
wait_tcp() {
  local deadline=$(( SECONDS + READY_TIMEOUT ))
  echo "waiting for Redis to serve (up to ${READY_TIMEOUT}s)..."
  while [ "$SECONDS" -lt "$deadline" ]; do
    if (exec 3<>"/dev/tcp/127.0.0.1/${PORT}") 2>/dev/null; then
      exec 3>&- 3<&-
      echo "Redis is serving."
      return 0
    fi
    sleep 1
  done
  echo "error: Redis did not become ready within ${READY_TIMEOUT}s" >&2
  return 1
}

# ── pod mode ───────────────────────────────────────────────────────────
POD_NAME="${RANKE_REDIS_NAME:-ranke-redis}"
POD_IMAGE="${RANKE_REDIS_IMAGE:-docker.io/library/redis:7}"

pod_need_podman() { command -v podman >/dev/null 2>&1 || { echo "error: podman not on PATH" >&2; exit 1; }; }
pod_is_running()  { [ "$(podman inspect -f '{{.State.Running}}' "$POD_NAME" 2>/dev/null)" = "true" ]; }

pod_print_env() {
  cat <<EOF

  Redis pod '${POD_NAME}' is up.  password: ${PASS}
  Reach it from the host:  127.0.0.1:${PORT}
EOF
  test_env_hint
}

pod_up() {
  pod_need_podman
  if pod_is_running; then
    echo "Redis '${POD_NAME}' already running; reusing it."
  else
    podman rm -f "$POD_NAME" >/dev/null 2>&1 || true
    echo "starting Redis '${POD_NAME}'..."
    podman run -d --rm --name "$POD_NAME" -p "127.0.0.1:${PORT}:6379" "$POD_IMAGE" \
      redis-server --requirepass "$PASS" \
      --maxmemory "$MAXMEMORY" --maxmemory-policy "$MAXMEMORY_POLICY" >/dev/null
    wait_tcp
  fi
  pod_print_env
}

pod_down()   { pod_need_podman; podman rm -f "$POD_NAME" >/dev/null 2>&1 && echo "removed '${POD_NAME}'." || echo "'${POD_NAME}' was not running."; }
pod_status() { pod_need_podman; pod_is_running && echo "'${POD_NAME}' running on 127.0.0.1:${PORT}" || echo "'${POD_NAME}' not running"; }
pod_env()    { pod_need_podman; pod_is_running && pod_print_env || { echo "'${POD_NAME}' not running" >&2; exit 1; }; }

# ── native mode ────────────────────────────────────────────────────────
NAT_DIR="${RANKE_REDIS_DIR:-$HOME/.ranke-redis}"
NAT_VERSION="${RANKE_REDIS_VERSION:-stable}"
SRC_DIR="$NAT_DIR/src"
DATA_DIR="$NAT_DIR/data"
PIDFILE="$NAT_DIR/redis.pid"
LOGFILE="$NAT_DIR/redis.log"
SERVER="$NAT_DIR/redis-server"
CLI="$NAT_DIR/redis-cli"
NAT_URL="https://download.redis.io/redis-${NAT_VERSION}.tar.gz"

nat_ensure_redis() {
  [ -x "$SERVER" ] && return 0
  command -v make >/dev/null 2>&1 || { echo "error: 'make' not on PATH (native build needs a C toolchain)" >&2; exit 1; }
  echo "downloading + building Redis ${NAT_VERSION} (~5MB source, compiles in ~1min)..."
  mkdir -p "$SRC_DIR"
  curl -fSL "$NAT_URL" -o "$NAT_DIR/redis.tar.gz"
  tar -xzf "$NAT_DIR/redis.tar.gz" -C "$SRC_DIR" --strip-components=1
  rm -f "$NAT_DIR/redis.tar.gz"
  ( cd "$SRC_DIR" && make -j"$(nproc 2>/dev/null || echo 2)" >/dev/null 2>&1 )
  cp "$SRC_DIR/src/redis-server" "$SERVER"
  cp "$SRC_DIR/src/redis-cli" "$CLI"
  echo "built redis-server + redis-cli into ${NAT_DIR}"
}

nat_ping()       { "$CLI" -p "$PORT" -a "$PASS" --no-auth-warning ping 2>/dev/null | grep -q PONG; }
nat_is_running() { [ -x "$CLI" ] && nat_ping; }

nat_print_env() {
  cat <<EOF

  Redis (native, in-container) is up.  password: ${PASS}
  Reach it (same container → localhost):  127.0.0.1:${PORT}
  Install dir: ${NAT_DIR}
  Manual CLI:  ${CLI} -p ${PORT} -a ${PASS} --no-auth-warning
EOF
  test_env_hint
}

nat_up() {
  nat_ensure_redis
  mkdir -p "$DATA_DIR"
  if nat_is_running; then
    echo "Redis already running; reusing it."
  else
    echo "starting Redis..."
    "$SERVER" --daemonize yes --port "$PORT" --requirepass "$PASS" \
      --dir "$DATA_DIR" --pidfile "$PIDFILE" --logfile "$LOGFILE" \
      --maxmemory "$MAXMEMORY" --maxmemory-policy "$MAXMEMORY_POLICY" \
      --save '' --appendonly no
    wait_tcp
  fi
  nat_print_env
}

nat_down() {
  if nat_is_running; then
    "$CLI" -p "$PORT" -a "$PASS" --no-auth-warning shutdown nosave 2>/dev/null || true
    echo "stopped."
  else
    echo "not running."
  fi
}
nat_status() { nat_is_running && echo "running — 127.0.0.1:${PORT}" || echo "not running"; }
nat_env()    { nat_is_running && nat_print_env || { echo "not running" >&2; exit 1; }; }
nat_purge()  { nat_down || true; rm -rf "$NAT_DIR"; echo "purged ${NAT_DIR}"; }

# ── dispatch ───────────────────────────────────────────────────────────
usage() {
  cat <<EOF
usage: $0 <pod|native> <command>

  pod     run Redis in a podman pod (needs podman)
            up | down | status | env
  native  compile + run Redis in this container (no podman/root)
            up | down | status | env | purge
EOF
}

mode="${1:-}"
cmd="${2:-up}"
case "$mode" in
  pod)
    case "$cmd" in
      up) pod_up ;; down) pod_down ;; status) pod_status ;; env) pod_env ;;
      *) usage; exit 2 ;;
    esac ;;
  native)
    case "$cmd" in
      up) nat_up ;; down) nat_down ;; status) nat_status ;; env) nat_env ;; purge) nat_purge ;;
      *) usage; exit 2 ;;
    esac ;;
  ""|-h|--help|help) usage ;;
  *) echo "unknown mode: '${mode}'" >&2; usage; exit 2 ;;
esac
