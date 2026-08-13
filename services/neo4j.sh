#!/usr/bin/env bash
# services/neo4j.sh — an easy, ephemeral Neo4j test instance for the perf suite,
# in one of two modes:
#
#   pod      run Neo4j in a podman pod on a shared network (needs podman);
#            mirrors how the s3 row uses a MinIO pod.
#   native   install + run Neo4j directly in this container (no podman, no root);
#            reached at 127.0.0.1 — use this where there is no podman.
#
# Both start Neo4j with the credentials the perf tests expect, wait until it
# serves, and print the RANKE_NEO4J_* env to point the tests at it.
#
# Usage:
#   services/neo4j.sh pod    up [agent-pod]   # start in a pod; optionally wire an agent pod to the net
#   services/neo4j.sh pod    {down|status|env}
#   services/neo4j.sh native {up|down|status|env|reap|purge}
#
# Shared overrides: RANKE_NEO4J_{PASS,HTTP_PORT,BOLT_PORT,READY_TIMEOUT}.
# Pod:    RANKE_NEO4J_{NETWORK,NAME,IMAGE}.
# Native: RANKE_NEO4J_{DIR,VERSION,HEAP,PAGECACHE,IDLE_MINUTES}.
set -euo pipefail

# ── shared config ──────────────────────────────────────────────────────
PASS="${RANKE_NEO4J_PASS:-rankeperfpass}" # matches the test default; >= 8 chars
HTTP_PORT="${RANKE_NEO4J_HTTP_PORT:-7474}"
BOLT_PORT="${RANKE_NEO4J_BOLT_PORT:-7687}"
READY_TIMEOUT="${RANKE_NEO4J_READY_TIMEOUT:-120}"

# wait_ready polls Neo4j's HTTP endpoint on the host/container-local port.
wait_ready() {
  local deadline=$(( SECONDS + READY_TIMEOUT ))
  echo "waiting for Neo4j to serve (up to ${READY_TIMEOUT}s)..."
  while [ "$SECONDS" -lt "$deadline" ]; do
    if curl -fsS --max-time 5 -o /dev/null "http://127.0.0.1:${HTTP_PORT}/" 2>/dev/null; then
      echo "Neo4j is serving."
      return 0
    fi
    sleep 1
  done
  echo "error: Neo4j did not become ready within ${READY_TIMEOUT}s" >&2
  return 1
}

test_env_hint() {
  local bolt="$1" http="$2"
  cat <<EOF

  Point the perf tests at it:
    RANKE_NEO4J_BOLT=${bolt} \\
    RANKE_NEO4J_HTTP=${http} \\
      go test ./tests/performance/ -run TestNeo4j -v
EOF
}

# ── pod mode ───────────────────────────────────────────────────────────
POD_NETWORK="${RANKE_NEO4J_NETWORK:-ranke-net}"
POD_NAME="${RANKE_NEO4J_NAME:-ranke-neo4j}"
POD_IMAGE="${RANKE_NEO4J_IMAGE:-docker.io/library/neo4j:5}"

pod_need_podman() { command -v podman >/dev/null 2>&1 || { echo "error: podman not on PATH" >&2; exit 1; }; }
pod_ip()          { podman inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$POD_NAME" 2>/dev/null; }
pod_is_running()  { [ "$(podman inspect -f '{{.State.Running}}' "$POD_NAME" 2>/dev/null)" = "true" ]; }

pod_print_env() {
  local ip; ip="$(pod_ip)"
  cat <<EOF

  Neo4j pod '${POD_NAME}' is up.  user: neo4j   password: ${PASS}

  Reach it
    from a pod on '${POD_NETWORK}':  bolt://${POD_NAME}:7687   http://${POD_NAME}:7474
    by container IP:                 bolt://${ip}:7687   http://${ip}:7474
    from the host:                   bolt://127.0.0.1:${BOLT_PORT}   http://127.0.0.1:${HTTP_PORT}
EOF
  test_env_hint "bolt://${POD_NAME}:7687" "http://${POD_NAME}:7474"
  cat <<EOF

  If your agent pod is not on '${POD_NETWORK}':  services/neo4j.sh pod up <agent-pod>
EOF
}

pod_up() {
  pod_need_podman
  local agent_pod="${1:-}"
  podman network exists "$POD_NETWORK" 2>/dev/null || { echo "creating network '${POD_NETWORK}'..."; podman network create "$POD_NETWORK" >/dev/null; }
  if pod_is_running; then
    echo "Neo4j '${POD_NAME}' already running; reusing it."
  else
    podman rm -f "$POD_NAME" >/dev/null 2>&1 || true
    echo "starting Neo4j '${POD_NAME}' on '${POD_NETWORK}'..."
    podman run -d --rm --name "$POD_NAME" --network "$POD_NETWORK" \
      -p "127.0.0.1:${HTTP_PORT}:7474" -p "127.0.0.1:${BOLT_PORT}:7687" \
      -e "NEO4J_AUTH=neo4j/${PASS}" "$POD_IMAGE" >/dev/null
    wait_ready
  fi
  if [ -n "$agent_pod" ]; then
    echo "connecting '${agent_pod}' to '${POD_NETWORK}'..."
    podman network connect "$POD_NETWORK" "$agent_pod" || \
      echo "note: could not connect '${agent_pod}' (a pod's network may be fixed at creation — relaunch it with --network ${POD_NETWORK})" >&2
  fi
  pod_print_env
}

pod_down()   { pod_need_podman; podman rm -f "$POD_NAME" >/dev/null 2>&1 && echo "removed '${POD_NAME}'." || echo "'${POD_NAME}' was not running."; }
pod_status() { pod_need_podman; pod_is_running && echo "'${POD_NAME}' running — IP $(pod_ip) on '${POD_NETWORK}'" || echo "'${POD_NAME}' not running"; }
pod_env()    { pod_need_podman; pod_is_running && pod_print_env || { echo "'${POD_NAME}' not running" >&2; exit 1; }; }

# ── native mode ────────────────────────────────────────────────────────
NAT_DIR="${RANKE_NEO4J_DIR:-$HOME/.ranke-neo4j}"
NAT_VERSION="${RANKE_NEO4J_VERSION:-5.26.0}"
NEO4J_HOME="$NAT_DIR/neo4j"
NAT_NEO4J_URL="https://dist.neo4j.org/neo4j-community-${NAT_VERSION}-unix.tar.gz"
NAT_JRE_URL="https://api.adoptium.net/v3/binary/latest/21/ga/linux/x64/jre/hotspot/normal/eclipse"

# neo runs a Neo4j bin command with our bundled JRE (unless the system has java).
neo() {
  if [ -d "$NAT_DIR/jre" ]; then JAVA_HOME="$NAT_DIR/jre" "$NEO4J_HOME/bin/$@"; else "$NEO4J_HOME/bin/$@"; fi
}

nat_ensure_java() {
  command -v java >/dev/null 2>&1 && return 0
  [ -x "$NAT_DIR/jre/bin/java" ] && return 0
  echo "downloading Temurin 21 JRE..."
  mkdir -p "$NAT_DIR/jre"
  curl -fSL "$NAT_JRE_URL" -o "$NAT_DIR/jre.tar.gz"
  tar -xzf "$NAT_DIR/jre.tar.gz" -C "$NAT_DIR/jre" --strip-components=1
  rm -f "$NAT_DIR/jre.tar.gz"
}

nat_ensure_neo4j() {
  [ -d "$NEO4J_HOME" ] && return 0
  echo "downloading Neo4j ${NAT_VERSION} (~150MB)..."
  mkdir -p "$NAT_DIR"
  curl -fSL "$NAT_NEO4J_URL" -o "$NAT_DIR/neo4j.tar.gz"
  tar -xzf "$NAT_DIR/neo4j.tar.gz" -C "$NAT_DIR"
  mv "$NAT_DIR/neo4j-community-${NAT_VERSION}" "$NEO4J_HOME"
  rm -f "$NAT_DIR/neo4j.tar.gz"
  echo "setting initial password..."
  neo neo4j-admin dbms set-initial-password "$PASS" >/dev/null 2>&1 || \
    echo "note: could not set initial password (already initialised?)" >&2
}

# Neo4j sizes itself for a dedicated host: no heap ceiling, and a page cache
# heuristic reading half of RAM. initial_size stays UNSET on purpose — pinned equal
# to max_size, AlwaysPreTouch makes the whole heap resident at startup, which costs
# more than no ceiling at all. Idle RSS over 6 runs on a 42 GB box: uncapped
# 414-686 MB, pinned 513-530, max_size alone 411-438. The cap buys a bounded worst
# case, not a fixed saving. Idempotent; `up` caps an install that predates it.
NAT_HEAP="${RANKE_NEO4J_HEAP:-256m}"
NAT_PAGECACHE="${RANKE_NEO4J_PAGECACHE:-128m}"

nat_cap_memory() {
  local conf="$NEO4J_HOME/conf/neo4j.conf"
  [ -w "$conf" ] || return 0
  grep -q '^# ranke test memory caps' "$conf" && return 0
  cat >>"$conf" <<EOF

# ranke test memory caps — see services/neo4j.sh nat_cap_memory
server.memory.heap.max_size=${NAT_HEAP}
server.memory.pagecache.size=${NAT_PAGECACHE}
EOF
  echo "capped heap at ${NAT_HEAP}, page cache at ${NAT_PAGECACHE}."
}

# nat_is_serving means Neo4j ANSWERS. `neo4j status` reads a pidfile, so a JVM that
# failed to bind reads as running and each retry adds another. --max-time is needed
# because a wedged JVM accepts the connection and never replies.
nat_is_serving() { curl -fsS --max-time 5 -o /dev/null "http://127.0.0.1:${HTTP_PORT}/" 2>/dev/null; }

# nat_pid echoes THIS install's JVM pid. The cmdline is checked for our own NEO4J_HOME,
# since a pidfile can name a dead or reused pid — and that check is what keeps the
# reaper off an instance it did not start.
nat_pid() {
  local pidfile="$NEO4J_HOME/run/neo4j.pid" pid
  [ -r "$pidfile" ] || return 1
  pid=$(cat "$pidfile" 2>/dev/null) || return 1
  [ -n "$pid" ] || return 1
  grep -qF "$NEO4J_HOME" "/proc/$pid/cmdline" 2>/dev/null || return 1
  echo "$pid"
}

# nat_clear_stale removes this install's JVM, serving or not, and the pidfile naming
# it. Called before a start so two attempts cannot leave two JVMs.
nat_clear_stale() {
  local pid
  if pid=$(nat_pid); then
    echo "stopping Neo4j (pid ${pid})..."
    neo neo4j stop >/dev/null 2>&1 || true
    kill -0 "$pid" 2>/dev/null && { kill "$pid" 2>/dev/null; sleep 2; }
    kill -0 "$pid" 2>/dev/null && kill -9 "$pid" 2>/dev/null
  fi
  rm -f "$NEO4J_HOME/run/neo4j.pid"
}

nat_print_env() {
  cat <<EOF

  Neo4j (native, in-container) is up.  user: neo4j   password: ${PASS}
  Reach it (same container → localhost):  bolt://127.0.0.1:${BOLT_PORT}   http://127.0.0.1:${HTTP_PORT}
  Install dir: ${NAT_DIR}
EOF
  test_env_hint "bolt://127.0.0.1:${BOLT_PORT}" "http://127.0.0.1:${HTTP_PORT}"
}

# The lock internal/exclusive takes around the shared Neo4j. The kernel drops a flock
# when its holder dies, so this survives a kill and needs nothing from the tests.
NAT_LOCK="${TMPDIR:-/tmp}/ranke-neo4j.lock"
NAT_IDLE_MINUTES="${RANKE_NEO4J_IDLE_MINUTES:-5}"

# nat_tests_running reports that some process holds the lock. Non-blocking: the answer
# is about this instant, which is why it is only half the reaper's test.
nat_tests_running() {
  command -v flock >/dev/null 2>&1 || return 0 # no flock: assume busy, never reap
  [ -e "$NAT_LOCK" ] || return 1
  ! flock -n "$NAT_LOCK" true 2>/dev/null
}

# nat_recently_active reports a store write within NAT_IDLE_MINUTES. The data dir is
# the signal because tests already produce it — each flushes the database at open —
# where a heartbeat must be remembered. Measured: 19h idle left the mtime unmoved.
nat_recently_active() {
  local data="$NEO4J_HOME/data"
  [ -d "$data" ] || return 1
  [ -n "$(find "$data" -newermt "-${NAT_IDLE_MINUTES} minutes" -print -quit 2>/dev/null)" ]
}

# nat_reap_if_idle needs both halves: packages take the lock separately, so it is
# momentarily free between two of them and the lock alone would reap mid-suite.
nat_reap_if_idle() {
  nat_pid >/dev/null || return 0
  nat_tests_running && return 0
  nat_recently_active && return 0
  echo "reaping a Neo4j idle for over ${NAT_IDLE_MINUTES}m with no tests holding the lock..."
  nat_clear_stale
}

nat_up() {
  nat_ensure_java
  nat_ensure_neo4j
  nat_cap_memory # a fresh install and one predating the caps both get them here
  if nat_is_serving; then
    echo "Neo4j already serving; reusing it."
    nat_print_env
    return 0
  fi
  # Nothing answers, so any JVM left is in the way whether or not it is idle — which
  # is why the reaper is the `reap` verb and not a step here: at `up` you want one.
  nat_clear_stale
  echo "starting Neo4j..."
  neo neo4j start
  # The env is a promise that something answers, so it is printed only once one does.
  if ! wait_ready; then
    echo "error: Neo4j did not come up; leaving nothing behind." >&2
    nat_clear_stale
    return 1
  fi
  nat_print_env
}

# down stops whatever this install left behind, serving or not — the leak is the case
# where a JVM exists and does not answer.
nat_down()   { nat_pid >/dev/null && { nat_clear_stale; echo "stopped."; } || echo "not running."; }
nat_status() {
  if nat_is_serving; then echo "serving — bolt://127.0.0.1:${BOLT_PORT}"
  elif nat_pid >/dev/null; then echo "a JVM is up but not serving (pid $(nat_pid)) — run 'down' then 'up'"
  else echo "not running"; fi
}
nat_env()    { nat_is_serving && nat_print_env || { echo "not serving" >&2; exit 1; }; }
nat_purge()  { nat_down || true; rm -rf "$NAT_DIR"; echo "purged ${NAT_DIR}"; }

# ── query ──────────────────────────────────────────────────────────────
# Run any Cypher against the running instance and print one JSON object per
# row, keyed by return column. Mode-independent: both pod and native publish
# HTTP on 127.0.0.1:${HTTP_PORT}. Cypher comes from the argument, or from stdin
# when the argument is "-", so a heredoc can carry a multi-line statement.
# --params takes a JSON object bound as the statement's $-parameters, which is
# how the adapter runs its own queries.
query() {
  local cypher="" params="{}"
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --params) params="${2:-}"; shift 2 ;;
      *) cypher="$1"; shift ;;
    esac
  done
  [ "$cypher" = "-" ] && cypher="$(cat)"
  if [ -z "$cypher" ]; then
    echo "error: no cypher given (pass a statement, or '-' to read stdin)" >&2
    return 2
  fi
  if ! jq -e 'type == "object"' >/dev/null 2>&1 <<<"$params"; then
    echo "error: --params must be a JSON object, got: ${params}" >&2
    return 2
  fi
  local body out
  body="$(jq -nc --arg s "$cypher" --argjson p "$params" \
    '{statements:[{statement:$s, parameters:$p}]}')"
  out="$(curl -fsS -u "neo4j:${PASS}" -H 'Content-Type: application/json' \
    -X POST "http://127.0.0.1:${HTTP_PORT}/db/${RANKE_NEO4J_DB:-neo4j}/tx/commit" \
    -d "$body")" || { echo "error: could not reach Neo4j on 127.0.0.1:${HTTP_PORT} (is it up?)" >&2; return 1; }

  # Neo4j reports Cypher errors in-band with HTTP 200, so check them explicitly.
  if [ "$(jq -r '.errors | length' <<<"$out")" != "0" ]; then
    jq -r '.errors[] | "error: \(.code): \(.message)"' <<<"$out" >&2
    return 1
  fi
  # Zip each row against the column names, so output is self-describing rather
  # than positional.
  jq -c '.results[]? | .columns as $c | .data[]?
         | [$c, .row] | transpose | map({(.[0]): .[1]}) | add' <<<"$out"
}

# ── dispatch ───────────────────────────────────────────────────────────
usage() {
  cat <<EOF
usage: $0 <pod|native> <command> [opts]
       $0 query <cypher> [--params '<json>']

  pod     run Neo4j in a podman pod on a shared network (needs podman)
            up [agent-pod] | down | status | env
  native  install + run Neo4j in this container (no podman/root)
            up | down | status | env | reap | purge
            up reuses a serving instance, clears a leaked one, and fails
            rather than printing the env when nothing comes up; status means
            SERVING, not "a pid exists"; reap stops an instance no test holds
            and none has touched for RANKE_NEO4J_IDLE_MINUTES (default 5).
  query   run any Cypher against the running instance (either mode) and
          print one JSON object per row, keyed by return column. Pass '-'
          to read the statement from stdin, and --params a JSON object to
          bind the statement's \$-parameters.
            $0 query 'MATCH (n) RETURN labels(n)[0] AS label, n.content_size AS size'
            $0 query 'MATCH (n {id: \$id}) RETURN n.height' --params '{"id":"b5ua..."}'

  Overrides: RANKE_NEO4J_DB selects the database (default neo4j).
EOF
}

mode="${1:-}"
cmd="${2:-up}"
case "$mode" in
  query)
    shift; query "$@" ;;
  pod)
    case "$cmd" in
      up) pod_up "${3:-}" ;;
      down) pod_down ;;
      status) pod_status ;;
      env) pod_env ;;
      *) usage; exit 2 ;;
    esac ;;
  native)
    case "$cmd" in
      up) nat_up ;;
      down) nat_down ;;
      status) nat_status ;;
      env) nat_env ;;
      reap) nat_reap_if_idle ;;
      purge) nat_purge ;;
      *) usage; exit 2 ;;
    esac ;;
  ""|-h|--help|help) usage ;;
  *) echo "unknown mode: '${mode}'" >&2; usage; exit 2 ;;
esac
