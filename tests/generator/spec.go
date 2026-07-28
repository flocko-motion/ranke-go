// package: tests/generator / testkit
// type:    tool
// job:     Spec — the full knob set the generator builds from, plus SpecForSize which derives every knob from a single `size`
// limits:  pure configuration + derivation, no building (-> generate.go); knobs the ranke API cannot yet express are marked BLOCKED and ignored by the builder
package generator

import "time"

// Spec is the complete, fine-tunable description of a graph to generate. The
// intended use is SpecForSize(seed, size) for a one-knob call, then override
// individual fields when a test wants to stress one corner. Every field is a
// deterministic input: the same Spec yields the same archive, ids included.
//
// The builder bakes in the "kitchen-sink" corners so a single generated
// archive exercises them all at once: multiple contributors, sources with
// tiny AND large inline blobs, external (hash-addressed) content, derivation
// chains, entities and the relations between them (both relation_direction
// polarities), long contribution/diff chains, high- and low-degree nodes,
// oversized field values, and multi-head consolidation.
type Spec struct {
	// Seed is the master seed. It fixes every derived key (via keyForIndex),
	// every content blob, and thus every id. Change it for an independent but
	// equally-deterministic archive.
	Seed int64
	// Size is the headline knob SpecForSize scales the rest from. It is
	// retained for reference/manifest; the builder reads the derived fields.
	Size int

	// Base is the clock's starting instant; the builder stamps created_at from
	// a clock advancing off it. Fixed (never time.Now) for reproducibility.
	Base time.Time
	// Step is how far the clock advances per claim.
	Step time.Duration

	// --- population knobs (all counts) ---

	// Contributors is how many distinct signing contributors author claims
	// (besides the operator the Sequencer signs branch tables with).
	Contributors int
	// Sources is the count of source/* claims (ingested artifacts).
	Sources int
	// Derivations is the count of derivation/* claims, each citing one or more
	// sources via derivation edges.
	Derivations int
	// Entities is the count of entity/* claims.
	Entities int
	// Relations is the count of relation/* claims, each wiring two entities
	// with a from/to edge pair.
	Relations int

	// --- shape knobs ---

	// DiffChainLen is the length of the longest contribution/diff chain: one
	// base source plus this many revisions overlaying it.
	DiffChainLen int
	// ExternalBlobs is how many source claims carry external (hash-addressed)
	// content in the Universe rather than inline bytes.
	ExternalBlobs int
	// TinyBlobBytes / LargeBlobBytes bound the inline content sizes, so the
	// archive holds both a tiny-blob and a large-blob corner.
	TinyBlobBytes  int
	LargeBlobBytes int
	// OversizedFieldBytes is the length of a large field value placed on one
	// claim (0 disables the corner). Kept under the ADT's field-value cap
	// (64 KiB) — a big-but-valid field, not an over-limit one.
	OversizedFieldBytes int
	// MaxEdgeDegree is the fan-out of the highest-degree derivation: it cites
	// up to this many sources at once (the high-degree corner); most nodes
	// stay low-degree.
	MaxEdgeDegree int

	// KeyExpiries is how many contribution/expiry claims to mint — each a
	// claim carrying a contribution/expiry edge to a contributor and a
	// pubkey_expires_after field (paper §Types). Just a claim, like everything
	// else in the graph.
	KeyExpiries int
	// Deletes is how many contribution/delete tombstones to mint — each a
	// claim whose contribution/delete edge names a claim by id, documenting a
	// removed-bytes gap (paper §Types). The bytes are not actually purged
	// here; the tombstone claim is the structural corner.
	Deletes int

	// Branches is how many named branches the contributions spread across;
	// 0 derives a count from the claim total. "main" is always the first.
	Branches int
}

// SpecForSize derives a full Spec from one size knob. The scaling is linear
// and deliberately simple; the point is that size=10 gives a small archive and
// size=2000 gives a 10k+-claim one, with every corner present at either scale.
// The shape knobs (blob sizes, diff-chain length, max degree) grow sublinearly
// so a large archive is mostly breadth, not a few monstrous claims.
func SpecForSize(seed int64, size int) Spec {
	if size < 1 {
		size = 1
	}
	return Spec{
		Seed: seed,
		Size: size,
		Base: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Step: time.Second,

		Contributors: clampMin(size/10, 2),
		Sources:      size * 2,
		Derivations:  size,
		Entities:     size,
		Relations:    size,

		DiffChainLen:        clampMin(ilog2(size)*2, 3),
		ExternalBlobs:       clampMin(size/5, 1),
		TinyBlobBytes:       8,
		LargeBlobBytes:      clampMin(size*64, 4096),
		OversizedFieldBytes: 60 * 1024, // large, under the 64 KiB field cap
		MaxEdgeDegree:       clampMin(ilog2(size)+2, 3),

		KeyExpiries: clampMin(size/20, 1),
		// Deletes default off: a contribution/delete tombstone documents purged
		// bytes, but the library is append-only and purges nothing, so at the
		// ADT level it would point at a still-present claim. Deletion is a
		// RankeDB (DB-level) concern; the knob stays for that testing later.
		Deletes: 0,
	}
}

// spineClaims is the near-constant branch-table/head overhead SpecForNodes
// subtracts before dividing, so a node target lands near the real claim count.
const spineClaims = 350

// SpecForNodes scales an archive to roughly nodes total claims — a dimension,
// not an exact count. SpecForSize's content claims run ~5× its unit; the
// sequencer then adds spineClaims of branch-table spine, so we subtract that
// before dividing to land near nodes across the useful range (1k → ~1k,
// 1m → ~1m). This is what the CLI's --size (1k, 1m, …) feeds.
func SpecForNodes(seed int64, nodes int) Spec {
	s := SpecForSize(seed, clampMin((nodes-spineClaims)/5, 1))
	s.Size = nodes // the headline reflects the requested node count, not the unit
	return s
}

func clampMin(v, min int) int {
	if v < min {
		return min
	}
	return v
}

// ilog2 returns floor(log2(n)) for n>=1, else 0 — a cheap sublinear scale.
func ilog2(n int) int {
	l := 0
	for n > 1 {
		n >>= 1
		l++
	}
	return l
}
