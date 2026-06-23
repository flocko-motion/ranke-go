// Package ranke is the Go reference implementation of the Ranke-Graph
// ADT (spec §4): a content-addressed, provenance-carrying claim graph.
//
// The pieces compose explicitly, mirroring the spec:
//
//   - Claim (§4.1–4.3) — a node plus its edges, atomically created and
//     immutable. Build one with ClaimBuilder; a claim's id is the
//     signature over its canonical encoding.
//   - Universe (𝒰, §4.5) — a content-addressed store of claims and
//     content bytes, with no notion of branches. The in-memory form is
//     core (NewMemUniverse via adapter/mem); persistence adapters live
//     one-per-package under adapter/ (adapter/fs, plus downstream S3,
//     Neo4j, ...), each implemented against this package's public API.
//   - BranchTableHead (B_h, §4.7) — the single mutable Id of the current
//     branches claim (NewFsBranchTableHead, NewMemBranchTableHead, ...).
//   - Archive (§4.8) — the (𝒰, B_h) tuple, composed by NewArchive(u, bth).
//     It owns neither dependency, so multiple Archives can share one
//     Universe; closing an Archive closes nothing underneath it.
//
// See README.md for a runnable walkthrough.

// package: ranke / doc
// type:    doc
// job:     package-level orientation: the core types and how they compose
// limits:  no code; runnable examples live in README.md; per-file docs in their own files
package ranke
