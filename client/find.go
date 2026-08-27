// package: client / transport
// type:    logic
// job:     the find-or-create and bulk-lookup reads three tools wrote for themselves — one claim of
// a type whose field equals a value, and every claim of a type set keyed by a field
// limits:  builds queries and runs them through the client (-> read.go); minting what is missing is
// the caller's, since only it knows what the claim should say
package client

import (
	"context"

	"github.com/flocko-motion/ranke-go"
)

// Lookup names the claims a find reads: the branch to read, the types to admit, and
// the field a match is decided on.
type Lookup struct {
	// Branch is the scope. A reserved scope works here as it does for a read.
	Branch string
	// Types are the claim types admitted, as "class/sub" globs. One entry is the
	// ordinary case; several make the type test a set.
	Types []string
	// Field is the field a match is read from — the key of the bulk form, and the
	// left side of the equality of the singular one.
	Field string
	// Head pins the closure, so a lookup repeated across a contribution sees the
	// same graph both times.
	Head ranke.Id
}

// query renders the lookup, with an extra test applied when the caller has one.
func (l Lookup) query(extra *ranke.Where) ranke.Query {
	tests := []ranke.Where{typeTest(l.Types)}
	if extra != nil {
		tests = append(tests, *extra)
	}
	where := tests[0]
	if len(tests) > 1 {
		where = ranke.Where{And: tests}
	}
	return ranke.Query{
		Select: ranke.Select{Branch: l.Branch, Head: l.Head},
		Where:  &where,
	}
}

// typeTest admits the named types: one is an equality, several a set.
func typeTest(types []string) ranke.Where {
	if len(types) == 1 {
		return ranke.Where{Field: "type", Test: &ranke.Comparison{Eq: types[0]}}
	}
	in := make([]any, 0, len(types))
	for _, t := range types {
		in = append(in, t)
	}
	return ranke.Where{Field: "type", Test: &ranke.Comparison{In: in}}
}

// FindOne returns the one claim whose field equals value, and whether there was one.
// The caller mints only when found is false, so a re-run reuses the existing id.
//
// Two matches error: picking either would make the reused id depend on result order.
func (c *Client) FindOne(ctx context.Context, l Lookup, value any) (claim ranke.Claim, found bool, err error) {
	if l.Field == "" || len(l.Types) == 0 {
		return nil, false, ErrIncompleteLookup
	}
	q := l.query(&ranke.Where{Field: l.Field, Test: &ranke.Comparison{Eq: value}})
	q.Limit.Results = 2 // one more than wanted, so a second match is visible

	claims, err := c.QueryClaims(ctx, q)
	if err != nil {
		return nil, false, err
	}
	switch len(claims) {
	case 0:
		return nil, false, nil
	case 1:
		return claims[0], true, nil
	default:
		return nil, false, ranke.WithDetail(ErrAmbiguous, l.Field)
	}
}

// FindByField keys every claim of the lookup's types by its field — FindOne in bulk,
// one read for many values. A claim without the field keys nothing, and claims
// sharing a value all appear, so a collision shows rather than resolving itself.
func (c *Client) FindByField(ctx context.Context, l Lookup) (map[string][]ranke.Claim, error) {
	if l.Field == "" || len(l.Types) == 0 {
		return nil, ErrIncompleteLookup
	}
	claims, err := c.QueryClaims(ctx, l.query(nil))
	if err != nil {
		return nil, err
	}
	out := map[string][]ranke.Claim{}
	for _, cl := range claims {
		v, err := cl.Node().GetField(l.Field)
		if err != nil {
			continue
		}
		out[v] = append(out[v], cl)
	}
	return out, nil
}
