// package: stack / persistence
// type:    adapter
// job:     the stack's read path — run the query on the top layer, re-read the claims from a layer
// holding the stored record when the top layer keeps none, then encode
// limits:  routing only, no storage and no query execution of its own (-> the layer Universes); the
// serialising itself is the library's (-> ranke.EncodeResults)
package stack

import (
	"context"

	"github.com/rankegraph/ranke-go"
)

var errNoRawClaims = ranke.WithDetail(ranke.ErrUnsupported,
	"adapter/stack: canonical claims — no layer keeps them")

// Query runs the read on the top layer, then re-reads the claims from a layer that
// keeps the stored record when the top layer keeps none, and serialises the result.
// The top layer is asked in native form, since a claim is only complete enough to
// encode once that re-read has happened.
func (s *stack) Query(ctx context.Context, q ranke.Query, scope ranke.Scope) (ranke.ResultStream, error) {
	top := s.layers[0]
	// The original form and verbatim cbor are both the stored record, which a layer
	// reconstructing its claims never holds; ids need no claim at all.
	needRaw := q.Output.Detail != ranke.DetailID && !top.caps.RawClaims &&
		(q.Output.Form == ranke.FormOriginal || q.Output.Encoding == ranke.ResultCBOR)
	wantEncoding := q.Output.Encoding != "" && q.Output.Encoding != ranke.ResultNative
	if !needRaw && !wantEncoding {
		return top.u.Query(ctx, q, scope)
	}
	if needRaw && !s.holdsRawClaims() {
		return nil, errNoRawClaims
	}
	down := q
	down.Output.Encoding = ranke.ResultNative
	rs, err := top.u.Query(ctx, down, scope)
	if err != nil {
		return nil, err
	}
	results, err := drain(rs)
	if err != nil {
		return nil, err
	}
	if needRaw {
		if ranke.ReportEnabled(ctx, ranke.ReportDebug) {
			ranke.ReportEvent(ctx, "stack", "hydrate", ranke.ReportDebug, "claims from a RawClaims layer",
				map[string]any{"results": len(results)})
		}
		if err := s.hydrateClaims(ctx, results, q.Output.Form); err != nil {
			return nil, err
		}
	}
	// The top layer's Kind stands for what it produced; EncodeResults restamps the
	// results it serialises, and leaves a report element on its own tag.
	if err := ranke.EncodeResults(results, q.Output); err != nil {
		return nil, err
	}
	return &hydratedStream{results: results}, nil
}

// holdsRawClaims reports whether any layer keeps the canonical claim bytes.
func (s *stack) holdsRawClaims() bool {
	for li := range s.layers {
		if s.layers[li].caps.RawClaims {
			return true
		}
	}
	return false
}

// drain reads a stream fully, returning every element it emitted — the report
// among them where the query asked for one (`R-QSTREAM`), so nothing is dropped by
// reading the sequence rather than the accessor beside it.
func drain(rs ranke.ResultStream) ([]ranke.QueryResult, error) {
	var out []ranke.QueryResult
	for rs.Next() {
		out = append(out, rs.Result())
	}
	if err := rs.Err(); err != nil {
		_ = rs.Close()
		return nil, err
	}
	return out, rs.Close()
}

// hydrateClaims refills every claim in results — endpoints and each path's route —
// from a RawClaims layer in one batch, keyed by the ids the read returned, since a
// layer answering with ids alone leaves the native fields empty. form picks what the
// refilled claims are: the stored delta, or that delta with its overlay resolved.
func (s *stack) hydrateClaims(ctx context.Context, results []ranke.QueryResult, form ranke.Form) error {
	var ids []ranke.Id
	seen := map[string]bool{}
	add := func(id ranke.Id) {
		if id == nil || seen[id.String()] {
			return
		}
		seen[id.String()] = true
		ids = append(ids, id)
	}
	for _, r := range results {
		add(r.ClaimId)
		for _, id := range r.PathId {
			add(id)
		}
		for _, c := range r.PathNative {
			if c != nil {
				add(c.ID())
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	// Delta form is what routes the read past the top layer to a RawClaims one, the
	// only place the stored record lives; materialising is then a stack-level step,
	// as it is in GetClaims.
	got, err := s.GetClaims(ctx, ids, ranke.WithNotDiffMaterialized())
	if err != nil {
		return err
	}
	if form != ranke.FormOriginal {
		if _, err := ranke.DefaultMaterialize(ctx, s, got); err != nil {
			return err
		}
	}
	byID := make(map[string]ranke.Claim, len(got))
	for i, id := range ids {
		byID[id.String()] = got[i]
	}
	for i := range results {
		if results[i].ClaimId != nil {
			results[i].ClaimNative = byID[results[i].ClaimId.String()]
		}
		if route := results[i].PathId; len(route) > 0 {
			hydrated := make([]ranke.Claim, 0, len(route))
			for _, id := range route {
				hydrated = append(hydrated, byID[id.String()])
			}
			results[i].PathNative = hydrated
			continue
		}
		for j, c := range results[i].PathNative {
			if c != nil {
				results[i].PathNative[j] = byID[c.ID().String()]
			}
		}
	}
	return nil
}

// hydratedStream serves an already-assembled element slice, the inner stream's
// report carried along in it.
type hydratedStream struct {
	results []ranke.QueryResult
	i       int
}

func (s *hydratedStream) Next() bool {
	if s.i >= len(s.results) {
		return false
	}
	s.i++
	return true
}
func (s *hydratedStream) Result() ranke.QueryResult  { return s.results[s.i-1] }
func (s *hydratedStream) Report() *ranke.QueryReport { return ranke.ReportOf(s.results) }
func (s *hydratedStream) Err() error                 { return nil }
func (s *hydratedStream) Close() error               { return nil }
