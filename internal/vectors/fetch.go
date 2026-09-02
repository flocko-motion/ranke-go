// package: internal/vectors / fetch
// type:    io
// job:     downloads and unpacks the published reference-artifact bundle, cached and revalidated so
// an unchanged release costs one conditional request
// limits:  transport only; the schema is manifest.go's and running the cases is check.go's
package vectors

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// LatestURL serves the newest published artifact set. The asset name carries no
// version, so this URL is stable while what it returns moves.
const LatestURL = "https://github.com/rankegraph/ranke-graph/releases/latest/download/ranke-testdata.tar.gz"

// maxBundle caps what will be read from the network, so a wrong URL cannot fill the
// disk. The real set is a few kilobytes.
const maxBundle = 32 << 20

var (
	errFetch      = errors.New("vectors.Fetch")
	errNoManifest = errors.New("vectors.Fetch: bundle holds no " + Name)
	errEscapes    = errors.New("vectors.Fetch: entry escapes the destination")
	errNoCache    = errors.New("vectors.Fetch: server answered 304 but nothing is cached")
)

// Fetch unpacks the bundle at url into dest and returns the directory holding its
// manifest. The bytes are cached, so a run that finds the release unchanged transfers
// nothing; an unreachable release is an error, never a stale answer.
func Fetch(ctx context.Context, url, dest string) (string, error) {
	body, err := load(ctx, url)
	if err != nil {
		return "", err
	}
	return unpack(bytes.NewReader(body), dest)
}

// load returns the bundle's bytes, revalidating the cache against the server. A 304
// means the cached copy is still the latest release.
func load(ctx context.Context, url string) ([]byte, error) {
	body, etag := cachePaths(url)
	cached, cacheErr := os.ReadFile(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errFetch, err)
	}
	// Only claim a cached copy when there is one to fall back on.
	if cacheErr == nil {
		if tag, err := os.ReadFile(etag); err == nil && len(tag) > 0 {
			req.Header.Set("If-None-Match", string(tag))
		}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errFetch, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotModified:
		if cacheErr != nil {
			return nil, errNoCache
		}
		return cached, nil
	case http.StatusOK:
		fresh, err := io.ReadAll(io.LimitReader(resp.Body, maxBundle))
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errFetch, err)
		}
		store(body, etag, fresh, resp.Header.Get("ETag"))
		return fresh, nil
	default:
		return nil, fmt.Errorf("%w: %s answered %s", errFetch, url, resp.Status)
	}
}

// cachePaths names the cached body and its validator, keyed by url so two sources
// never collide.
func cachePaths(url string) (body, etag string) {
	sum := sha256.Sum256([]byte(url))
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	base := filepath.Join(dir, "ranke-go", "vectors", hex.EncodeToString(sum[:8]))
	return base + ".tar.gz", base + ".etag"
}

// store writes the cache, best effort: a cache that cannot be written costs a
// download next time and nothing else.
func store(body, etag string, data []byte, tag string) {
	if err := os.MkdirAll(filepath.Dir(body), 0o755); err != nil {
		return
	}
	if err := os.WriteFile(body, data, 0o644); err != nil {
		return
	}
	if tag == "" {
		_ = os.Remove(etag) // no validator: revalidation would 200 anyway
		return
	}
	_ = os.WriteFile(etag, []byte(tag), 0o644)
}

// unpack extracts a gzipped tar into dest and reports where the manifest landed.
func unpack(r io.Reader, dest string) (string, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errFetch, err)
	}
	defer gz.Close()

	root := ""
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("%w: %w", errFetch, err)
		}
		name := filepath.Clean(h.Name)
		if filepath.IsAbs(name) || strings.HasPrefix(name, "..") {
			return "", fmt.Errorf("%w: %s", errEscapes, h.Name)
		}
		target := filepath.Join(dest, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return "", fmt.Errorf("%w: %w", errFetch, err)
			}
		case tar.TypeReg:
			if err := writeEntry(tr, target, h.Size); err != nil {
				return "", err
			}
			if filepath.Base(name) == Name {
				root = filepath.Dir(target)
			}
		}
	}
	if root == "" {
		return "", errNoManifest
	}
	return root, nil
}

// writeEntry copies exactly size bytes to target, so a lying header cannot run away
// with the disk.
func writeEntry(r io.Reader, target string, size int64) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("%w: %w", errFetch, err)
	}
	f, err := os.Create(target)
	if err != nil {
		return fmt.Errorf("%w: %w", errFetch, err)
	}
	defer f.Close()
	if _, err := io.CopyN(f, r, size); err != nil {
		return fmt.Errorf("%w: %w", errFetch, err)
	}
	return nil
}
