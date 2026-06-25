// Package ranke is the Go reference implementation of the Ranke-Graph
// ADT (spec §4): a content-addressed, provenance-carrying claim graph.
//
// The pieces compose explicitly, mirroring the spec:
//
//   - Claim (§4.1–4.3) — a node plus its edges, atomically created and
//     immutable. Build one with ClaimBuilder; a claim's id is the
//     signature over its canonical encoding.
//   - Universe (𝒰, §4.5) — a content-addressed store of claims and
//     content bytes, with no notion of branches. Storage adapters live
//     one-per-package under adapter/storage/ (mem, fs, sqlite, s3, plus
//     downstream backends), each implemented against this package's
//     public API.
//   - BranchTableHead (B_h, §4.7) — the single mutable Id naming the
//     current branch-table revision; the system's sequencing point and a
//     separate seam from the Universe. Backends live one-per-package under
//     adapter/sequencer/ (mem.New, file.New) plus the generic closure
//     injector sequencer.New.
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
