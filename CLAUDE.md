# Requirements for new Agents

You MUST read docs/papers/*
That's the precondition to do anything in this repo. 

When starting to plan a change, *always RE-read* the relevant sections in the docs. 
Agents tend to forget details from the papers. The papers specify all details, so no guessing required. 

# Rules for Tooling

Before hand-rolling anything, look for the thing that already does it. In order:
`Makefile` (how to build, test, lint), `services/*` (how to get infrastructure
up), `brokkr` (how to read the code). Each has a mode for what you want; a long
bespoke `go test` incantation or a hand-written Cypher dump means you skipped a
step. Read a script's `usage()`, not just its top comment — the top comment is
routinely the shorter list.

- use jq, not pyton, to work with json
- use brokkr instead of grep (brokkr --help)
- use gopls instead of hand rolled mechanic refactoring with sed/python 
- use `services/neo4j.sh query {q}` for querying neo4j

## Makefile

The entry point for building and testing. `grep -E '^[a-z].*:' Makefile` lists
the targets; each carries a comment saying what it is for.

- `make test` — the fast gate: one pass over `./...`, the rows that need no
  service (`RANKE_ROWS=mem,fs,sqlite`), no benchmark and no 10k-claim scale set.
  Seconds, and the cache works.
- `make test/full` — everything: every row required, the benchmark, the scale
  set, the scenarios and their docs. The target CI runs.
- `make test/matrix` — the matrix alone, verbose, over every row. A row it asks
  for and cannot open FAILS; narrow the set with `RANKE_ROWS=mem,fs,sqlite` when
  the services are not up.
- `make check` — the static gates, vet, and `test/full` in one.
- Every target runs through `$(GOTEST)`, so one override reaches all of them
  (`make test/integration GOTEST="go test -count=1"` to skip the cache). Packages
  run in parallel — `internal/exclusive` serialises the shared services — and
  `-p 1` is not needed (see Tests): adding it back costs wall-clock and buys
  nothing. Do not add a bare `go test` to this file; override the whole thing.

## services

Infrastructure for the rows that need it. All three scripts have a **`native`**
mode that runs the service in-container — no podman, no root — which is the mode
that works here:

- `services/neo4j.sh native up` — adds the `neo4j/mem` row. Auto-detected once
  serving; no env var needed.
- `services/redis.sh native up` — does NOT auto-detect. The row stays skipped
  until you also pass `RANKE_REDIS_ADDR=127.0.0.1:6379
  RANKE_REDIS_PASS=rankeperfpass`.
- `services/s3.sh native up` — adds the `s3` row, and with the other two the
  `neo4j/redis/s3` stack. Also needs its env: `RANKE_S3_ENDPOINT=http://127.0.0.1:9000
  RANKE_S3_KEY=minioadmin RANKE_S3_SECRET=minioadmin`. Each open creates its own
  bucket, so concurrent runs against one store stay off each other's objects.
- `services/neo4j.sh query '<cypher>'` — ad-hoc Cypher against the running
  instance. The way to isolate a lowering bug: run the generated statement
  directly and bisect it, rather than inferring from a Go-level error.
- The pod mode of each script, and the pods `minioPod()`/`redisPod()`/`neo4jPod()`
  spawn, need podman — absent here. The env vars above are the way in without it,
  and CI uses the same ones against its service containers.

## brokkr

- use `brokkr --help` to learn all available features
- `brokkr map [path...]` — per file, the arch header plus each type/func with its
  doc and signature. The way to learn a package.
- `brokkr map --grep <text>` — the grep replacement: shows the *declarations
  enclosing* a match, not raw lines. If you find yourself piping its output
  through grep to make it useful, it was the wrong tool for the question.
- `brokkr lint` — the quality gate: four-field header on every non-test .go file,
  a doc comment on every exported symbol, deadcode, 700 lines per file.
- `--tail N` gets bounded output plus the exit status in one call.

## gopls

- `gopls imports -w <file>` after moving code between files or packages — one
  file per invocation.
- `gopls rename` for symbols. There is no package-move command: `gomvpkg` predates
  modules and does not work here, so a package move is `git mv` plus editing each
  import line by hand.

## Not this

- No `sed` and no python for editing files. Manual edits, or gopls.
- No compound shell commands — one command per call, so a failure is legible.
- For "what depends on this package", `go list -f '{{.ImportPath}} {{.Imports}}
  {{.TestImports}}' ./...` is authoritative. A text search is not: it misses
  build constraints and cannot tell a test import from a real one.

# Writing code

- Comments are short. Two lines is already long; a 10-line block is wrong.
  Say why, not what.
- Say what a thing IS, not what it is not.
- No `fmt.Errorf` in the codebase. Errors are static sentinels (`errors.New`,
  one per fixed condition, collected per package) composed lazily — in package
  ranke via `wrap` / `withDetail` / `wrapDetail`. `errors.Join(sentinel, ...)`
  keeps `errors.Is` matching when a sentinel from another package must stay
  matchable. `fmt.Errorf` is fine in tests.

# Tests

- A test must not do the system's work for it. If a test calls the thing that
  production forgets to call, the test passes and reality fails.
- Cross-backend agreement proves backends agree, not that they are right. Where
  every backend shares a gap they agree happily. Assert exact answers on a toy
  graph (`generator.Toy*`) for that; the matrix is the second layer.
- Tests require infrastructure. Missing infrastructure is a test failure, not a
  skip.
- Every neo4j-touching test flushes the whole database at open, and one live instance
  serves every package. `internal/exclusive` holds a cross-process lock around that,
  so `go test ./...` is safe and `-p 1` is no longer needed. A `sync.Mutex` cannot do
  this: each package is its own process.
- A new test that wipes a shared service takes `exclusive.Lock` too, or it will wipe
  another package's data.
- Reproduce before diagnosing, and trust the failure over the ticket. A title
  saying every query diverges may mean one query errors; the corpus tells you
  which. Fixing the described bug rather than the observed one wastes the run.

# Generator

- `tests/generator` generates every variation the ADT allows, including the
  awkward ones: cross-branch references, diff chains, external content,
  oversized fields, multiple contributors. A corner it omits is a corner
  nothing tests, so breadth is the point — not realism.
- `Toy*` specs are the opposite: the smallest archive exhibiting ONE corner, so
  a test asserting on it has nothing else in the graph to explain.

# Architecture

- `DefaultX` implementations are the slow, simple reference for a Universe port.
  They are not a fallback that grants a capability a layer lacks. A performance
  layer implements the port natively; using DefaultX there is the bug.
- Tags are internal performance tactics owned by the storage layer. The signal is
  global (`Universe.Tag`), the internals are the layer's.
- Capabilities exist so the router can be intelligent. A layer that cannot serve a
  request must not be asked; if asked anyway, it must fail rather than answer with
  something else.
