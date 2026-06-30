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
func NewBlobUniverse(store BlobStore) ranke.Universe {
	return &blobUniverse{store: store}
}

type blobUniverse struct {
	store BlobStore
}

//deadcode:keep
func (u *blobUniverse) Close() error { return u.store.Close() }

//deadcode:keep
func (u *blobUniverse) GetClaims(ctx context.Context, ids []ranke.Id) ([]ranke.Claim, error) {
	out := make([]ranke.Claim, len(ids))
	for i, id := range ids {
		if id == nil {
			return nil, errors.New("adapter.GetClaims: nil id")
		}
		data, err := u.store.Get(ctx, id.String())
		if err != nil {
			return nil, err
		}
		c, err := ranke.DecodeClaim(id, data)
		if err != nil {
			return nil, err
		}
		out[i] = c
	}
	return out, nil
}

//deadcode:keep
func (u *blobUniverse) PutClaims(ctx context.Context, cs []ranke.Claim) error {
	for _, c := range cs {
		if c == nil || c.ID() == nil {
			return errors.New("adapter.PutClaims: nil claim or id")
		}
		data, err := c.Encode()
		if err != nil {
			return err
		}
		if err := u.store.Put(ctx, c.ID().String(), data); err != nil {
			return err
		}
	}
	return nil
}

//deadcode:keep
func (u *blobUniverse) HasClaims(ctx context.Context, ids []ranke.Id) ([]bool, error) {
	return u.hasAll(ctx, ids)
}

//deadcode:keep
func (u *blobUniverse) GetContents(ctx context.Context, refs []ranke.ContentRef) ([][]byte, error) {
	out := make([][]byte, len(refs))
	for i, ref := range refs {
		if ref.Hash == nil {
			return nil, errors.New("adapter.GetContents: nil hash")
		}
		data, err := u.store.Get(ctx, ref.Hash.String())
		if err != nil {
			return nil, err
		}
		if err := ranke.VerifyContent(ref.Hash, ref.Size, data); err != nil {
			return nil, err
		}
		out[i] = data
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
	for i, id := range ids {
		if id == nil {
			continue
		}
		has, err := u.store.Has(ctx, id.String())
		if err != nil {
			return nil, err
		}
		out[i] = has
	}
	return out, nil
}

//deadcode:keep
func (u *blobUniverse) CopyClaims(ctx context.Context, src ranke.Universe, ids []ranke.Id, opts ...ranke.CopyOption) error {
	return DefaultCopyClaims(ctx, u, src, ids, opts...)
}

//deadcode:keep
func (u *blobUniverse) CopyContents(ctx context.Context, src ranke.Universe, refs []ranke.ContentRef, opts ...ranke.CopyOption) error {
	return DefaultCopyContents(ctx, u, src, refs, opts...)
}

// Capabilities surfaces the backing store's declared capabilities as the
// Universe's.
//
//deadcode:keep
func (u *blobUniverse) Capabilities() ranke.Capabilities {
	return u.store.Capabilities()
}
