package backends

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"
)

// TestS3 validates the s3 test infrastructure end to end against the store named by
// RANKE_S3_ENDPOINT (services/s3.sh, or a CI service container): the bucket handed out
// is fresh and empty, two opens never share one, and cleanup removes it with its
// objects — a bucket left per open is what a shared store would accumulate. The pod
// path needs podman and is not covered here.
func TestS3(t *testing.T) {
	if os.Getenv("RANKE_S3_ENDPOINT") == "" {
		t.Skip("no s3 endpoint (services/s3.sh native up, then set RANKE_S3_ENDPOINT)")
	}
	ctx := context.Background()
	client, bucket, cleanup, err := s3Conn()
	require.NoError(t, err)
	require.NotEmpty(t, bucket)

	list, err := client.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	require.Empty(t, list.Contents, "a fresh bucket carries no objects")

	// A second open against the same store gets its own bucket: one run wiping
	// another's objects is what that prevents.
	_, other, otherCleanup, err := s3Conn()
	require.NoError(t, err)
	require.NotEqual(t, bucket, other)
	otherCleanup()

	// An occupied bucket is the case that matters: S3 refuses to delete one, so the
	// cleanup has to empty it first.
	_, err = client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("claim"), Body: bytes.NewReader([]byte("x")),
	})
	require.NoError(t, err)

	cleanup()
	_, err = client.HeadBucket(ctx, &awss3.HeadBucketInput{Bucket: aws.String(bucket)})
	require.Error(t, err, "cleanup must remove the bucket it created")
}
