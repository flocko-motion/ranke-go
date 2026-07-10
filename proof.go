// package: ranke / proof
// type:    data
// job:     SealedProof + Receipt — the tokens passed between Archive.Prepare (verify, off-lock) and Sequencer.Submit (advance B_h, serial)
// limits:  mints nothing itself (-> archive.Prepare); advances no state (-> sequencer.Submit)
package ranke

import "time"

// Height is a monotone commit counter for a Ranke-Archive: the length
// of the contribution/branches chain (B_h history). The Sequencer owns
// it as the single writer; each Submit advances it by one. A SealedProof
// is stamped with the Height its snapshot was pinned at, so Submit can
// tell whether the archive moved under it (§Sequencer).
type Height int64

// SealedProof is the verified, height-stamped result of Archive.Prepare:
// the contribution has been persisted into the Universe and its delta
// verified against the branch's prior head. Submit consumes it to
// advance B_h.
//
// It is opaque and un-forgeable in-process: the concrete type is
// unexported and the interface carries an unexported marker, so only
// Archive.Prepare can mint one. (A cross-process deployment would add a
// MAC over the fields; in-process the type system is the seal.)
type SealedProof interface {
	// Height is the archive height the proof was prepared against.
	Height() Height
	// Branch is the branch the contribution targets.
	Branch() string
	// Head is the id of the client-signed contribution/head claim that
	// consolidates the contribution's open heads — the head Submit
	// binds the branch to (directly, or via a server consolidation when
	// the branch moved).
	Head() Id
	// ContainsLimiting reports whether the contribution carries a
	// limiting claim (early key expiry, revocation), the only case that
	// makes validity non-monotonic and forces Submit's re-verify path.
	ContainsLimiting() bool

	sealed()
}

// sealedProof is the sole SealedProof implementation. priorHead is the
// branch head captured when the proof was prepared (nil for a new
// branch); Submit compares it against the branch head under the lock to
// detect a concurrent advance.
type sealedProof struct {
	height           Height
	branch           string
	head             Id
	priorHead        Id
	containsLimiting bool
	// createdAt is the contribution's resolved timestamp, carried so
	// Submit stamps the branch-table claim (and any consolidation head)
	// consistently with the contribution/head it binds.
	createdAt time.Time
}

func (p *sealedProof) Height() Height         { return p.height }
func (p *sealedProof) Branch() string         { return p.branch }
func (p *sealedProof) Head() Id               { return p.head }
func (p *sealedProof) ContainsLimiting() bool { return p.containsLimiting }
func (p *sealedProof) sealed()                {}

// Receipt is the outcome of a committed Submit: the new archive height
// and the branch head the contribution now resolves through.
type Receipt interface {
	// Height is the archive height after the commit.
	Height() Height
	// Branch is the branch that was advanced.
	Branch() string
	// Head is the branch's new head id (a server consolidation head when
	// the branch had moved, otherwise the proof's head).
	Head() Id
}

type receipt struct {
	height Height
	branch string
	head   Id
}

func (r *receipt) Height() Height { return r.height }
func (r *receipt) Branch() string { return r.branch }
func (r *receipt) Head() Id       { return r.head }
