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
#   services/neo4j.sh native {up|down|status|env|purge}
#
# Shared overrides: RANKE_NEO4J_{PASS,HTTP_PORT,BOLT_PORT,READY_TIMEOUT}.
# Pod:    RANKE_NEO4J_{NETWORK,NAME,IMAGE}.   Native: RANKE_NEO4J_{DIR,VERSION}.
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
    if curl -fsS -o /dev/null "http://127.0.0.1:${HTTP_PORT}/" 2>/dev/null; then
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

nat_is_running() { [ -d "$NEO4J_HOME" ] && neo neo4j status >/dev/null 2>&1; }

nat_print_env() {
  cat <<EOF

  Neo4j (native, in-container) is up.  user: neo4j   password: ${PASS}
  Reach it (same container → localhost):  bolt://127.0.0.1:${BOLT_PORT}   http://127.0.0.1:${HTTP_PORT}
  Install dir: ${NAT_DIR}
EOF
  test_env_hint "bolt://127.0.0.1:${BOLT_PORT}" "http://127.0.0.1:${HTTP_PORT}"
}

nat_up() {
  nat_ensure_java
  nat_ensure_neo4j
  if nat_is_running; then echo "Neo4j already running; reusing it."; else echo "starting Neo4j..."; neo neo4j start; wait_ready; fi
  nat_print_env
}

nat_down()   { nat_is_running && { neo neo4j stop; echo "stopped."; } || echo "not running."; }
nat_status() { nat_is_running && echo "running — bolt://127.0.0.1:${BOLT_PORT}" || echo "not running"; }
nat_env()    { nat_is_running && nat_print_env || { echo "not running" >&2; exit 1; }; }
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
            up | down | status | env | purge
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
      purge) nat_purge ;;
      *) usage; exit 2 ;;
    esac ;;
  ""|-h|--help|help) usage ;;
  *) echo "unknown mode: '${mode}'" >&2; usage; exit 2 ;;
esac
