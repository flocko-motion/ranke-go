// package: sqlite / persistence
// type:    adapter
// job:     stores claims and content blobs as rows in a single SQLite table
// limits:  no indexing or codec logic; a BlobStore behind storage.NewBlobUniverse (-> adapter)
//
// Package sqlite is a SQLite persistence adapter for a ranke Universe. It
// stores claims and content blobs as rows in one flat table, addressed by
// their id strings — the database analogue of the fs adapter's flat
// directory. It is a thin storage.BlobStore; the claim/content/copy
// machinery comes from storage.NewBlobUniverse.
//
// Backed by the pure-Go modernc.org/sqlite driver, so it needs no cgo and
// no external infrastructure.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/adapter/storage"

	_ "modernc.org/sqlite"
)

// New opens (creating if needed) a SQLite-backed Universe at dsn. dsn is a
// modernc.org/sqlite data source name — typically a file path or
// "file:..." URI. For an in-memory store use a temp file or a shared-cache
// URI ("file:mem?mode=memory&cache=shared"); a bare ":memory:" gives each
// pooled connection its own database and will not behave as one store.
var (
	errEmptyDSN = errors.New("adapter/sqlite.New: empty dsn")
	errNew      = errors.New("adapter/sqlite.New")
	errIO       = errors.New("adapter/sqlite: io")
)

// New opens a sqlite-backed Universe at dsn, creating its schema if absent.
func New(dsn string) (ranke.Universe, error) {
	if dsn == "" {
		return nil, errEmptyDSN
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("%w: open %s: %w", errNew, dsn, err)
	}
	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("%w: connect %s: %w", errNew, dsn, err)
	}
	writable := probeWritable(ctx, db)
	if writable {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS blobs (
			id   TEXT PRIMARY KEY,
			data BLOB NOT NULL
		)`); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("%w: create schema: %w", errNew, err)
		}
	}
	caps := ranke.Capabilities{
		Overwrite:  writable,
		Delete:     writable,
		Enumerate:  true, // a SELECT works on any readable database
		Persistent: persistentDSN(dsn),
	}
	return storage.NewBlobUniverse(&store{db: db, caps: caps}), nil
}

type store struct {
	db   *sql.DB
	caps ranke.Capabilities
}

// probeWritable tests write access without mutating: BEGIN IMMEDIATE takes a
// write lock (failing at once on a read-only database), then ROLLBACK releases
// it. It uses one dedicated connection so the BEGIN/ROLLBACK pair is not split
// across the pool.
func probeWritable(ctx context.Context, db *sql.DB) bool {
	conn, err := db.Conn(ctx)
	if err != nil {
		return false
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return false
	}
	_, _ = conn.ExecContext(ctx, "ROLLBACK")
	return true
}

// persistentDSN reports whether the DSN names durable storage rather than an
// in-memory database (":memory:" or "mode=memory").
func persistentDSN(dsn string) bool {
	d := strings.ToLower(dsn)
	return !strings.Contains(d, ":memory:") && !strings.Contains(d, "mode=memory")
}

func (s *store) Get(ctx context.Context, key string) ([]byte, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM blobs WHERE id = ?`, key).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ranke.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("%w: read %s: %w", errIO, key, err)
	}
	return data, nil
}

// Put stores data under key, overwriting any existing row (INSERT OR REPLACE).
// Content-addressed, so a normal re-put writes identical bytes; the overwrite
// is what repairs a corrupted row in place. Callers dedup via Has.
func (s *store) Put(ctx context.Context, key string, data []byte) error {
	if _, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO blobs (id, data) VALUES (?, ?)`, key, data); err != nil {
		return fmt.Errorf("%w: write %s: %w", errIO, key, err)
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
		return false, fmt.Errorf("%w: stat %s: %w", errIO, key, err)
	}
	return true, nil
}

func (s *store) Close() error { return s.db.Close() }

// Capabilities returns what New probed: persistence from the DSN (file vs
// in-memory) and overwrite/delete from a write-lock probe (a read-only database
// reports neither).
func (s *store) Capabilities() ranke.Capabilities {
	return s.caps
}
