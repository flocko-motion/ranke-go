package ranke

import (
	"context"
	"errors"
	"io"
	"sync"
)

// NewMemUniverse returns an ephemeral, in-memory Universe.
func NewMemUniverse() Universe {
	return &memUniverse{
		claims:  make(map[string]Claim),
		content: make(map[string][]byte),
	}
}

type memUniverse struct {
	mu      sync.RWMutex
	claims  map[string]Claim
	content map[string][]byte
}

func (u *memUniverse) Close() error { return nil }

func (u *memUniverse) LoadClaim(_ context.Context, id Id) (Claim, error) {
	if id == nil {
		return nil, errors.New("ranke.memUniverse.LoadClaim: nil id")
	}
	u.mu.RLock()
	defer u.mu.RUnlock()
	c, ok := u.claims[id.String()]
	if !ok {
		return nil, ErrNotFound
	}
	return c, nil
}

func (u *memUniverse) SaveClaim(_ context.Context, c Claim) error {
	if c == nil || c.ID() == nil {
		return errors.New("ranke.memUniverse.SaveClaim: nil claim or id")
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	k := c.ID().String()
	if _, ok := u.claims[k]; !ok {
		u.claims[k] = c
	}
	return nil
}

func (u *memUniverse) HasClaim(_ context.Context, id Id) (bool, error) {
	if id == nil {
		return false, nil
	}
	u.mu.RLock()
	defer u.mu.RUnlock()
	_, ok := u.claims[id.String()]
	return ok, nil
}

func (u *memUniverse) GetContent(_ context.Context, hash Id, _ uint64) ([]byte, error) {
	if hash == nil {
		return nil, errors.New("ranke.memUniverse.GetContent: nil hash")
	}
	u.mu.RLock()
	defer u.mu.RUnlock()
	b, ok := u.content[hash.String()]
	if !ok {
		return nil, ErrNotFound
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out, nil
}

func (u *memUniverse) StreamContent(ctx context.Context, hash Id, size uint64) (io.ReadCloser, error) {
	b, err := u.GetContent(ctx, hash, size)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytesReader(b)), nil
}

func (u *memUniverse) SaveContent(_ context.Context, hash Id, content []byte) error {
	if hash == nil {
		return errors.New("ranke.memUniverse.SaveContent: nil hash")
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	k := hash.String()
	if _, ok := u.content[k]; !ok {
		cp := make([]byte, len(content))
		copy(cp, content)
		u.content[k] = cp
	}
	return nil
}

func (u *memUniverse) HasContent(_ context.Context, hash Id) (bool, error) {
	if hash == nil {
		return false, nil
	}
	u.mu.RLock()
	defer u.mu.RUnlock()
	_, ok := u.content[hash.String()]
	return ok, nil
}

type bytesReaderT struct {
	b   []byte
	pos int
}

func bytesReader(b []byte) *bytesReaderT { return &bytesReaderT{b: b} }

func (r *bytesReaderT) Read(p []byte) (int, error) {
	if r.pos >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.pos:])
	r.pos += n
	return n, nil
}

