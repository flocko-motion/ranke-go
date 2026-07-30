// package: ranke / filter
// type:    logic
// job:     the Filter contract and the built-in edge filters (by field value, by type) that
// Claim.Edges applies, AND-combined
// limits:  selection only; edge and claim construction live elsewhere (-> edge, claim)
package ranke

// Filter selects a subset of edges matching some criterion. Passed
// to Claim.Edges as a variadic list — every filter must match (AND).
// Callers can implement their own; NewTypeFilter and NewEncodingFilter
// cover the common cases.
type Filter interface {
	MatchEdge(e Edge) bool
	MatchNode(n Node) bool
	IsEdgeFilter() bool
}

// EdgeFilterFieldValue matches an edge carrying a field named Field whose
// value equals Value exactly. An edge lacking the field never matches.
type EdgeFilterFieldValue struct {
	Field string
	Value string
}

// MatchEdge reports whether e carries the named field with the exact value.
func (f EdgeFilterFieldValue) MatchEdge(e Edge) bool {
	v, err := e.GetField(f.Field)
	return err == nil && v == f.Value
}

// MatchNode always passes — this is an edge-only filter.
func (f EdgeFilterFieldValue) MatchNode(Node) bool { return true }

// IsEdgeFilter reports that this filter selects edges (not nodes).
func (f EdgeFilterFieldValue) IsEdgeFilter() bool { return true }

// EdgeFilterType matches an edge whose type ("class/sub") equals Type
// exactly.
type EdgeFilterType struct {
	Type string
}

// MatchEdge reports whether e's type ("class/sub") equals the target exactly.
func (f EdgeFilterType) MatchEdge(e Edge) bool {
	return e.Type() == f.Type
}

// MatchNode always passes — this is an edge-only filter.
func (f EdgeFilterType) MatchNode(Node) bool { return true }

// IsEdgeFilter reports that this filter selects edges (not nodes).
func (f EdgeFilterType) IsEdgeFilter() bool { return true }

// EdgeFilterTypes matches an edge whose type ("class/sub") is any of Types —
// EdgeFilterType widened to a set (OR within this one filter).
type EdgeFilterTypes struct {
	Types []string
}

// MatchEdge reports whether e's type equals any of the target types.
func (f EdgeFilterTypes) MatchEdge(e Edge) bool {
	for _, t := range f.Types {
		if e.Type() == t {
			return true
		}
	}
	return false
}

// MatchNode always passes — this is an edge-only filter.
func (f EdgeFilterTypes) MatchNode(Node) bool { return true }

// IsEdgeFilter reports that this filter selects edges (not nodes).
func (f EdgeFilterTypes) IsEdgeFilter() bool { return true }
