// package: rest / persistence
// type:    adapter
// job:     stores claims and content blobs over a tiny HTTP blob API (GET/PUT/HEAD)
// limits:  no auth, retries, or pagination; just the BlobStore primitives over HTTP (-> adapter)
//
// Package rest is an HTTP persistence adapter for a ranke Universe. It
// proves the BlobStore seam generalizes past local storage: the three
// primitives map straight onto a trivial REST contract —
//
//	GET    {baseURL}/{id}   -> 200 + bytes, or 404
//	PUT    {baseURL}/{id}   <- bytes,        200/201
//	HEAD   {baseURL}/{id}   -> 200, or 404
//
// storage.NewBlobUniverse supplies the claim codec, content integrity, and
// copy machinery on top. Any object store or blob service exposing this
// shape (or trivially wrapped to) is a ranke Universe.
package rest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/adapter/storage"
)

// New returns a Universe backed by the blob API rooted at baseURL, using
// the given client (nil = http.DefaultClient).
//
//deadcode:keep
func New(baseURL string, client *http.Client) (ranke.Universe, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("adapter/rest.New: empty baseURL")
	}
	if client == nil {
		client = http.DefaultClient
	}
	return storage.NewBlobUniverse(&store{
		base:   strings.TrimRight(baseURL, "/"),
		client: client,
	}), nil
}

type store struct {
	base   string
	client *http.Client
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
		return nil, fmt.Errorf("GET %s: %s", key, resp.Status)
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
		return fmt.Errorf("PUT %s: %s", key, resp.Status)
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
		return false, fmt.Errorf("HEAD %s: %s", key, resp.Status)
	}
}

//deadcode:keep
func (s *store) Close() error { return nil }
