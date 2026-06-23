// package: minimal / persistence
// type:    adapter
// job:     the smallest possible Universe — a map[string][]byte behind the BlobStore seam
// limits:  ephemeral, unsynchronized illustration of the minimum an adapter must implement (-> adapter)
//
// Package minimal is the smallest a ranke Universe adapter can be: a
// map[string][]byte exposed as an storage.BlobStore. The three primitives
// (Get/Put/Has) plus Close are all a byte-oriented backend has to provide;
// storage.NewBlobUniverse supplies the entire ranke.Universe — claim codec,
// content integrity, bulk loops, copy walkers — on top of them.
//
// It is functionally a stripped-down adapter/mem (no defensive copies, no
// locking) and exists to show the floor, not for production use.
//
// This adapter is a proof-of-concept artifact for the paper and is
// deliberately FROZEN: keep it the minimal illustration, do not optimize
// it. For an evolving in-memory backend use adapter/mem, which holds live
// claim objects and is the one that gets optimized over time.
package minimal

import (
	"context"
	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/adapter/storage"
	"sync"
)

// New returns a minimal in-memory Universe — an unsynchronized
// map[string][]byte behind the BlobStore seam.
func New() ranke.Universe {
	return storage.NewBlobUniverse(&store{m: make(map[string][]byte)})
}

type store struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (s *store) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.m[key]
	if !ok {
		return nil, ranke.ErrNotFound
	}
	return b, nil
}

func (s *store) Put(_ context.Context, key string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[key]; !ok {
		s.m[key] = data
	}
	return nil
}

func (s *store) Has(_ context.Context, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.m[key]
	return ok, nil
}

func (s *store) Close() error { return nil }
