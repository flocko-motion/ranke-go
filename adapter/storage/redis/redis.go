// package: redis / persistence-cache
// type:    adapter
// job:     stores claims and content blobs as keys in redis — a fast, shared, in-memory byte cache tier
// limits:  a storage.BlobStore behind storage.NewBlobUniverse (-> adapter); not authoritative — a cache under a query layer and above a durable store (paper §Composing Universes)
//
// Package redis is a redis persistence adapter for a ranke Universe: a fast,
// shared, in-memory byte cache. It stores claims (by id) and content (by hash)
// as opaque redis string values — a thin storage.BlobStore, with the
// claim/content/copy machinery supplied by storage.NewBlobUniverse.
//
// It is a CACHE tier, not authoritative. Keys are content-addressed and
// immutable, so cached values never go stale and no invalidation is ever
// needed; capacity is managed by redis itself (maxmemory + LRU/LFU eviction),
// and an optional per-key TTL bounds staleness in time. Stack it under a
// graph/query layer (-> adapter/storage/neo4j) and above a durable store
// (-> adapter/storage/s3, adapter/storage/fs).
//
// New takes an already-configured *redis.Client so the adapter stays free of
// connection concerns: production wires a real client, tests point one at a
// local or pod redis (services/redis.sh). The adapter does not own the client;
// Close is a no-op.
package redis

import (
	"context"
	"errors"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/adapter/storage"
)

var errNilClient = errors.New("adapter/redis.New: nil client")

// defaultConcurrency is how many keys the bulk Universe ops transfer in
// parallel by default — redis is a network hop, so throughput comes from
// pipelining requests over the per-request latency.
const defaultConcurrency = 16

// New returns a Universe backed by the given redis client. The client's
// connection, auth, and database are the caller's concern; the adapter does
// not own it (Close is a no-op).
func New(client *goredis.Client, opts ...Option) (ranke.Universe, error) {
	if client == nil {
		return nil, errNilClient
	}
	cfg := config{concurrency: defaultConcurrency, tier: ranke.StorageTierLazy}
	for _, o := range opts {
		o(&cfg)
	}
	return storage.NewBlobUniverse(&store{
		client: client,
		ttl:    cfg.ttl,
		prefix: cfg.prefix,
	}, storage.WithConcurrency(cfg.concurrency), storage.WithTier(cfg.tier)), nil
}

type config struct {
	ttl         time.Duration
	prefix      string
	concurrency int
	tier        ranke.StorageTier
}

// Option configures a redis store.
type Option func(*config)

// WithTTL sets a per-key expiry on writes, bounding how long a cached value
// lives. Default 0 = no expiry (values persist until evicted by redis's
// maxmemory policy). Content addressing makes any TTL safe: an expired key is
// simply refetched from the layer below and re-cached.
func WithTTL(d time.Duration) Option { return func(c *config) { c.ttl = d } }

// WithKeyPrefix namespaces every key (e.g. "ranke:"), so one redis instance
// can host several archives or coexist with other data. Default "".
func WithKeyPrefix(p string) Option { return func(c *config) { c.prefix = p } }

// WithTier sets the write role redis serves in a stack (Capabilities.Tier).
// Default is lazy — a read-through cache, populated on a read miss served from
// below and never written on the write path. A deployment that wants redis
// written through (e.g. eager) can say so; redis holds verbatim claims, so any
// tier is allowed.
func WithTier(t ranke.StorageTier) Option { return func(c *config) { c.tier = t } }

// WithConcurrency sets how many keys the bulk operations
// (GetClaims/PutClaims/HasClaims/GetContents) transfer in parallel. n<=1
// forces sequential. Defaults to defaultConcurrency.
func WithConcurrency(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.concurrency = n
		}
	}
}

type store struct {
	client *goredis.Client
	ttl    time.Duration
	prefix string
}

// Compile-time proof the store satisfies the BlobStore seam.
var _ storage.BlobStore = (*store)(nil)

func (s *store) key(k string) string { return s.prefix + k }

// Get returns the bytes stored under key, or ranke.ErrNotFound when absent.
func (s *store) Get(ctx context.Context, key string) ([]byte, error) {
	b, err := s.client.Get(ctx, s.key(key)).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, ranke.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return b, nil
}

// Put stores data under key with the configured TTL, overwriting any existing
// value (a re-put of content-addressed bytes writes identical bytes).
func (s *store) Put(ctx context.Context, key string, data []byte) error {
	return s.client.Set(ctx, s.key(key), data, s.ttl).Err()
}

// Has reports whether key is present.
func (s *store) Has(ctx context.Context, key string) (bool, error) {
	n, err := s.client.Exists(ctx, s.key(key)).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// Capabilities: redis can overwrite (SET), delete (DEL), and enumerate (SCAN).
// Persistent is false — this is a cache tier, not authoritative storage, even
// if the server happens to run with AOF/RDB.
func (s *store) Capabilities() ranke.Capabilities {
	return ranke.Capabilities{
		Overwrite:  true,
		Delete:     true,
		Enumerate:  true,
		Persistent: false,
	}
}

// Close is a no-op: the caller owns the client's lifecycle.
func (s *store) Close() error { return nil }
