// package: rest / persistence
// type:    adapter
// job:     stores claims and content blobs over a tiny HTTP blob API (GET/PUT/HEAD)
// limits:  no auth, retries, or pagination; just the BlobStore primitives over HTTP (-> adapter)
//
// Package rest shows the BlobStore seam reaching past local storage, its three
// primitives mapping onto a trivial contract:
//
//	GET    {baseURL}/{id}   -> 200 + bytes, or 404
//	PUT    {baseURL}/{id}   <- bytes,        200/201
//	HEAD   {baseURL}/{id}   -> 200, or 404
//
// Any blob service of this shape is a ranke Universe.
package rest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/adapter/storage"
)

var (
	errEmptyBaseURL = errors.New("adapter/rest.New: empty baseURL")
	errNoTier       = errors.New("adapter/rest.New: caps declare no valid tier")
	errStatus       = errors.New("adapter/rest: unexpected status")
)

// New returns a Universe over the blob API at baseURL (nil client =
// http.DefaultClient). A GET/PUT/HEAD proxy cannot discover the remote's
// durability or delete support, so the operator declares it in caps.
//
//deadcode:keep
func New(baseURL string, client *http.Client, caps ranke.Capabilities) (ranke.Universe, error) {
	if baseURL == "" {
		return nil, errEmptyBaseURL
	}
	if client == nil {
		client = http.DefaultClient
	}
	// The remote can't be probed, so the caller must declare a valid tier — a
	// blob store keeps claims verbatim, so any tier is allowed but not none.
	eff := caps
	eff.RawClaims, eff.ExternalContent = true, true
	if !eff.AllowsTier(caps.Tier) {
		return nil, fmt.Errorf("%w (%q)", errNoTier, caps.Tier)
	}
	return storage.NewBlobUniverse(&store{
		base:   strings.TrimRight(baseURL, "/"),
		client: client,
		caps:   caps,
	}, storage.WithTier(caps.Tier)), nil
}

type store struct {
	base   string
	client *http.Client
	caps   ranke.Capabilities
}

func (s *store) url(key string) string { return s.base + "/" + url.PathEscape(key) }

//deadcode:keep
func (s *store) Get(ctx context.Context, key string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url(key), nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return io.ReadAll(resp.Body)
	case http.StatusNotFound:
		return nil, ranke.ErrNotFound
	default:
		return nil, fmt.Errorf("%w: GET %s: %s", errStatus, key, resp.Status)
	}
}

//deadcode:keep
func (s *store) Put(ctx context.Context, key string, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, s.url(key), bytes.NewReader(data))
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("%w: PUT %s: %s", errStatus, key, resp.Status)
	}
	return nil
}

//deadcode:keep
func (s *store) Has(ctx context.Context, key string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, s.url(key), nil)
	if err != nil {
		return false, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("%w: HEAD %s: %s", errStatus, key, resp.Status)
	}
}

//deadcode:keep
func (s *store) Close() error { return nil }

// Capabilities returns what was declared at construction, GET/PUT/HEAD being blind
// to the rest.
//
//deadcode:keep
func (s *store) Capabilities() ranke.Capabilities {
	return s.caps
}
