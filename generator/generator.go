// package: generator / testkit
// type:    tool
// job:     deterministic "kitchen-sink" Ranke-Graph generator for integration + conformance testing — one size knob, every corner baked in
// limits:  builds via the public ranke API; the caller supplies the Universe to store into (-> adapter); does not assert (-> tests)
package generator

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"time"

	"github.com/flocko-motion/ranke-go"
)

// The generator is fully deterministic: a given (seed, size) yields the exact
// same graph — same claims, same ids — on every run and, crucially, across
// the Go and Python reference implementations. That rules out any language
// PRNG (Go's math/rand ≠ Python's) and time.Now: keys, timestamps, and
// content are all spec'd functions of the seed, reproducible in any language.

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
// byte-identical content — hence identical content hashes and ids.
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
// stamped at `at`. The returned Contributor carries the key, so claims
// attributed to it sign automatically.
func contributor(ctx context.Context, master int64, index int, at time.Time) (ranke.Contributor, error) {
	priv := keyForIndex(master, index)
	pubkey, err := ranke.EncodePublicKey(priv.Public())
	if err != nil {
		return nil, err
	}
	c, err := ranke.NewClaim(ranke.NodeContributor, nil).
		WithInlineContent(pubkey).
		WithCreatedAt(at).
		Sign(priv)
	if err != nil {
		return nil, err
	}
	// Inline pubkey → no Universe needed to resolve it.
	return c.AsContributor(ctx, nil, priv)
}
