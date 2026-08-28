// package: tests/rql / integration
// type:    tool
// job:     the answer rules of §order, limit, execution — the returned sequence is sorted as the
// order keys fix, and stays inside the bounds limit sets
// limits:  pairwise on what was returned, never a sort: checking that a sequence is ordered is
// not producing the order, which is the engine's (-> query_default.go)
package rql

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rankegraph/ranke-go"
)

// ruleResultBound: `R-QLIMIT` — results within the cap, and truncation claimed only
// where a bound could have done the cutting. Limit is applied last (R-QEVAL's order,
// which this does not enforce), so a short answer under an uncut cap was not cut.
func ruleResultBound(_ context.Context, t *answerUnderVerification) []Violation {
	var out []Violation
	limit, budget, n := t.q.Limit.Results, t.q.Limit.Time, len(t.results)

	if limit > 0 && n > limit {
		out = append(out, Violation{Index: -1, Err: fmt.Errorf(
			"%d results returned under limit.results %d", n, limit)})
	}
	if t.report == nil {
		return out
	}
	if t.report.Results != n {
		out = append(out, Violation{Index: -1, Err: fmt.Errorf(
			"report counts %d results, the stream carried %d", t.report.Results, n)})
	}
	if t.report.Truncated && budget == 0 {
		switch {
		case limit == 0:
			out = append(out, Violation{Index: -1, Err: fmt.Errorf(
				"report claims truncation with both bounds unset — nothing could have cut the read")})
		case n < limit:
			out = append(out, Violation{Index: -1, Err: fmt.Errorf(
				"report claims truncation at %d results under limit.results %d — the cap did not cut", n, limit)})
		}
	}
	return out
}

// ruleAnswerOrder: `R-QSORT` — each adjacent pair is in order under the stated keys,
// then the natural (created_at, id) tie-break. Pairwise on the returned sequence, so
// it checks sortedness rather than producing a sort.
func ruleAnswerOrder(_ context.Context, t *answerUnderVerification) []Violation {
	var out []Violation
	for i := 1; i < len(t.results); i++ {
		prev, cur := claimOf(t.results[i-1]), claimOf(t.results[i])
		if prev == nil || cur == nil {
			continue // no claim in hand: a detail:id answer carries no fields to order by
		}
		if order := comparePair(prev, cur, t.q.Order); order > 0 {
			out = append(out, Violation{Index: i, Err: fmt.Errorf(
				"element %d sorts before element %d under %s", i, i-1, describeOrder(t.q.Order))})
		}
	}
	return out
}

// claimOf is the claim an element carries natively, nil when it carries none.
func claimOf(r ranke.QueryResult) ranke.Claim {
	if r.ClaimNative != nil {
		return r.ClaimNative
	}
	if n := len(r.PathNative); n > 0 {
		return r.PathNative[n-1] // a route's endpoint is what the sort ranks
	}
	return nil
}

// comparePair returns >0 when a must follow b — the one verdict a sortedness check
// needs. 0 covers both "in order" and "cannot tell", which keeps the rule sound: an
// incomparable pair is passed over rather than reported.
func comparePair(a, b ranke.Claim, keys []ranke.OrderKey) int {
	for _, k := range keys {
		av, aok := orderValue(a, k.Field)
		bv, bok := orderValue(b, k.Field)
		if aok != bok {
			if !aok { // a lacks the key, b has it: missing sorts last
				return 1
			}
			return -1
		}
		if !aok {
			continue // neither has it — this key decides nothing
		}
		c, ok := compareValues(av, bv, k.Compare)
		if !ok {
			return 0 // not comparable: say nothing rather than something wrong
		}
		if c == 0 {
			continue
		}
		if k.Dir == ranke.SortDesc {
			c = -c
		}
		return c
	}
	// Natural order, the tie-break that makes the sort total.
	at, bt := a.Node().CreatedAt(), b.Node().CreatedAt()
	if !at.Equal(bt) {
		if at.After(bt) {
			return 1
		}
		return -1
	}
	return strings.Compare(a.ID().String(), b.ID().String())
}

// orderValue reads the field a key names, matching what a read projects
// (query_default.queryFieldValue): the well-known fields, then the node's own.
func orderValue(c ranke.Claim, field string) (any, bool) {
	n := c.Node()
	switch field {
	case "id":
		return c.ID().String(), true
	case "type":
		return n.Type(), true
	case "created_at":
		return n.CreatedAt(), true
	case "height":
		return n.Height(), true
	case "encoding":
		return n.Encoding(), n.ContentKind() != ranke.ContentNone
	case "content_size":
		return n.GetContentSize(), n.ContentKind() != ranke.ContentNone
	}
	v, err := n.GetField(field)
	return v, err == nil
}

// compareValues orders two field values under the collation, reporting ok=false
// where it cannot — a pair the rule then passes over.
func compareValues(a, b any, col ranke.Collation) (int, bool) {
	if at, ok := a.(time.Time); ok {
		bt, ok := b.(time.Time)
		if !ok {
			return 0, false
		}
		switch {
		case at.Before(bt):
			return -1, true
		case at.After(bt):
			return 1, true
		}
		return 0, true
	}
	if col != ranke.CompareLexical {
		af, aok := asFloat(a)
		bf, bok := asFloat(b)
		if aok && bok {
			switch {
			case af < bf:
				return -1, true
			case af > bf:
				return 1, true
			}
			return 0, true
		}
		if col == ranke.CompareNumeric {
			return 0, false // asked for numeric and the values are not
		}
	}
	as, aok := a.(string)
	bs, bok := b.(string)
	if !aok || !bok {
		return 0, false
	}
	return strings.Compare(as, bs), true
}

// asFloat coerces a numeric field value, including the decimal text a field holds.
func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case uint64:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	case string:
		f, err := strconv.ParseFloat(n, 64)
		return f, err == nil
	}
	return 0, false
}

// describeOrder names the keys a violation was judged under, so the message says
// which sort was broken.
func describeOrder(keys []ranke.OrderKey) string {
	if len(keys) == 0 {
		return "the natural (created_at, id) order"
	}
	parts := make([]string, 0, len(keys)+1)
	for _, k := range keys {
		dir := "asc"
		if k.Dir == ranke.SortDesc {
			dir = "desc"
		}
		parts = append(parts, k.Field+" "+dir)
	}
	return strings.Join(parts, ", ") + ", then (created_at, id)"
}
