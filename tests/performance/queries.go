// package: tests/performance / integration
// type:    test
// job:     the fixed RQL query set the matrix runs on every backend — timed a few times each for per-query latency, and hashed (order-sensitive) so every backend's result set can be checked against the mem reference
// limits:  queries only; wiring into the run + the mem-reference comparison live in the harness (-> harness.go). Hashes identity (ids) in stream order, not content bytes.
package performance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/adapter/storage/mem"
	"github.com/flocko-motion/ranke-go/generator"
)

// namedQuery pairs a stable name with an RQL query. The name keys both the
// per-query latency report and the cross-backend determinism check.
type namedQuery struct {
	name string
	q    ranke.Query
}

// perfQueries is the fixed query set, rooted at the generated archive. Each is
// a representative RQL shape; because the generator's clock is deterministic,
// the default (created_at, id) order is stable, so the ordered result set — and
// thus its hash — is identical across backends that answer the same meaning.
func perfQueries(m *generator.Manifest) []namedQuery {
	head := m.Head
	qs := []namedQuery{
		{"closure/head", ranke.Query{Select: ranke.Select{Claim: head}}},
		{"where/sources", ranke.Query{
			Select: ranke.Select{Claim: head},
			Where:  &ranke.Where{Field: "type", Test: &ranke.Comparison{Glob: "source/*"}},
		}},
		{"where/height-ge-2", ranke.Query{
			Select: ranke.Select{Claim: head},
			Where:  &ranke.Where{Field: "height", Test: &ranke.Comparison{Ge: 2}},
		}},
		{"path/derivation-d3", ranke.Query{
			Select: ranke.Select{Claim: head, Path: []ranke.PathStep{{Edges: []string{"derivation/*"}, Depth: 3}}},
		}},
		{"order/height-desc-20", ranke.Query{
			Select: ranke.Select{Claim: head},
			Order:  &ranke.Order{Field: "height", Desc: true},
			Limit:  ranke.Limit{Results: 20},
		}},
	}
	if m.HighDegree != nil {
		qs = append(qs, namedQuery{"closure/high-degree", ranke.Query{Select: ranke.Select{Claim: m.HighDegree}}})
	}
	return qs
}

// runQuery executes q against u, draining the stream, and returns the
// order-sensitive hash of the reached ids, the result count, and how long the
// query-plus-drain took. The hash commits to identity in emitted order, so a
// backend whose engine reorders or drops results diverges from the reference.
func runQuery(ctx context.Context, u ranke.Universe, q ranke.Query) (hash string, results int, dur time.Duration, err error) {
	start := time.Now()
	rs, err := u.Query(ctx, q, nil)
	if err != nil {
		return "", 0, 0, err
	}
	h := sha256.New()
	for rs.Next() {
		h.Write([]byte(rs.Result().Claim.ID().String()))
		h.Write([]byte{'\n'})
		results++
	}
	dur = time.Since(start)
	if e := rs.Err(); e != nil {
		_ = rs.Close()
		return "", 0, 0, e
	}
	if e := rs.Close(); e != nil {
		return "", 0, 0, e
	}
	return hex.EncodeToString(h.Sum(nil)), results, dur, nil
}

// queryStat is one named query's outcome on a backend: latency over the reps
// and the result-set hash (compared to the mem reference for determinism).
type queryStat struct {
	name          string
	min, avg, max time.Duration
	results       int
	hash          string
}

// runQuerySet runs every query in the set reps times against u, returning the
// per-query latency stats (with the result-set hash) in query order. reps ≤ 0
// defaults to 10.
func runQuerySet(ctx context.Context, u ranke.Universe, m *generator.Manifest, reps int) ([]queryStat, error) {
	if reps <= 0 {
		reps = 10
	}
	var stats []queryStat
	for _, nq := range perfQueries(m) {
		var mn, mx, sum time.Duration
		var hash string
		var results int
		for i := 0; i < reps; i++ {
			h, n, d, err := runQuery(ctx, u, nq.q)
			if err != nil {
				return nil, err
			}
			hash, results = h, n
			if i == 0 || d < mn {
				mn = d
			}
			if d > mx {
				mx = d
			}
			sum += d
		}
		stats = append(stats, queryStat{
			name: nq.name, min: mn, avg: sum / time.Duration(reps), max: mx,
			results: results, hash: hash,
		})
	}
	return stats, nil
}

// referenceQueryHashes generates the archive into a fresh mem Universe — the
// reference implementation (the standard every other backend must match) — and
// returns each query's result-set hash keyed by name.
func referenceQueryHashes(ctx context.Context, spec generator.Spec) (map[string]string, error) {
	u := mem.New()
	m, err := generator.Generate(ctx, u, spec)
	if err != nil {
		return nil, err
	}
	stats, err := runQuerySet(ctx, u, m, 1)
	if err != nil {
		return nil, err
	}
	hashes := make(map[string]string, len(stats))
	for _, s := range stats {
		hashes[s.name] = s.hash
	}
	return hashes, nil
}
