package ranke

// Filter selects a subset of edges matching some criterion. Passed
// to Claim.Edges as a variadic list — every filter must match (AND).
// Callers can implement their own; NewTypeFilter and NewEncodingFilter
// cover the common cases.
type EdgeFilter interface {
	Match(e Edge) bool
}

// EdgeFilterFieldValue matches an edge carrying a field named Field whose
// value equals Value exactly. An edge lacking the field never matches.
type EdgeFilterFieldValue struct {
	Field string
	Value string
}

func (f EdgeFilterFieldValue) Match(e Edge) bool {
	v, err := e.GetField(f.Field)
	return err == nil && v == f.Value
}

// EdgeFilterType matches an edge whose type ("class/sub") equals Type
// exactly.
type EdgeFilterType struct {
	Type string
}

func (f EdgeFilterType) Match(e Edge) bool {
	return e.Type() == f.Type
}
