# Requirements for new Agents

You MUST read docs/papers/*
That's the precondition to do anything in this repo. 

When starting to plan a change, *always RE-read* the relevant sections in the docs. 
Agents tend to forget details from the papers. The papers specify all details, so no guessing required. 

# Rules for Tooling

- use jq, not pyton, to work with json
- use brokkr instead of grep (brokkr --help)
- use gopls instead of hand rolled mechanic refactoring with sed/python 
- use `services/neo4j.sh query {q}` for querying neo4j

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
- Every neo4j-touching test flushes the whole database at open, and `go test ./...`
  runs packages in parallel — so they wipe each other. Run `go test -p 1` for
  anything involving neo4j until that is fixed.

# Architecture

- `DefaultX` implementations are the slow, simple reference for a Universe port.
  They are not a fallback that grants a capability a layer lacks. A performance
  layer implements the port natively; using DefaultX there is the bug.
- Tags are internal performance tactics owned by the storage layer. The signal is
  global (`Universe.Tag`), the internals are the layer's.
- Capabilities exist so the router can be intelligent. A layer that cannot serve a
  request must not be asked; if asked anyway, it must fail rather than answer with
  something else.
