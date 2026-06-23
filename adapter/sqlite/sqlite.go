// package: sqlite / persistence
// type:    adapter
// job:     stores claims and content blobs as rows in a single SQLite table
// limits:  no indexing or codec logic; a BlobStore behind adapter.NewBlobUniverse (-> adapter)
//
// Package sqlite is a SQLite persistence adapter for a ranke Universe. It
// stores claims and content blobs as rows in one flat table, addressed by
// their id strings — the database analogue of the fs adapter's flat
// directory. It is a thin adapter.BlobStore; the claim/content/copy
// machinery comes from adapter.NewBlobUniverse.
//
// Backed by the pure-Go modernc.org/sqlite driver, so it needs no cgo and
// no external infrastructure.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/adapter"

	_ "modernc.org/sqlite"
)

// New opens (creating if needed) a SQLite-backed Universe at dsn. dsn is a
// modernc.org/sqlite data source name — typically a file path or
// "file:..." URI. For an in-memory store use a temp file or a shared-cache
// URI ("file:mem?mode=memory&cache=shared"); a bare ":memory:" gives each
// pooled connection its own database and will not behave as one store.
func New(dsn string) (ranke.Universe, error) {
	if dsn == "" {
		return nil, errors.New("adapter/sqlite.New: empty dsn")
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("adapter/sqlite.New: open %s: %w", dsn, err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS blobs (
		id   TEXT PRIMARY KEY,
		data BLOB NOT NULL
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("adapter/sqlite.New: create schema: %w", err)
	}
	return adapter.NewBlobUniverse(&store{db: db}), nil
}

type store struct {
	db *sql.DB
}

func (s *store) Get(ctx context.Context, key string) ([]byte, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM blobs WHERE id = ?`, key).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ranke.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", key, err)
	}
	return data, nil
}

// Put stores key idempotently — INSERT OR IGNORE leaves an existing row
// untouched (content-addressed: the bytes already match).
func (s *store) Put(ctx context.Context, key string, data []byte) error {
	if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO blobs (id, data) VALUES (?, ?)`, key, data); err != nil {
		return fmt.Errorf("write %s: %w", key, err)
	}
	return nil
}

func (s *store) Has(ctx context.Context, key string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM blobs WHERE id = ?`, key).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", key, err)
	}
	return true, nil
}

func (s *store) Close() error { return s.db.Close() }
