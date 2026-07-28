// package: tests/generator / testkit
// type:    tool
// job:     deterministic "kitchen-sink" Ranke-Graph generator for integration + conformance testing — one size knob, every corner baked in
// limits:  builds via the public ranke API; the caller supplies the Universe to store into (-> adapter); does not assert (-> tests)
package generator

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"math"
	"strings"
	"time"

	"github.com/brianvoe/gofakeit/v7"
	"github.com/flocko-motion/ranke-go"
)

// The generator is deterministic within the Go reference implementation: a
// given (seed, size) yields the same graph — same claims, same ids — on every
// run and across storage backends, so perf hashes and conformance vectors are
// stable. It avoids time.Now (see Clock); keys and content are spec'd SHA-256
// recipes of the seed and names come from a gofakeit seeded the same way. Go is
// the reference implementation; Python only needs the ADT and a verifier, so it
// does not regenerate this output and the recipes need not be language-neutral.

// --- deterministic keys -------------------------------------------------

// keySeed derives the 32-byte Ed25519 seed for contributor #index from the
// master seed: SHA-256( little-endian uint64(master) ‖ uvarint(index) ). It
// is deliberately a plain, spec'd byte recipe so a Python generator derives
// byte-identical seeds — hence identical keys, signatures, and ids.
func keySeed(master int64, index int) [sha256.Size]byte {
	var buf [8 + binary.MaxVarintLen64]byte
	binary.LittleEndian.PutUint64(buf[:8], uint64(master))
	n := binary.PutUvarint(buf[8:], uint64(index))
	return sha256.Sum256(buf[:8+n])
}

// keyForIndex derives contributor #index's deterministic Ed25519 private key.
func keyForIndex(master int64, index int) ed25519.PrivateKey {
	s := keySeed(master, index)
	return ed25519.NewKeyFromSeed(s[:])
}

// --- deterministic content ---------------------------------------------

// fill returns n bytes deterministically derived from (master, tag, index):
// SHA-256 in counter mode over master ‖ tag ‖ index ‖ counter. Like the key
// recipe it is a plain byte construction, so a Python generator produces
// byte-identical content — hence identical ids (and, for external content,
// identical content hashes).
func fill(master int64, tag string, index, n int) []byte {
	if n <= 0 {
		return nil
	}
	out := make([]byte, 0, n)
	var hdr [8 + binary.MaxVarintLen64]byte
	binary.LittleEndian.PutUint64(hdr[:8], uint64(master))
	m := binary.PutUvarint(hdr[8:], uint64(index))
	prefix := append(append([]byte{}, hdr[:8+m]...), tag...)
	for ctr := uint64(0); len(out) < n; ctr++ {
		var cb [binary.MaxVarintLen64]byte
		c := binary.PutUvarint(cb[:], ctr)
		block := sha256.Sum256(append(append([]byte{}, prefix...), cb[:c]...))
		out = append(out, block[:]...)
	}
	return out[:n]
}

// --- deterministic clock ------------------------------------------------

// Clock hands out monotonically increasing timestamps from a fixed base —
// never time.Now — so created_at (which feeds the id) is reproducible while
// still satisfying the §Claims monotonicity rule. One Clock is the single
// time source shared by the generator, the testkit History, and the testkit
// Sequencer, so the whole run advances on one deterministic timeline.
type Clock struct {
	t    time.Time
	step time.Duration
}

// NewClock returns a Clock at base advancing by step per tick.
func NewClock(base time.Time, step time.Duration) *Clock { return &Clock{t: base, step: step} }

// Now returns the current time without advancing.
func (c *Clock) Now() time.Time { return c.t }

// Tick returns the current time and advances the clock by one step.
func (c *Clock) Tick() time.Time {
	t := c.t
	c.t = c.t.Add(c.step)
	return t
}

// --- deterministic contributors ----------------------------------------

// contributor builds a deterministic signed root contributor for index: its
// multikey-encoded pubkey is its content (§5.7), signed by the derived key,
// stamped at `at`, and named with a gofakeit name seeded from the same
// (master, index) recipe so the name — hence the id — is stable across runs.
// The returned Contributor carries the key, so claims attributed to it sign
// automatically.
func contributor(ctx context.Context, master int64, index int, at time.Time) (ranke.Contributor, error) {
	priv := keyForIndex(master, index)
	pubkey, err := ranke.EncodePublicKey(priv.Public())
	if err != nil {
		return nil, err
	}
	c, err := ranke.NewClaim(ranke.NodeContributor, nil).
		WithInlineContent(pubkey).
		WithEncoding(ranke.EncodingOctetStream). // a multikey pubkey is raw bytes
		WithField(ranke.FieldName, contributorName(master, index)).
		WithCreatedAt(at).
		Sign(priv)
	if err != nil {
		return nil, err
	}
	// Inline pubkey → no Universe needed to resolve it.
	return c.AsContributor(ctx, nil, priv)
}

// contributorName picks a stable random name for contributor #index. gofakeit
// is seeded from the same SHA-256 (master, index) recipe as the key, so the
// name is reproducible run to run (within the Go reference generator).
func contributorName(master int64, index int) string {
	s := keySeed(master, index)
	f := gofakeit.New(binary.LittleEndian.Uint64(s[:8]))
	return f.Name()
}

// contentSeed derives a gofakeit seed for a claim's content from (master, tag,
// index) — the same construction as fill/keySeed, so content is reproducible
// run to run (Go reference only; see the package note).
func contentSeed(master int64, tag string, index int) uint64 {
	var hdr [8 + binary.MaxVarintLen64]byte
	binary.LittleEndian.PutUint64(hdr[:8], uint64(master))
	m := binary.PutUvarint(hdr[8:], uint64(index))
	b := append(append([]byte{}, hdr[:8+m]...), tag...)
	s := sha256.Sum256(b)
	return binary.LittleEndian.Uint64(s[:8])
}

// textFor returns deterministic legible text for a claim's content. Content is
// knowledge (§Everything Is Knowledge), so a source note or a derivation
// summary is human-readable text (text/plain) — not binary fill.
func textFor(master int64, tag string, index int) string {
	return gofakeit.New(contentSeed(master, tag, index)).Sentence(12)
}

// nameFor returns a deterministic person name for an entity/person's content.
func nameFor(master int64, index int) string {
	return gofakeit.New(contentSeed(master, "person", index)).Name()
}

// --- deterministic branches --------------------------------------------

// branchCount is how many branches a graph of claimCount claims carries: at
// least 2 (a single-branch archive is a degenerate shape), growing ~
// logarithmically with size (≈2 at 100 claims, ≈10 at 100k). Cross-branch
// propagation is out of scope (paper 2); branches exist so branch tagging and
// branch-scoped reads have something to exercise.
func branchCount(claimCount int) int {
	if claimCount < 1 {
		claimCount = 1
	}
	c := int(2.5 * (math.Log10(float64(claimCount)) - 1))
	if c < 2 {
		return 2
	}
	return c
}

// branchNames returns the branch names for a graph of claimCount claims: "main"
// plus deterministic gofakeit words, seeded so they are stable run to run.
func branchNames(claimCount int, seed int64) []string {
	names := []string{"main"}
	f := gofakeit.New(contentSeed(seed, "branch", 0))
	seen := map[string]bool{"main": true}
	for len(names) < branchCount(claimCount) {
		w := strings.ToLower(f.Word())
		if w == "" || seen[w] {
			continue
		}
		seen[w] = true
		names = append(names, w)
	}
	return names
}
