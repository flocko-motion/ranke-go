# ranke-go

Go reference implementation of the **Ranke-Graph** ADT — a provenance-first
data structure for attributed claims.

The project home, paper, and conformance suite live at
[github.com/flocko-motion/ranke-graph](https://github.com/flocko-motion/ranke-graph).
This repository is the Go module:

- the canonical Go library, importable from downstream projects, and
- the reference implementation against which other implementations
  (Python, etc.) are checked for conformance.

## Status

Early. Public interfaces are defined; a minimum implementation passes
six user-perspective tests (helper contracts, scenarios drawn from
the paper, the §3.5 provenance invariant). Persistence backends
beyond in-memory, set algebra, scoped visibility, and the conformance
suite are not yet implemented — see the paper for what is intended.

## Install

```sh
go get github.com/flocko-motion/ranke-go
```

## Build & test

```sh
make            # run the tests
make build      # verify the library compiles
make test-verbose
```

## Design

Public API is interface-first; concrete types are unexported and
returned through constructors. Records are content-addressed with
IPFS multihash (SHA-256) over CBOR Deterministic encoding (RFC 8949
§4.2). See `interfaces.go` for the full surface and `tests/` for
worked examples.

## License

Apache 2.0 — see `LICENSE`.
