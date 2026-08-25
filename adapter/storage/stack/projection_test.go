// package: stack / projection_test
// type:    test
// job:     the test doubles that model a graph-native layer — a structure-only cache and the
// engines that reconstruct claims or answer with ids alone, as neo4j does
// limits:  fakes only, no assertions of their own; the stack behaviour they exercise is
// stack_test.go's
package stack_test

import (
	"context"
	"io"

	"github.com/flocko-motion/ranke-go"
)

// --- structure-only cache: models a neo4j-style cache in a stack ---
//
// It holds claim STRUCTURE but reconstructs claims WITHOUT their inline content
// bytes (like neo4j, which cannot inline binary content), and keeps no verbatim
// CBOR (RawClaims=false) and no content blobs (ExternalContent=false). So a
// content read cannot be served from this layer: it must fall through to the
// byte layer below, addressed BY CLAIM (GetClaimContent) — a bare hash lookup
// would miss, since inline content is not a standalone blob.
type structOnlyCache struct{ ranke.Universe }

func (s structOnlyCache) GetClaims(ctx context.Context, ids []ranke.Id, opts ...ranke.GetOption) ([]ranke.Claim, error) {
	cs, err := s.Universe.GetClaims(ctx, ids, opts...)
	if err != nil {
		return nil, err
	}
	for i, c := range cs {
		stripped, err := stripContent(c)
		if err != nil {
			return nil, err
		}
		cs[i] = stripped
	}
	return cs, nil
}

func (s structOnlyCache) GetClaimsRaw(context.Context, []ranke.Id) ([][]byte, error) {
	return nil, ranke.ErrNotFound // structure-only: keeps no verbatim CBOR
}

func (s structOnlyCache) GetContents(context.Context, []ranke.ContentRef) ([][]byte, error) {
	return nil, ranke.ErrNotFound // holds no content blobs
}

func (s structOnlyCache) HasContents(_ context.Context, hashes []ranke.Id) ([]bool, error) {
	return make([]bool, len(hashes)), nil
}

func (s structOnlyCache) StreamContent(context.Context, ranke.Id, uint64) (io.ReadCloser, error) {
	return nil, ranke.ErrNotFound
}

func (s structOnlyCache) Capabilities() ranke.Capabilities {
	c := s.Universe.Capabilities()
	c.RawClaims = false
	c.ExternalContent = false
	c.ContentCap = 0
	c.Tier = ranke.StorageTierEager // a lossy projection can't be authoritative (like neo4j)
	return c
}

// projectionEngine is a structure-only cache in the engine seat: its Query
// reconstructs each claim from parts, so a result carries no stored record — the
// shape a graph-native engine (neo4j) returns.
type projectionEngine struct{ structOnlyCache }

func (p projectionEngine) Query(ctx context.Context, q ranke.Query, scope ranke.Scope) (ranke.ResultStream, error) {
	return reshape(ctx, p.Universe, q, scope, func(r ranke.QueryResult) (ranke.QueryResult, error) {
		if r.ClaimNative == nil {
			return r, nil
		}
		c, err := stripContent(r.ClaimNative)
		if err != nil {
			return r, err
		}
		r.ClaimNative = c
		return r, nil
	})
}

// idOnlyEngine answers with identities alone, holding no canonical bytes to
// serialise — what neo4j does for a cbor read: it selects, a byte layer encodes.
type idOnlyEngine struct{ structOnlyCache }

func (e idOnlyEngine) Query(ctx context.Context, q ranke.Query, scope ranke.Scope) (ranke.ResultStream, error) {
	return reshape(ctx, e.Universe, q, scope, func(r ranke.QueryResult) (ranke.QueryResult, error) {
		return ranke.QueryResult{Kind: ranke.KindClaimId, ClaimId: r.ClaimId, PathId: r.PathId}, nil
	})
}

// reshape runs the query on u and rewrites every result through f.
func reshape(ctx context.Context, u ranke.Universe, q ranke.Query, scope ranke.Scope,
	f func(ranke.QueryResult) (ranke.QueryResult, error)) (ranke.ResultStream, error) {
	rs, err := u.Query(ctx, q, scope)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []ranke.QueryResult
	for rs.Next() {
		r, err := f(rs.Result())
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	return &sliceStream{results: out}, nil
}

// sliceStream serves an already-assembled result slice.
type sliceStream struct {
	results []ranke.QueryResult
	i       int
}

func (s *sliceStream) Next() bool {
	s.i++
	return s.i <= len(s.results)
}
func (s *sliceStream) Result() ranke.QueryResult  { return s.results[s.i-1] }
func (s *sliceStream) Report() *ranke.QueryReport { return ranke.ReportOf(s.results) }
func (s *sliceStream) Err() error                 { return nil }
func (s *sliceStream) Close() error               { return nil }

type fielder interface {
	Fields() []string
	GetField(string) (string, error)
}

func fieldsOf(f fielder) map[string]string {
	names := f.Fields()
	if len(names) == 0 {
		return nil
	}
	m := make(map[string]string, len(names))
	for _, k := range names {
		v, _ := f.GetField(k)
		m[k] = v
	}
	return m
}

// stripContent rebuilds a claim from its parts WITHOUT the inline content bytes
// (InlineContent omitted) — exactly how a structure-only cache reconstructs a
// claim it holds but whose binary content it never inlined.
func stripContent(c ranke.Claim) (ranke.Claim, error) {
	n := c.Node()
	parts := ranke.ClaimParts{
		ID:          n.ID(),
		Type:        n.Type(),
		Encoding:    n.Encoding(),
		CreatedAt:   n.CreatedAt(),
		Height:      n.Height(),
		ContentHash: n.GetContentHash(),
		ContentSize: n.GetContentSize(),
		Fields:      fieldsOf(n),
	}
	for _, e := range c.Edges() {
		parts.Edges = append(parts.Edges, ranke.EdgeParts{
			ID:                e.ID(),
			Reference:         e.Reference(),
			Type:              e.Type(),
			Encoding:          e.Encoding(),
			RelationDirection: e.RelationDirection(),
			ContentHash:       e.GetContentHash(),
			ContentSize:       e.GetContentSize(),
			Fields:            fieldsOf(e),
		})
	}
	return ranke.AssembleClaim(parts)
}
