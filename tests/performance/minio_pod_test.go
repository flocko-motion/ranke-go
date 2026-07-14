// package: tests/performance / integration
// type:    test
// job:     spawn a real MinIO (S3-compatible) instance in a podman pod for the s3 matrix row, tear it down after — so s3 runs against a real object store on the wire, not an in-process fake
// limits:  requires podman on PATH and network to pull the image; skips the s3 row when podman is absent (e.g. this sandbox). mem/fs/sqlite need no pod and do not use this.
package performance

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"
)

const (
	minioImage = "docker.io/minio/minio:latest"
	minioUser  = "minioadmin"
	minioPass  = "minioadmin"
	minioReady = 30 * time.Second
)

// minioPod starts a MinIO container in a podman pod, waits for it to become
// healthy, creates one empty bucket, and returns an S3 client pointed at it.
// The pod is removed at test end. When podman is unavailable the row skips —
// the matrix stays honest (an S3-on-the-wire row that did not run says so)
// rather than falling back to an in-process fake.
func minioPod(t *testing.T) (*awss3.Client, string) {
	t.Helper()
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("podman not on PATH; skipping s3 backend (needs a real object-store pod)")
	}

	const bucket = "ranke-perf"
	port := freePort(t)
	name := fmt.Sprintf("ranke-perf-minio-%d", port)

	// One MinIO container in its own pod, host port → MinIO's 9000. --rm so a
	// crash cleans itself; we also force-remove in cleanup.
	runCtx, cancel := context.WithTimeout(context.Background(), minioReady)
	defer cancel()
	out, err := exec.CommandContext(runCtx, "podman", "run", "-d", "--rm",
		"--name", name,
		"-p", fmt.Sprintf("127.0.0.1:%d:9000", port),
		"-e", "MINIO_ROOT_USER="+minioUser,
		"-e", "MINIO_ROOT_PASSWORD="+minioPass,
		minioImage, "server", "/data",
	).CombinedOutput()
	require.NoError(t, err, "podman run minio: %s", strings.TrimSpace(string(out)))
	t.Cleanup(func() {
		_ = exec.Command("podman", "rm", "-f", name).Run()
	})

	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitReady(t, endpoint)

	cfg := aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(minioUser, minioPass, ""),
	}
	client := awss3.NewFromConfig(cfg, func(o *awss3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true // MinIO serves path-style
	})
	_, err = client.CreateBucket(context.Background(), &awss3.CreateBucketInput{
		Bucket: aws.String(bucket),
	})
	require.NoError(t, err, "create bucket")
	return client, bucket
}

// freePort grabs an ephemeral port by binding :0 and releasing it — the pod
// then maps its host side to this port.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// waitReady polls MinIO's liveness endpoint until it answers 200 or the
// deadline passes.
func waitReady(t *testing.T, endpoint string) {
	t.Helper()
	deadline := time.Now().Add(minioReady)
	for time.Now().Before(deadline) {
		resp, err := http.Get(endpoint + "/minio/health/live")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("minio did not become ready at %s within %s", endpoint, minioReady)
}
