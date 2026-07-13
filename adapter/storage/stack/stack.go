// package: stack / persistence
// type:    adapter
// job:     compose ordered Universe layers into one read-through / write-through Universe — cache + durable tiers with self-healing repair (paper §Composing Universes)
// limits:  no storage of its own (-> the layer Universes); naming/selecting layers is the caller's (-> ranke-db config)
//
// Package stack composes several ranke.Universe layers into one, top (cache)
// to bottom (authoritative). A write descends from the top: each eager layer
// stores it, each lazy layer passes it through untouched. A read descends to
// the first layer that has the item and, on the way back up, fills the layers
// it passed (every eager layer — which should hold everything — and every lazy
// layer that opts into read-fill). That fill doubles as repair: because Put
// overwrites (storage.BlobStore), a layer whose bytes were missing or corrupt
// is restored from the good copy below. Corruption is a storage failure the
// stack routes around and heals; a graph-level failure (bad signature) is the
// same on every layer and is left to verification.
package stack

import (
	"context"
	"errors"
	"io"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/adapter/storage"
)

type mode int

const (
	eager mode = iota // stored on write
	lazy              // passed through on write, filled on read
)

// Layer is one tier of a Stack: a Universe plus how the stack populates it.
// Construct with Eager (write-through) or Lazy (read-through cache); never use
// the zero value.
type Layer struct {
	u        ranke.Universe
	mode     mode
	readFill bool   // lazy: fill on a read miss served from below
	maxSize  uint64 // content blobs larger than this are not stored here (0 = no cap)
}

// Option tunes a Layer.
type Option func(*Layer)

// MaxContentSize caps the size, in bytes, of a content blob this layer stores
// (0 = no cap). Claim records are tiny and always stored; this lets a small
// cache tier hold short content (names, notes) while skipping large blobs.
func MaxContentSize(bytes uint64) Option { return func(l *Layer) { l.maxSize = bytes } }

// NoReadFill makes a lazy layer read-only: it is consulted on reads but never
// populated by them (e.g. a tier another instance manages). No effect on an
// eager layer, which is always restored.
func NoReadFill() Option { return func(l *Layer) { l.readFill = false } }

// Eager returns a write-through layer: every write stores it here, so it holds
// the whole archive — a durable/authoritative tier.
func Eager(u ranke.Universe, opts ...Option) Layer {
	l := Layer{u: u, mode: eager}
	for _, o := range opts {
		o(&l)
	}
	return l
}

// Lazy returns a read-through cache layer: writes pass through untouched, and a
// read miss served from below fills it on the way back up (unless NoReadFill).
func Lazy(u ranke.Universe, opts ...Option) Layer {
	l := Layer{u: u, mode: lazy, readFill: true}
	for _, o := range opts {
		o(&l)
	}
	return l
}

// wantsFill reports whether a read should populate this layer from below: every
// eager layer (it should hold everything) and every read-filling lazy layer.
func (l Layer) wantsFill() bool { return l.mode == eager || l.readFill }

// fitsContent reports whether content of the given size belongs in this layer.
func (l Layer) fitsContent(size int) bool { return l.maxSize == 0 || uint64(size) <= l.maxSize }

type stack struct {
	layers []Layer
}

// NewStack composes layers into one Universe, ordered top (first, cache) to
// bottom (last, authoritative). At least one eager (write-through) layer is
// required — otherwise a write would persist nowhere.
func NewStack(layers ...Layer) (ranke.Universe, error) {
	if len(layers) == 0 {
		return nil, errors.New("stack: at least one layer required")
	}
	eagers := 0
	for _, l := range layers {
		if l.u == nil {
			return nil, errors.New("stack: layer has a nil Universe")
		}
		if l.mode == eager {
			eagers++
		}
	}
	if eagers == 0 {
		return nil, errors.New("stack: at least one eager (write-through) layer required")
	}
	return &stack{layers: layers}, nil
}

func (s *stack) PutClaims(ctx context.Context, cs []ranke.Claim) error {
	for _, l := range s.layers {
		if l.mode != eager {
			continue
		}
		if err := l.u.PutClaims(ctx, cs); err != nil {
			return err
		}
	}
	return nil
}

func (s *stack) PutContents(ctx context.Context, blobs []ranke.ContentBlob) error {
	for _, l := range s.layers {
		if l.mode != eager {
			continue
		}
		sel := blobs
		if l.maxSize > 0 {
			sel = nil
			for _, b := range blobs {
				if l.fitsContent(len(b.Content)) {
					sel = append(sel, b)
				}
			}
		}
		if len(sel) == 0 {
			continue
		}
		if err := l.u.PutContents(ctx, sel); err != nil {
			return err
		}
	}
	return nil
}

func (s *stack) GetClaims(ctx context.Context, ids []ranke.Id) ([]ranke.Claim, error) {
	out := make([]ranke.Claim, len(ids))
	pending := seq(len(ids))
	for li := range s.layers {
		if len(pending) == 0 {
			break
		}
		has, err := s.layers[li].u.HasClaims(ctx, pickIds(ids, pending))
		if err != nil {
			return nil, err
		}
		var hitIDs []ranke.Id
		var hitAt []int
		var still []int
		for k, orig := range pending {
			if has[k] {
				hitIDs = append(hitIDs, ids[orig])
				hitAt = append(hitAt, orig)
			} else {
				still = append(still, orig)
			}
		}
		if len(hitIDs) > 0 {
			got, err := s.layers[li].u.GetClaims(ctx, hitIDs)
			if err != nil {
				return nil, err
			}
			for j, c := range got {
				out[hitAt[j]] = c
			}
			s.fillClaims(ctx, li, got)
		}
		pending = still
	}
	if len(pending) > 0 {
		return nil, ranke.ErrNotFound
	}
	return out, nil
}

func (s *stack) GetContents(ctx context.Context, refs []ranke.ContentRef) ([][]byte, error) {
	out := make([][]byte, len(refs))
	pending := seq(len(refs))
	for li := range s.layers {
		if len(pending) == 0 {
			break
		}
		has, err := s.layers[li].u.HasContents(ctx, pickHashes(refs, pending))
		if err != nil {
			return nil, err
		}
		var hitRefs []ranke.ContentRef
		var hitAt []int
		var still []int
		for k, orig := range pending {
			if has[k] {
				hitRefs = append(hitRefs, refs[orig])
				hitAt = append(hitAt, orig)
			} else {
				still = append(still, orig)
			}
		}
		if len(hitRefs) > 0 {
			got, err := s.layers[li].u.GetContents(ctx, hitRefs)
			switch {
			case errors.Is(err, ranke.ErrIntegrity):
				// Corrupt bytes at this layer — a storage failure. Descend to a
				// good copy and repair on the way back; keep these pending.
				still = append(still, hitAt...)
			case err != nil:
				return nil, err
			default:
				for j, b := range got {
					out[hitAt[j]] = b
				}
				s.fillContents(ctx, li, hitRefs, got)
			}
		}
		pending = still
	}
	if len(pending) > 0 {
		return nil, ranke.ErrNotFound
	}
	return out, nil
}

func (s *stack) HasClaims(ctx context.Context, ids []ranke.Id) ([]bool, error) {
	out := make([]bool, len(ids))
	pending := seq(len(ids))
	for li := range s.layers {
		if len(pending) == 0 {
			break
		}
		has, err := s.layers[li].u.HasClaims(ctx, pickIds(ids, pending))
		if err != nil {
			return nil, err
		}
		var still []int
		for k, orig := range pending {
			if has[k] {
				out[orig] = true
			} else {
				still = append(still, orig)
			}
		}
		pending = still
	}
	return out, nil
}

func (s *stack) HasContents(ctx context.Context, hashes []ranke.Id) ([]bool, error) {
	out := make([]bool, len(hashes))
	pending := seq(len(hashes))
	for li := range s.layers {
		if len(pending) == 0 {
			break
		}
		has, err := s.layers[li].u.HasContents(ctx, pickIds(hashes, pending))
		if err != nil {
			return nil, err
		}
		var still []int
		for k, orig := range pending {
			if has[k] {
				out[orig] = true
			} else {
				still = append(still, orig)
			}
		}
		pending = still
	}
	return out, nil
}

func (s *stack) StreamContent(ctx context.Context, hash ranke.Id, size uint64) (io.ReadCloser, error) {
	for li := range s.layers {
		ok, err := ranke.HasContent(ctx, s.layers[li].u, hash)
		if err != nil {
			return nil, err
		}
		if ok {
			return s.layers[li].u.StreamContent(ctx, hash, size)
		}
	}
	return nil, ranke.ErrNotFound
}

func (s *stack) CopyClaims(ctx context.Context, src ranke.Universe, ids []ranke.Id, opts ...ranke.CopyOption) error {
	return storage.DefaultCopyClaims(ctx, s, src, ids, opts...)
}

func (s *stack) CopyContents(ctx context.Context, src ranke.Universe, refs []ranke.ContentRef, opts ...ranke.CopyOption) error {
	return storage.DefaultCopyContents(ctx, s, src, refs, opts...)
}

// InClosure reports whether id is in head's closure.
//
// TODO: implement — delegate to the most capable layer (a graph engine
// answers natively); otherwise fall through the layers like a read.
func (s *stack) InClosure(ctx context.Context, head, id ranke.Id) (bool, error) {
	panic("TODO: stack.InClosure not implemented")
}

// GetFromClosure returns the claim at id if it is in head's closure.
//
// TODO: implement — delegate to the layer that answers InClosure.
func (s *stack) GetFromClosure(ctx context.Context, head, id ranke.Id) (ranke.Claim, error) {
	panic("TODO: stack.GetFromClosure not implemented")
}

// Capabilities derives the stack's from its layers, per capability:
//   - Overwrite: every eager layer can (repair must reach the durable tier);
//   - Delete: every layer can (else a deleted key lingers in some layer);
//   - Enumerate: some eager layer can (an eager layer holds the whole keyset);
//   - Persistent: some eager layer is (the durable tier survives);
//   - GQL: some layer offers it (a query routes to that layer).
func (s *stack) Capabilities() ranke.Capabilities {
	overwrite, del := true, true
	var enumerate, persistent, gql bool
	for _, l := range s.layers {
		c := l.u.Capabilities()
		if !c.Delete {
			del = false
		}
		if c.GQL {
			gql = true
		}
		if l.mode == eager {
			if !c.Overwrite {
				overwrite = false
			}
			if c.Enumerate {
				enumerate = true
			}
			if c.Persistent {
				persistent = true
			}
		}
	}
	return ranke.Capabilities{
		Overwrite:  overwrite,
		Delete:     del,
		Enumerate:  enumerate,
		Persistent: persistent,
		GQL:        gql,
	}
}

// Close closes every layer (best-effort: all are attempted, errors joined).
func (s *stack) Close() error {
	var errs []error
	for _, l := range s.layers {
		if err := l.u.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// fillClaims writes claims served from a lower layer into every fill-wanting
// layer above it — restoring eager layers and populating read-fill caches.
// Best-effort: a cache write failure must not fail the read.
func (s *stack) fillClaims(ctx context.Context, servedAt int, cs []ranke.Claim) {
	for i := 0; i < servedAt; i++ {
		if s.layers[i].wantsFill() {
			_ = s.layers[i].u.PutClaims(ctx, cs)
		}
	}
}

// fillContents is fillClaims for content, honouring each layer's size cap.
func (s *stack) fillContents(ctx context.Context, servedAt int, refs []ranke.ContentRef, datas [][]byte) {
	for i := 0; i < servedAt; i++ {
		l := s.layers[i]
		if !l.wantsFill() {
			continue
		}
		var blobs []ranke.ContentBlob
		for j, r := range refs {
			if l.fitsContent(len(datas[j])) {
				blobs = append(blobs, ranke.ContentBlob{Hash: r.Hash, Content: datas[j]})
			}
		}
		if len(blobs) > 0 {
			_ = l.u.PutContents(ctx, blobs)
		}
	}
}

func seq(n int) []int {
	s := make([]int, n)
	for i := range s {
		s[i] = i
	}
	return s
}

func pickIds(ids []ranke.Id, idx []int) []ranke.Id {
	out := make([]ranke.Id, len(idx))
	for i, j := range idx {
		out[i] = ids[j]
	}
	return out
}

func pickHashes(refs []ranke.ContentRef, idx []int) []ranke.Id {
	out := make([]ranke.Id, len(idx))
	for i, j := range idx {
		out[i] = refs[j].Hash
	}
	return out
}
