// package: sqlite / persistence
// type:    adapter
// job:     stores claims and content blobs as rows in a single SQLite table
// limits:  no indexing or codec logic; a BlobStore behind storage.NewBlobUniverse (-> adapter)
//
// Package sqlite keys claims and blobs by their id strings in one flat table, the
// database analogue of the fs adapter's directory. The pure-Go modernc.org/sqlite
// driver backs it, so it needs neither cgo nor external infrastructure.
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

var (
	errEmptyDSN = errors.New("adapter/sqlite.New: empty dsn")
	errNew      = errors.New("adapter/sqlite.New")
	errIO       = errors.New("adapter/sqlite: io")
)

// New opens a sqlite-backed Universe at dsn, creating its schema if absent. In
// memory, use "file:mem?mode=memory&cache=shared" — bare ":memory:" gives each
// pooled connection a database of its own.
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

// probeWritable takes a write lock with BEGIN IMMEDIATE and rolls it back, on one
// dedicated connection so the pair is not split across the pool.
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

// Put stores data under key by INSERT OR REPLACE. Keys are content-addressed, so the
// overwrite repairs a corrupted row in place; callers dedup via Has.
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

// Capabilities returns what New probed: persistence from the DSN, overwrite and
// delete from the write-lock probe.
func (s *store) Capabilities() ranke.Capabilities {
	return s.caps
}
