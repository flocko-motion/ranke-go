// package: redis / persistence-cache
// type:    adapter
// job:     stores claims and content blobs as keys in redis — a fast, shared, in-memory byte cache tier
// limits:  a storage.BlobStore behind storage.NewBlobUniverse (-> adapter); not authoritative — a
// cache under a query layer and above a durable store (paper §Composing Universes)
//
// Package redis stores claims (by id) and content (by hash) as opaque string values.
// Keys are content-addressed and immutable, so a cached value stays correct and
// capacity is redis's own concern (maxmemory + eviction), with an optional per-key
// TTL. New takes a configured client it does not own, so Close is a no-op.
package redis

import (
	"context"
	"errors"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/rankegraph/ranke-go"
	"github.com/rankegraph/ranke-go/adapter/storage"
)

var errNilClient = errors.New("adapter/redis.New: nil client")

// defaultConcurrency pipelines bulk key transfers to hide the network hop.
const defaultConcurrency = 16

// New returns a Universe backed by client, whose connection, auth, and
// database stay the caller's concern.
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

// WithTTL sets a per-key expiry on writes; default 0 leaves eviction to redis's
// maxmemory policy. Content addressing makes any TTL safe — expiry means refetch.
func WithTTL(d time.Duration) Option { return func(c *config) { c.ttl = d } }

// WithKeyPrefix namespaces every key (e.g. "ranke:"), so one redis instance
// can host several archives or coexist with other data. Default "".
func WithKeyPrefix(p string) Option { return func(c *config) { c.prefix = p } }

// WithTier sets the write role redis serves in a stack (Capabilities.Tier);
// default lazy fills on read misses. Verbatim claims make any tier valid.
func WithTier(t ranke.StorageTier) Option { return func(c *config) { c.tier = t } }

// WithConcurrency sets how many keys the bulk operations transfer in parallel;
// n<=1 forces sequential.
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

// Put stores data under key with the configured TTL, overwriting any value there.
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

// Delete removes key (DEL), which reports 0 for an absent key rather than failing.
func (s *store) Delete(ctx context.Context, key string) error {
	return s.client.Del(ctx, s.key(key)).Err()
}

// Capabilities: redis overwrites (SET), deletes (DEL), and enumerates (SCAN),
// and serves as a cache tier whatever AOF/RDB the server runs.
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
