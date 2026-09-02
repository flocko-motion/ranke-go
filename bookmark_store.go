// package: ranke / bookmark_store
// type:    io
// job:     the 𝒰_hist interface — bookmark records by id_seq(i, s) — and NewMemoryBookmarks, the
// in-process reference implementation
// limits:  opaque bytes by key and nothing else; the record's shape and its rules are
// bookmark.go's, the list over a store bookmarks.go's (-> bookmark, bookmarks)
package ranke

import (
	"bytes"
	"context"
	"sync"
)

// BookmarkStore is 𝒰_hist, the bookmark store: records under id_seq(i, s), a keyspace
// defined apart from 𝒰 and freely co-located with it physically (foundation paper
// §Bookmarks). Its guarantees are deliberately weaker than 𝒰's — a bookmark is only a
// locator, so an entry may be overwritten or purged.
type BookmarkStore interface {
	// Get returns the record at key, ErrNotFound where the slot holds nothing.
	Get(ctx context.Context, key Id) ([]byte, error)
	// Put stores record at key, replacing whatever the slot held.
	Put(ctx context.Context, key Id, record []byte) error
}

// NewMemoryBookmarks returns an ephemeral in-process BookmarkStore, the reference
// implementation: the stored bytes verbatim, keyed by id.
func NewMemoryBookmarks() BookmarkStore {
	return &memoryBookmarks{records: make(map[string][]byte)}
}

type memoryBookmarks struct {
	mu      sync.RWMutex
	records map[string][]byte
}

func (s *memoryBookmarks) Get(_ context.Context, key Id) ([]byte, error) {
	if key == nil {
		return nil, errBookmarkNilSlot
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.records[key.String()]
	if !ok {
		return nil, ErrNotFound
	}
	return bytes.Clone(record), nil
}

func (s *memoryBookmarks) Put(_ context.Context, key Id, record []byte) error {
	if key == nil {
		return errBookmarkNilSlot
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[key.String()] = bytes.Clone(record)
	return nil
}
