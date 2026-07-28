// package: stack / persistence
// type:    adapter
// job:     the stack's read path — run the query on the engine layer, and when the form asked for is one that layer cannot hold, re-read the claims from a layer that can
// limits:  routing only, no storage and no query execution of its own (-> the layer Universes); claim/content routing and repair live alongside (-> stack.go)
package stack

import (
	"context"
	"errors"

	"github.com/flocko-motion/ranke-go"
)

// Joined so errors.Is still matches ranke.ErrUnsupported across the port.
var errNoOriginals = errors.Join(ranke.ErrUnsupported,
	errors.New("adapter/stack: original form — no layer keeps canonical claims"))

// Query runs the read on the top layer — which layer answers is a routing
// decision, and the selection is the expensive part. The form asked for is a
// separate question: original form is the stored, id-defining claim, which only a
// RawClaims layer keeps. When the engine has it, the engine answers alone; when it
// does not, the engine still decides WHICH claims (the costly half) and the claims
// themselves are re-read from a layer that holds the originals.
func (s *stack) Query(ctx context.Context, q ranke.Query, scope ranke.Scope) (ranke.ResultStream, error) {
	engine := s.layers[0]
	if q.Output.Form != ranke.FormOriginal || engine.caps.RawClaims {
		return engine.u.Query(ctx, q, scope)
	}
	if !s.holdsRawClaims() {
		return nil, errNoOriginals
	}
	rs, err := engine.u.Query(ctx, q, scope)
	if err != nil {
		return nil, err
	}
	results, report, err := drain(rs)
	if err != nil {
		return nil, err
	}
	if ranke.ReportEnabled(ctx, ranke.ReportDebug) {
		ranke.ReportEvent(ctx, "stack", "hydrate", ranke.ReportDebug, "original form from a RawClaims layer",
			map[string]any{"results": len(results)})
	}
	if err := s.hydrateOriginals(ctx, results); err != nil {
		return nil, err
	}
	return &hydratedStream{results: results, report: report}, nil
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

// drain reads a stream fully, returning its results and report.
func drain(rs ranke.ResultStream) ([]ranke.QueryResult, *ranke.QueryReport, error) {
	var out []ranke.QueryResult
	for rs.Next() {
		out = append(out, rs.Result())
	}
	if err := rs.Err(); err != nil {
		_ = rs.Close()
		return nil, nil, err
	}
	report := rs.Report()
	return out, report, rs.Close()
}

// hydrateOriginals replaces every claim in results — endpoints and each path's
// route — with its stored delta form, read in one batch. GetClaims routes the
// delta request to a RawClaims layer, so this is where the originals come from.
func (s *stack) hydrateOriginals(ctx context.Context, results []ranke.QueryResult) error {
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
		add(r.Id)
		for _, c := range r.Path {
			if c != nil {
				add(c.ID())
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	got, err := s.GetClaims(ctx, ids, ranke.WithNotDiffMaterialized())
	if err != nil {
		return err
	}
	byID := make(map[string]ranke.Claim, len(got))
	for i, id := range ids {
		byID[id.String()] = got[i]
	}
	for i := range results {
		if results[i].Claim != nil {
			results[i].Claim = byID[results[i].Id.String()]
		}
		for j, c := range results[i].Path {
			if c != nil {
				results[i].Path[j] = byID[c.ID().String()]
			}
		}
	}
	return nil
}

// hydratedStream serves an already-assembled result slice.
type hydratedStream struct {
	results []ranke.QueryResult
	report  *ranke.QueryReport
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
func (s *hydratedStream) Report() *ranke.QueryReport { return s.report }
func (s *hydratedStream) Err() error                 { return nil }
func (s *hydratedStream) Close() error               { return nil }
