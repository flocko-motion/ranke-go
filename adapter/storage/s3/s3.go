// package: s3 / persistence
// type:    adapter
// job:     stores claims and content blobs as objects in an S3 bucket
// limits:  no indexing or codec logic; a BlobStore behind storage.NewBlobUniverse (-> adapter)
//
// Package s3 is an S3 persistence adapter for a ranke Universe. It stores
// claims and content blobs as objects in a single bucket, keyed by their
// id strings — the object-store analogue of the fs adapter's flat
// directory. It is a thin storage.BlobStore — the claim/content/copy
// machinery comes from storage.NewBlobUniverse — plus an Open method
// (storage.Streamer) so large content streams from the response body
// without buffering.
//
// New takes an already-configured *s3.Client so the adapter stays free of
// credential/endpoint concerns: production wires a real client, tests wire
// one pointed at an in-process fake (gofakes3).
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

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/adapter/storage"
)

// New returns a Universe backed by the given bucket on client. The bucket is
// assumed to already exist; the adapter does not create or own it (nor the
// client — Close is a no-op). New learns the bucket's capabilities with a
// sentinel probe (see probeCaps). Pass ReadOnly for a bucket you may only read
// (or one under object-lock/WORM) so the probe never writes.
func New(client *s3.Client, bucket string, opts ...Option) (ranke.Universe, error) {
	if client == nil {
		return nil, errors.New("adapter/s3.New: nil client")
	}
	if bucket == "" {
		return nil, errors.New("adapter/s3.New: empty bucket")
	}
	cfg := config{concurrency: defaultConcurrency}
	for _, o := range opts {
		o(&cfg)
	}
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

// defaultConcurrency is how many objects the bulk Universe ops fetch/store in
// parallel by default — S3 has no synchronous multi-object API, so throughput
// comes from concurrent single-object requests over the per-request latency.
const defaultConcurrency = 16

// Option configures an s3 store.
type Option func(*config)

// WithConcurrency sets how many objects the bulk operations
// (GetClaims/PutClaims/HasClaims/GetContents) transfer in parallel. Higher
// widths hide S3's per-request latency at the cost of more in-flight
// requests; n<=1 forces sequential. Defaults to defaultConcurrency.
func WithConcurrency(n int) Option { return func(c *config) { c.concurrency = n } }

// ReadOnly declares the bucket read-only (or append-only / object-locked): the
// probe never writes a sentinel — which on a WORM bucket could never be removed
// — and the Universe reports no Overwrite or Delete. A write probe cannot tell
// read-only from append-only (a first PUT of a new key succeeds on the latter),
// so the operator states it.
func ReadOnly() Option { return func(c *config) { c.readOnly = true } }

// sentinelKey is a fixed, harmless object key used once at New to learn what
// the bucket allows.
const sentinelKey = "ranke-graph-sentinel"

// probeCaps learns the bucket's capabilities. Listing (Enumerate) is a read and
// is always probed. When readOnly is set, no write is attempted. Otherwise it
// PUTs the sentinel twice — the second PUT is a real overwrite, which separates
// a read-write bucket from append-only/object-lock — then DeleteObject probes
// delete and cleans up. S3 storage is durable, so Persistent is intrinsic; a
// failed probe just leaves that capability false and never fails New.
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

// isNotFound reports whether err is S3's "object does not exist", across
// the typed GetObject/HeadObject errors and the generic API error code.
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
		return nil, fmt.Errorf("read body %s: %w", key, err)
	}
	return data, nil
}

// Open implements storage.Streamer: the raw GetObject body, so content
// streams from S3 instead of being read whole into memory.
func (s *store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, ranke.ErrNotFound
		}
		return nil, fmt.Errorf("get %s: %w", key, err)
	}
	return out.Body, nil
}

// Put stores key only when it is absent, via a conditional write
// (If-None-Match: "*"). Keys are content-addressed, so a present key already
// holds identical bytes — the conditional write dedups it away in a single
// request instead of re-uploading the object. A 412 Precondition Failed
// means the key was already there, which is success (a skipped write), not an
// error. (Overwriting a corrupted entry — read-through repair — is a separate
// forced write, not this path.)
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
		return fmt.Errorf("put %s: %w", key, err)
	}
	return nil
}

// isPreconditionFailed reports whether err is S3's 412 for a failed
// If-None-Match — i.e. the object already exists.
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

func (s *store) Has(ctx context.Context, key string) (bool, error) {
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("head %s: %w", key, err)
	}
	return true, nil
}

func (s *store) Close() error { return nil }

// Capabilities returns the bucket's capabilities as probed at New (see probeCaps).
func (s *store) Capabilities() ranke.Capabilities {
	return s.caps
}
