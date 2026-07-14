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

func (f EdgeFilterFieldValue) MatchEdge(e Edge) bool {
	v, err := e.GetField(f.Field)
	return err == nil && v == f.Value
}

func (f EdgeFilterFieldValue) MatchNode(Node) bool { return true }
func (f EdgeFilterFieldValue) IsEdgeFilter() bool  { return true }

// EdgeFilterType matches an edge whose type ("class/sub") equals Type
// exactly.
type EdgeFilterType struct {
	Type string
}

func (f EdgeFilterType) MatchEdge(e Edge) bool {
	return e.Type() == f.Type
}

func (f EdgeFilterType) MatchNode(Node) bool { return true }
func (f EdgeFilterType) IsEdgeFilter() bool  { return true }
