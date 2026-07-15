// This file adds the BlobStore seam: every byte-oriented Universe backend
// (mem, fs, sqlite, s3, …) differs only in how it stores and fetches
// opaque bytes by key. The claim/content/copy machinery — codec, integrity
// checks, bulk loops — is identical across all of them and lives here, so
// a new adapter only has to implement three primitives.

// package: adapter / blobstore
// type:    logic
// job:     the BlobStore seam — three byte primitives (Get/Put/Has) become a full ranke.Universe
// limits:  no storage of its own; concrete blob backends live in sub-packages (-> adapter/fs, adapter/mem)
package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"

	"github.com/flocko-motion/ranke-go"
)

// BlobStore is the minimal backing store a Universe adapter must provide:
// opaque bytes addressed by a string key. Claims and content share one
// namespace (keys are id strings — claim ids and content hashes). Implement
// these three operations and NewBlobUniverse supplies the rest of
// ranke.Universe for free.
type BlobStore interface {
	// Get returns the bytes stored under key, or ranke.ErrNotFound if the
	// key is absent.
	Get(ctx context.Context, key string) ([]byte, error)
	// Put stores data under key, overwriting any existing bytes. Keys are
	// content-addressed, so a normal re-put writes identical bytes; the
	// overwrite is what lets a read-through repair restore a corrupted
	// entry. Callers that want to skip a redundant write check Has first.
	Put(ctx context.Context, key string, data []byte) error
	// Has reports whether key is present.
	Has(ctx context.Context, key string) (bool, error)
	// Capabilities reports what this backend can do (see ranke.Capabilities) —
	// its nature (durable medium? can it delete / enumerate?), not merely the
	// three primitives it exposes here. NewBlobUniverse surfaces it as the
	// Universe's capabilities.
	Capabilities() ranke.Capabilities
	// Close releases any resources the store holds.
	Close() error
}

// Streamer is an optional BlobStore extension for backends that can hand
// back a raw byte stream without buffering the whole blob (a file handle,
// an HTTP response body, …). When a store implements it, NewBlobUniverse
// uses Open for StreamContent — wrapping the raw stream in a verifying
// reader — instead of loading the entire blob via Get. Stores that don't
// implement it fall back to Get + buffer, which is fine for small blobs.
type Streamer interface {
	// Open returns a raw, unverified byte stream for key, or
	// ranke.ErrNotFound if absent. The caller wraps it for integrity.
	Open(ctx context.Context, key string) (io.ReadCloser, error)
}

// NewBlobUniverse adapts a BlobStore into a full ranke.Universe, supplying
// the claim codec, content integrity checks, bulk loops, and the default
// copy walkers — everything that is identical across byte-oriented
// backends. A complete adapter is then just a BlobStore implementation.
//
//deadcode:keep
func NewBlobUniverse(store BlobStore, opts ...BlobUniverseOption) ranke.Universe {
	u := &blobUniverse{store: store, concurrency: 1, heights: ranke.NewHeightCache()}
	for _, o := range opts {
		o(u)
	}
	if u.concurrency > 1 {
		// One semaphore for the whole Universe — the concurrency cap is a
		// TOTAL across every (possibly concurrent) call, not per call.
		u.sem = make(chan struct{}, u.concurrency)
	}
	return u
}

// BlobUniverseOption configures the Universe wrapper.
type BlobUniverseOption func(*blobUniverse)

// WithConcurrency caps how many per-key store operations run at once — a
// TOTAL over the whole Universe, shared across all concurrent calls, so N
// simultaneous bulk calls never exceed n in-flight requests. n<=1 keeps ops
// sequential (the default). Network backends (s3, rest) set it to fan out
// over request latency without overwhelming the endpoint; local backends
// leave it sequential.
func WithConcurrency(n int) BlobUniverseOption {
	return func(u *blobUniverse) {
		if n > 0 {
			u.concurrency = n
		}
	}
}

type blobUniverse struct {
	store       BlobStore
	concurrency int
	// sem bounds concurrent store operations across the whole Universe (nil
	// when concurrency<=1 → sequential). Shared by every forEach call, so the
	// cap is a total, not per-call.
	sem chan struct{}
	// heights memoises id→height so GetClaimHeights rarely re-decodes a claim.
	// Warmed opportunistically by GetClaims/PutClaims (every claim handled is
	// Noted), so it fills with normal traffic. Heights are immutable, so the
	// memo never goes stale.
	heights *ranke.HeightCache
}

// forEach runs fn for each index in [0,n). When the Universe has a shared
// concurrency semaphore, up to that many run at once — the cap is a TOTAL
// across all concurrent forEach calls on this Universe, not per call — else
// it runs sequentially. fn must write its result to a distinct per-index slot
// (no shared state); the first error is returned and cancels the rest.
func (u *blobUniverse) forEach(ctx context.Context, n int, fn func(context.Context, int) error) error {
	if u.sem == nil {
		for i := 0; i < n; i++ {
			if err := fn(ctx, i); err != nil {
				return err
			}
		}
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		ferr error
	)
launch:
	for i := 0; i < n; i++ {
		select {
		case <-ctx.Done(): // cancelled (or a sibling failed) while waiting for a slot
			break launch
		case u.sem <- struct{}{}: // acquire a shared slot
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-u.sem }()
			if err := fn(ctx, i); err != nil {
				mu.Lock()
				if ferr == nil {
					ferr = err
					cancel()
				}
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if ferr != nil {
		return ferr
	}
	return ctx.Err()
}

//deadcode:keep
func (u *blobUniverse) Close() error { return u.store.Close() }

//deadcode:keep
func (u *blobUniverse) GetClaims(ctx context.Context, ids []ranke.Id, opts ...ranke.GetOption) ([]ranke.Claim, error) {
	out := make([]ranke.Claim, len(ids))
	err := u.forEach(ctx, len(ids), func(ctx context.Context, i int) error {
		id := ids[i]
		if id == nil {
			return errors.New("adapter.GetClaims: nil id")
		}
		data, err := u.store.Get(ctx, id.String())
		if err != nil {
			return err
		}
		c, err := ranke.DecodeClaim(id, data)
		if err != nil {
			return err
		}
		out[i] = c
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Warm the height cache from what we just decoded (heights are on the raw
	// claims, before materialisation), so later GetClaimHeights avoids a reload.
	u.heights.NoteClaims(out...)
	// A byte store returns raw claims; the read path materialises diff overlays
	// via the ADT default, honouring WithNotDiffMaterialized (passed through).
	// Materialisation runs after the parallel fetch, so it sees complete claims.
	return ranke.DefaultMaterialize(ctx, u, out, opts...)
}

//deadcode:keep
func (u *blobUniverse) PutClaims(ctx context.Context, cs []ranke.Claim) error {
	if err := u.forEach(ctx, len(cs), func(ctx context.Context, i int) error {
		c := cs[i]
		if c == nil || c.ID() == nil {
			return errors.New("adapter.PutClaims: nil claim or id")
		}
		data, err := c.Encode()
		if err != nil {
			return err
		}
		return u.store.Put(ctx, c.ID().String(), data)
	}); err != nil {
		return err
	}
	// Every stored claim is one we hold in hand — warm the height cache.
	u.heights.NoteClaims(cs...)
	return nil
}

//deadcode:keep
func (u *blobUniverse) HasClaims(ctx context.Context, ids []ranke.Id) ([]bool, error) {
	return u.hasAll(ctx, ids)
}

//deadcode:keep
func (u *blobUniverse) GetContents(ctx context.Context, refs []ranke.ContentRef) ([][]byte, error) {
	out := make([][]byte, len(refs))
	err := u.forEach(ctx, len(refs), func(ctx context.Context, i int) error {
		ref := refs[i]
		if ref.Hash == nil {
			return errors.New("adapter.GetContents: nil hash")
		}
		data, err := u.store.Get(ctx, ref.Hash.String())
		if err != nil {
			return err
		}
		if err := ranke.VerifyContent(ref.Hash, ref.ContentSize, data); err != nil {
			return err
		}
		out[i] = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

//deadcode:keep
func (u *blobUniverse) StreamContent(ctx context.Context, hash ranke.Id, size uint64) (io.ReadCloser, error) {
	if hash == nil {
		return nil, errors.New("adapter.StreamContent: nil hash")
	}
	// True streaming when the store can hand back a raw stream; otherwise
	// load the whole blob and stream from memory.
	if s, ok := u.store.(Streamer); ok {
		raw, err := s.Open(ctx, hash.String())
		if err != nil {
			return nil, err
		}
		r, err := ranke.NewVerifyingReader(raw, hash, size)
		if err != nil {
			_ = raw.Close()
			return nil, err
		}
		return r, nil
	}
	data, err := u.store.Get(ctx, hash.String())
	if err != nil {
		return nil, err
	}
	return ranke.NewVerifyingReader(io.NopCloser(bytes.NewReader(data)), hash, size)
}

//deadcode:keep
func (u *blobUniverse) PutContents(ctx context.Context, blobs []ranke.ContentBlob) error {
	for _, bl := range blobs {
		if bl.Hash == nil {
			return errors.New("adapter.PutContents: nil hash")
		}
		if err := u.store.Put(ctx, bl.Hash.String(), bl.Content); err != nil {
			return err
		}
	}
	return nil
}

//deadcode:keep
func (u *blobUniverse) HasContents(ctx context.Context, hashes []ranke.Id) ([]bool, error) {
	return u.hasAll(ctx, hashes)
}

// hasAll is the shared body of HasClaims/HasContents — both ask the flat
// key namespace whether each id is present, tolerating nil entries.
func (u *blobUniverse) hasAll(ctx context.Context, ids []ranke.Id) ([]bool, error) {
	out := make([]bool, len(ids))
	err := u.forEach(ctx, len(ids), func(ctx context.Context, i int) error {
		id := ids[i]
		if id == nil {
			return nil
		}
		has, err := u.store.Has(ctx, id.String())
		if err != nil {
			return err
		}
		out[i] = has
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

//deadcode:keep
func (u *blobUniverse) CopyClaims(ctx context.Context, src ranke.Universe, ids []ranke.Id, opts ...ranke.CopyOption) error {
	return ranke.DefaultCopyClaims(ctx, u, src, ids, opts...)
}

//deadcode:keep
func (u *blobUniverse) CopyContents(ctx context.Context, src ranke.Universe, refs []ranke.ContentRef, opts ...ranke.CopyOption) error {
	return ranke.DefaultCopyContents(ctx, u, src, refs, opts...)
}

// Capabilities surfaces the backing store's declared capabilities as the
// Universe's.
//
//deadcode:keep
func (u *blobUniverse) Capabilities() ranke.Capabilities {
	c := u.store.Capabilities()
	c.RawClaims = true       // a blob store keeps each claim's verbatim canonical bytes
	c.ExternalContent = true // and holds externalized content of any size (no cap)
	return c
}

// GetClaimHeights answers from the id→height cache, decoding only the ids it
// has not seen (and caching those). With normal get/put traffic warming the
// cache, a repeat lookup almost never touches the store.
func (u *blobUniverse) GetClaimHeights(ctx context.Context, ids []ranke.Id) ([]uint64, error) {
	return u.heights.GetClaimHeights(ctx, u, ids)
}

// Query answers an RQL read via the reference executor over this byte store; a
// forward-only store, so uses/connections steps are refused (ReverseWalk false).
func (u *blobUniverse) Query(ctx context.Context, q ranke.Query, scope ranke.Scope) (ranke.ResultStream, error) {
	return ranke.DefaultQuery(ctx, u, q, scope)
}

// GetClaimsRaw returns each claim's stored bytes verbatim — a byte store holds
// exactly the CBOR the id was signed over, so no decode (and no re-encode) is
// involved.
//
// GetClaimTags is unsupported: an opaque byte store holds no mutable side-data
// (Capabilities.Tags is false).
//
//deadcode:keep
func (u *blobUniverse) GetClaimTags(_ context.Context, _ []ranke.Id) ([]map[string]string, error) {
	return nil, ranke.ErrUnsupported
}

// SetClaimsTags is unsupported (see GetClaimTags).
//
//deadcode:keep
func (u *blobUniverse) SetClaimsTags(_ context.Context, _ []string, _ map[string]map[string]string) error {
	return ranke.ErrUnsupported
}

//deadcode:keep
func (u *blobUniverse) GetClaimsRaw(ctx context.Context, ids []ranke.Id) ([][]byte, error) {
	out := make([][]byte, len(ids))
	err := u.forEach(ctx, len(ids), func(ctx context.Context, i int) error {
		id := ids[i]
		if id == nil {
			return errors.New("adapter.GetClaimsRaw: nil id")
		}
		data, err := u.store.Get(ctx, id.String())
		if err != nil {
			return err
		}
		out[i] = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
