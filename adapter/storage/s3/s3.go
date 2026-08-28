// package: s3 / persistence
// type:    adapter
// job:     stores claims and content blobs as objects in an S3 bucket
// limits:  no indexing or codec logic; a BlobStore behind storage.NewBlobUniverse (-> adapter)
//
// Package s3 keys claims and blobs by their id strings in one bucket, the
// object-store analogue of the fs adapter's flat directory. Open (storage.Streamer)
// streams large content off the response body. New takes a configured client, so
// tests point one at an in-process fake (gofakes3).
package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/rankegraph/ranke-go"
	"github.com/rankegraph/ranke-go/adapter/storage"
)

// New assumes the bucket already exists and probes its capabilities (see
// probeCaps); pass ReadOnly for a WORM bucket so the probe never writes.
var (
	errNilClient   = errors.New("adapter/s3.New: nil client")
	errEmptyBucket = errors.New("adapter/s3.New: empty bucket")
	errIO          = errors.New("adapter/s3: io")
)

// New returns an S3-backed Universe over the given client and bucket.
func New(client *s3.Client, bucket string, opts ...Option) (ranke.Universe, error) {
	if client == nil {
		return nil, errNilClient
	}
	if bucket == "" {
		return nil, errEmptyBucket
	}
	cfg := config{concurrency: defaultConcurrency}
	for _, o := range opts {
		o(&cfg)
	}
	// NewBlobUniverse defaults a byte store to the authoritative tier, which S3
	// is: a durable, verbatim source of truth.
	return storage.NewBlobUniverse(&store{
		client: client,
		bucket: bucket,
		caps:   probeCaps(context.Background(), client, bucket, cfg.readOnly),
	}, storage.WithConcurrency(cfg.concurrency)), nil
}

type store struct {
	client *s3.Client
	bucket string
	caps   ranke.Capabilities
}

type config struct {
	readOnly    bool
	concurrency int
}

// defaultConcurrency is how many objects bulk ops transfer in parallel: S3 has
// no multi-object API, so throughput comes from concurrent single-object calls.
const defaultConcurrency = 16

// Option configures an s3 store.
type Option func(*config)

// WithConcurrency sets how many objects the bulk operations transfer in
// parallel, hiding S3's per-request latency; n<=1 forces sequential.
func WithConcurrency(n int) Option { return func(c *config) { c.concurrency = n } }

// ReadOnly declares the bucket read-only, append-only, or object-locked, so the
// probe writes no sentinel: a write probe cannot tell those cases apart.
func ReadOnly() Option { return func(c *config) { c.readOnly = true } }

// sentinelKey is the harmless object key the New-time capability probe writes.
const sentinelKey = "ranke-graph-sentinel"

// probeCaps learns the bucket's capabilities: a listing probes Enumerate, two
// sentinel PUTs separate read-write from append-only, then a DELETE cleans up.
func probeCaps(ctx context.Context, client *s3.Client, bucket string, readOnly bool) ranke.Capabilities {
	caps := ranke.Capabilities{Persistent: true}
	if _, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  aws.String(bucket),
		MaxKeys: aws.Int32(1),
	}); err == nil {
		caps.Enumerate = true
	}
	if readOnly {
		return caps
	}
	put := func(body string) error {
		_, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(sentinelKey),
			Body:   bytes.NewReader([]byte(body)),
		})
		return err
	}
	if put("ranke capability probe 1") == nil {
		caps.Overwrite = put("ranke capability probe 2") == nil
		_, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(sentinelKey),
		})
		caps.Delete = err == nil
	}
	return caps
}

// isNotFound reports whether err is S3's "object does not exist", typed or by code.
func isNotFound(err error) bool {
	var nsk *types.NoSuchKey
	var nf *types.NotFound
	if errors.As(err, &nsk) || errors.As(err, &nf) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound", "404":
			return true
		}
	}
	return false
}

func (s *store) Get(ctx context.Context, key string) ([]byte, error) {
	body, err := s.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("%w: read body %s: %w", errIO, key, err)
	}
	return data, nil
}

// Open implements storage.Streamer: the raw GetObject body, so content streams.
func (s *store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, ranke.ErrNotFound
		}
		return nil, fmt.Errorf("%w: get %s: %w", errIO, key, err)
	}
	return out.Body, nil
}

// Put stores key when absent, via a conditional write (If-None-Match: "*"): keys
// are content-addressed, so a 412 means identical bytes are there — a dedup hit.
func (s *store) Put(ctx context.Context, key string, data []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		IfNoneMatch: aws.String("*"),
	})
	if err != nil {
		if isPreconditionFailed(err) {
			return nil // already present — content-addressed, identical bytes
		}
		return fmt.Errorf("%w: put %s: %w", errIO, key, err)
	}
	return nil
}

// isPreconditionFailed reports whether err is S3's 412: the object already exists.
func isPreconditionFailed(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "PreconditionFailed", "412":
			return true
		}
	}
	return false
}

// Delete removes the object; S3 reports success for a key that was not there, which
// is the idempotence the port asks for.
func (s *store) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("%w: delete %s: %w", errIO, key, err)
	}
	return nil
}

func (s *store) Has(ctx context.Context, key string) (bool, error) {
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("%w: head %s: %w", errIO, key, err)
	}
	return true, nil
}

func (s *store) Close() error { return nil }

// Capabilities returns the bucket's capabilities as probed at New (see probeCaps).
func (s *store) Capabilities() ranke.Capabilities {
	return s.caps
}
