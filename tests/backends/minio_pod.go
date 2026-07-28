// package: tests/backends / integration
// type:    tool
// job:     spawn a real MinIO (S3-compatible) instance in a podman pod for the s3 matrix row, tear it down after — so s3 runs against a real object store on the wire, not an in-process fake
// limits:  requires podman on PATH and network to pull the image; returns ErrUnavailable when podman is absent (e.g. this sandbox). mem/fs/sqlite need no pod.
package backends

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
)

const (
	minioImage = "docker.io/minio/minio:latest"
	minioUser  = "minioadmin"
	minioPass  = "minioadmin"
	minioReady = 30 * time.Second
)

// minioPod starts a MinIO container in a podman pod, waits for it to serve,
// creates one empty bucket, and returns an S3 client plus a cleanup func that
// removes the pod. Returns ErrUnavailable when podman is not on PATH, so the
// matrix skips the row rather than falling back to an in-process fake.
func minioPod() (*awss3.Client, string, func(), error) {
	if _, err := exec.LookPath("podman"); err != nil {
		return nil, "", nil, fmt.Errorf("%w: podman not on PATH (s3 needs a real object-store pod)", ErrUnavailable)
	}

	const bucket = "ranke-perf"
	port, err := freePort()
	if err != nil {
		return nil, "", nil, err
	}
	name := fmt.Sprintf("ranke-perf-minio-%d", port)

	runCtx, cancel := context.WithTimeout(context.Background(), minioReady)
	defer cancel()
	out, err := exec.CommandContext(runCtx, "podman", "run", "-d", "--rm",
		"--name", name,
		"-p", fmt.Sprintf("127.0.0.1:%d:9000", port),
		"-e", "MINIO_ROOT_USER="+minioUser,
		"-e", "MINIO_ROOT_PASSWORD="+minioPass,
		minioImage, "server", "/data",
	).CombinedOutput()
	if err != nil {
		return nil, "", nil, fmt.Errorf("podman run minio: %v: %s", err, strings.TrimSpace(string(out)))
	}
	cleanup := func() { _ = exec.Command("podman", "rm", "-f", name).Run() }

	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	if err := waitReady(endpoint); err != nil {
		cleanup()
		return nil, "", nil, err
	}

	cfg := aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(minioUser, minioPass, ""),
	}
	client := awss3.NewFromConfig(cfg, func(o *awss3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true // MinIO serves path-style
	})
	if _, err := client.CreateBucket(context.Background(), &awss3.CreateBucketInput{
		Bucket: aws.String(bucket),
	}); err != nil {
		cleanup()
		return nil, "", nil, fmt.Errorf("create bucket: %w", err)
	}
	// Serve-readiness gate: HeadBucket until it answers, so pod warm-up never
	// bleeds into the measured window.
	if err := waitServing(client, bucket); err != nil {
		cleanup()
		return nil, "", nil, err
	}
	return client, bucket, cleanup, nil
}

// freePort grabs an ephemeral port by binding :0 and releasing it.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// waitReady polls MinIO's readiness endpoint until 200 or the deadline.
func waitReady(endpoint string) error {
	deadline := time.Now().Add(minioReady)
	for time.Now().Before(deadline) {
		resp, err := http.Get(endpoint + "/minio/health/ready")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("minio not ready at %s within %s", endpoint, minioReady)
}

// waitServing blocks until HeadBucket succeeds — a real S3 round-trip
// confirming the pod serves requests, not merely that it is up.
func waitServing(client *awss3.Client, bucket string) error {
	deadline := time.Now().Add(minioReady)
	for {
		_, err := client.HeadBucket(context.Background(), &awss3.HeadBucketInput{
			Bucket: aws.String(bucket),
		})
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("minio bucket %s not serving within %s: %w", bucket, minioReady, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
