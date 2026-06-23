package rest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/adapter/storage/adaptertest"
)

// TestConformance runs the shared Universe suite against the rest adapter,
// each universe talking to a fresh in-process blob server — no external
// service. The server is the reference implementation of the tiny GET/
// PUT/HEAD contract the adapter targets.
func TestConformance(t *testing.T) {
	adaptertest.Run(t, func(t *testing.T) ranke.Universe {
		ts := httptest.NewServer(newBlobServer())
		t.Cleanup(ts.Close)
		u, err := New(ts.URL, ts.Client())
		if err != nil {
			t.Fatalf("rest.New: %v", err)
		}
		return u
	})
}

// newBlobServer is a map-backed HTTP blob store implementing the contract
// the rest adapter expects: GET/PUT/HEAD on /{key}.
func newBlobServer() http.Handler {
	var (
		mu sync.Mutex
		m  = map[string][]byte{}
	)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/")
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			b, ok := m[key]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(b)
		case http.MethodHead:
			if _, ok := m[key]; !ok {
				w.WriteHeader(http.StatusNotFound)
			}
		case http.MethodPut:
			b, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			if _, ok := m[key]; !ok {
				m[key] = b
			}
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}
