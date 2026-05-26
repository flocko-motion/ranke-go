// Package ranke is the Go reference implementation of the
// Ranke-Graph ADT (spec §4).
//
// Architecture follows the spec directly:
//
//   - Universe (𝒰, §4.5) — a content-addressed bag of claims and
//     content bytes. Composable: NewFsUniverse, NewMemUniverse, plus
//     downstream backends (S3, Neo4j, ...) that satisfy the Universe
//     interface.
//
//   - BranchTableHead (B_h, §4.7) — persists the single mutable Id of
//     the current contribution/branches claim. NewFsBranchTableHead,
//     NewMemBranchTableHead, or anything else that satisfies it.
//
//   - Archive (§4.8) — the (𝒰, B_h) tuple. NewArchive(u, bth) composes
//     them. No per-backend factories: callers compose explicitly so
//     the shape of the deployment is visible at the call site.
//
// Multiple Archives may share one Universe. Archive does not own
// either of its dependencies — closing an Archive does not close
// the Universe or BranchTableHead. The caller does.
//
// Higher-level concerns (queries, indices, cache stacks, federation)
// live in the application layer above this library.
//
// Interface declarations live with their concrete implementations
// (Universe in universe.go, Archive in archive.go, Claim in claim.go,
// etc.) — there is no central interfaces.go.
package ranke
