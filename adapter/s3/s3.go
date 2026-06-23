// package: s3 / persistence
// type:    adapter
// job:     stores claims and content blobs as objects in an S3 bucket
// limits:  no indexing or codec logic; a BlobStore behind adapter.NewBlobUniverse (-> adapter)
//
// Package s3 is an S3 persistence adapter for a ranke Universe. It stores
// claims and content blobs as objects in a single bucket, keyed by their
// id strings — the object-store analogue of the fs adapter's flat
// directory. It is a thin adapter.BlobStore — the claim/content/copy
// machinery comes from adapter.NewBlobUniverse — plus an Open method
// (adapter.Streamer) so large content streams from the response body
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
	"github.com/flocko-motion/ranke-go/adapter"
)

// New returns a Universe backed by the given bucket on client. The bucket
// is assumed to already exist; the adapter does not create or own it (nor
// the client — Close is a no-op).
func New(client *s3.Client, bucket string) (ranke.Universe, error) {
	if client == nil {
		return nil, errors.New("adapter/s3.New: nil client")
	}
	if bucket == "" {
		return nil, errors.New("adapter/s3.New: empty bucket")
	}
	return adapter.NewBlobUniverse(&store{client: client, bucket: bucket}), nil
}

type store struct {
	client *s3.Client
	bucket string
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

// Open implements adapter.Streamer: the raw GetObject body, so content
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

// Put stores key. Idempotent: the key is content-addressed, so re-putting
// writes the same bytes.
func (s *store) Put(ctx context.Context, key string, data []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	})
	if err != nil {
		return fmt.Errorf("put %s: %w", key, err)
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
		return false, fmt.Errorf("head %s: %w", key, err)
	}
	return true, nil
}

func (s *store) Close() error { return nil }
