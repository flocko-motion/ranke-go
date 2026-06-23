package s3

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/adapter/adaptertest"
)

// TestConformance runs the shared black-box Universe suite against the s3
// adapter, each universe backed by a fresh in-process gofakes3 server —
// no AWS account, no Docker, no network. Real-S3 fidelity (multipart,
// throttling, eventual consistency) is out of scope here; that belongs in
// an infrastructure-gated test.
func TestConformance(t *testing.T) {
	adaptertest.Run(t, func(t *testing.T) ranke.Universe {
		client, bucket := fakeS3(t)
		u, err := New(client, bucket)
		if err != nil {
			t.Fatalf("s3.New: %v", err)
		}
		return u
	})
}

// fakeS3 spins up an in-process gofakes3 server with one empty bucket and
// returns a client pointed at it. The server is torn down at test end.
func fakeS3(t *testing.T) (*awss3.Client, string) {
	t.Helper()
	const bucket = "ranke-test"

	ts := httptest.NewServer(gofakes3.New(s3mem.New()).Server())
	t.Cleanup(ts.Close)

	cfg := aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
	}
	client := awss3.NewFromConfig(cfg, func(o *awss3.Options) {
		o.BaseEndpoint = aws.String(ts.URL)
		o.UsePathStyle = true // fake serves path-style, not vhost-style
	})

	if _, err := client.CreateBucket(context.Background(), &awss3.CreateBucketInput{
		Bucket: aws.String(bucket),
	}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	return client, bucket
}
