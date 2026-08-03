// package: internal/vectors / fetch
// type:    io
// job:     downloads and unpacks the published reference-artifact bundle
// limits:  transport only; the schema is manifest.go's and running the cases is check.go's
package vectors

import (
	"archive/tar"
	"compress/gzip"
	"context"
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
const LatestURL = "https://github.com/flocko-motion/ranke-graph/releases/latest/download/ranke-testdata.tar.gz"

var (
	errFetch      = errors.New("vectors.Fetch")
	errNoManifest = errors.New("vectors.Fetch: bundle holds no " + Name)
	errEscapes    = errors.New("vectors.Fetch: entry escapes the destination")
)

// Fetch downloads the bundle at url into dest, returning the directory that holds
// its manifest — whatever the archive named its root.
func Fetch(ctx context.Context, url, dest string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errFetch, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errFetch, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: %s answered %s", errFetch, url, resp.Status)
	}
	return unpack(resp.Body, dest)
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
