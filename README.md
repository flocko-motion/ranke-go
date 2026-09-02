# ranke-go

Go reference implementation of the **Ranke-Graph** ADT (spec §4) — a
content-addressed, provenance-carrying graph of attributed claims.

The project home, paper, and cross-language conformance suite live at
[github.com/rankegraph/ranke-graph](https://github.com/rankegraph/ranke-graph).
This repository is the Go module: the canonical library importable from
downstream projects, and the reference other implementations are checked
against.

Full API docs: [pkg.go.dev/github.com/rankegraph/ranke-go](https://pkg.go.dev/github.com/rankegraph/ranke-go).

## Install

```sh
go get github.com/rankegraph/ranke-go
```

## Quickstart

Build a small attributed graph and commit it to a branch over an
in-memory store:

```go
package main

import (
	"context"
	"fmt"

	"github.com/rankegraph/ranke-go"
	"github.com/rankegraph/ranke-go/adapter/mem"
)

func main() {
	ctx := context.Background()

	// A contributor is the root of attribution (identity-signed here;
	// pass a key via ClaimBuilder.Pubkey + .Sign(key) for real signing).
	aliceClaim, err := ranke.ClaimBuilder{
		Type:    ranke.NodeContributor,
		Content: []byte("alice@example.com"),
	}.Sign()
	if err != nil {
		panic(err)
	}
	alice, err := aliceClaim.AsContributor()
	if err != nil {
		panic(err)
	}

	// A source claim attributed to Alice.
	email, err := ranke.ClaimBuilder{
		Type:        ranke.TypeSource("email"),
		Encoding:    ranke.EncodingMessage("rfc822"),
		Content:     []byte("From: a\r\nTo: b\r\n\r\nhi\r\n"),
		Contributor: alice,
	}.Sign()
	if err != nil {
		panic(err)
	}

	g := ranke.NewGraph(alice)
	if err := g.Add(email); err != nil {
		panic(err)
	}

	// Compose an Archive (𝒰, B_h) and commit the graph to branch "main".
	arc, err := ranke.NewArchive(ctx, mem.New(), ranke.NewMemBranchTableHead())
	if err != nil {
		panic(err)
	}
	if err := arc.AddGraph(ctx, "main", g, alice); err != nil {
		panic(err)
	}

	// Read the head back and verify its provenance (§5.10).
	if err := arc.VerifyBranch(ctx, "main"); err != nil {
		panic(err)
	}
	br, _ := arc.GetBranch(ctx, "main")
	fmt.Println("main head:", br.Latest().Head())
}
```

Swap the backend by composing different parts — nothing else changes:

```go
u, _ := fs.New("data/universe")                         // adapter/fs
bth, _ := ranke.NewFsBranchTableHead("data/B_h")
arc, _ := ranke.NewArchive(ctx, u, bth)
```

## Architecture

The pieces compose explicitly — no per-backend factories, so the shape of
a deployment is visible at the call site:

| Piece | Spec | What it is |
|---|---|---|
| `Claim` | §4.1–4.3 | A node plus its edges, atomically created and immutable. Built with `ClaimBuilder`; its id is the signature over its canonical encoding. |
| `Graph` | §4.4 | A set of claims with provenance walking, validation, and consolidation. |
| `Universe` | §4.5 | Content-addressed store of claims and content bytes; no branches. |
| `BranchTableHead` | §4.7 | The single mutable id of the current branches claim. |
| `Archive` | §4.8 | The `(𝒰, B_h)` tuple — `NewArchive(u, bth)`. Owns neither dependency, so Archives can share a Universe. |

**Persistence is adapter-shaped.** The domain types are core; a `Universe`
binding to a backing store is an adapter implemented strictly against the
public API. Each lives in its own package:

- `adapter/mem` — ephemeral, map-backed (`mem.New()`).
- `adapter/fs` — flat-directory filesystem (`fs.New(dir)`).
- downstream S3 / Neo4j / SQL satisfy the same interface.

Records are content-addressed with IPFS multihash (SHA-256) over CBOR
Deterministic encoding (RFC 8949 §4.2). Serialization (`Claim.Encode` /
`DecodeClaim`) and content integrity (`VerifyContent`) are storage-agnostic,
so adapters move opaque bytes and never touch the internal representation.

Writing your own adapter? Implement `ranke.Universe` and delegate the
cross-universe copy to `adapter.DefaultCopyClaims` / `DefaultCopyContents`.

## Build & test

```sh
make            # run the tests
make build      # verify the library compiles
make test-verbose
make docs       # re-fetch the spec and papers into docs/papers/ (gitignored)
make verify     # build, gofmt, lint, citations, scenarios
```

`make verify` checks the rule ids comments cite — a backticked `V-…` or `R-…` —
against the spec's own declarations, so it reads `docs/papers/`. That directory is
fetched rather than committed, and `verify` brings it up to the ranke-graph ref
first: one `git ls-remote` against the commit stamped in `docs/papers/.ranke-graph-sha`,
cloning only when the ref has moved. A gate that cannot see the spec fails rather
than passing, and so does one that cannot establish the copy's age — an expiring
cache reads green against whatever it happens to hold. Working without the network
is a deliberate ask: `RANKE_DOCS_OFFLINE=1` keeps the copy on disk, and `RANKE_SPEC`
points the citation gates at one of your own.

It also reproduces each conformance scenario and diffs the result against the
committed bundle, which is what holds the claim ids in
`conformance/scenarios/*/data_reference/` to a value. That regenerates
`conformance/scenarios/*/data/` in your working tree — generated output that
`.gitignore` covers and `make clean` removes. After an intentional change to a
scenario or to anything that moves an id, promote the new bundle with
`make update-references`, then read the diff and confirm each scenario still
reports every claim valid.

The shared black-box suite in `adapter/adaptertest` runs against any
`Universe`; `adapter/fs` and `adapter/mem` each wrap it, and `fs` adds
medium-specific tests (corrupted/truncated files). End-to-end scenarios
drawn from the paper live under `conformance/scenarios/`.

## License

Apache 2.0 — see `LICENSE`.
