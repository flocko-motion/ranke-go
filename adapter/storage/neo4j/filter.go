// package: neo4j / filter
// type:    adapter
// job:     lower `where` and `order` to a Cypher predicate and sort terms, matching what
// evalComparison and sortResults decide natively (`R-QCYPHER`)
// limits:  renders conditions and binds their operands; which nodes they apply to is
// query.go's (-> query, convert for the stored property forms)
package neo4j

import (
	"strconv"
	"strings"
	"time"

	"github.com/rankegraph/ranke-go"
)

// intField are the fields neo4j stores as native integers, so comparisons and
// ORDER BY use them without coercion. Other fields are strings; type is a label.
var intField = map[string]bool{"height": true, "content_size": true}

// timeField names the properties stored as fixed-width iso8601Nano, whose
// lexicographic order is their instant order.
var timeField = map[string]bool{"created_at": true}

// atStoredPrecision spells a time operand the way the property holds it. Cypher
// compares these as strings, and RFC3339 omits trailing zeros: the stored
// "…:02.000000000Z" sorts BELOW a bound written "…:02Z", "." (0x2E) preceding "Z"
// (0x5A). So Eq matched nothing, Ge skipped the instant it named and Lt included it,
// where evalComparison compares instants.
func atStoredPrecision(field string, v any) any {
	if !timeField[field] {
		return v
	}
	switch t := v.(type) {
	case time.Time:
		return t.UTC().Format(iso8601Nano)
	case string:
		// The layouts asTime accepts, so both engines agree on what is a time.
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
			if p, err := time.Parse(layout, t); err == nil {
				return p.UTC().Format(iso8601Nano)
			}
		}
	}
	return v
}

// whereClause lowers a Where tree to a Cypher boolean over node. A nil tree is
// "true". p names the bound-parameter sink and ctr the next free index.
func whereClause(w *ranke.Where, node string, p map[string]any, ctr *int) string {
	if w == nil {
		return "true"
	}
	switch {
	case len(w.And) > 0:
		return "(" + joinClauses(w.And, " AND ", node, p, ctr) + ")"
	case len(w.Or) > 0:
		return "(" + joinClauses(w.Or, " OR ", node, p, ctr) + ")"
	case w.Not != nil:
		return "(NOT " + whereClause(w.Not, node, p, ctr) + ")"
	case w.Test != nil:
		return cmpClause(w.Field, *w.Test, node, p, ctr)
	default:
		return "true"
	}
}

func joinClauses(ws []ranke.Where, sep, node string, p map[string]any, ctr *int) string {
	parts := make([]string, len(ws))
	for i := range ws {
		parts[i] = whereClause(&ws[i], node, p, ctr)
	}
	return strings.Join(parts, sep)
}

// bind stores v under a fresh $wN parameter and returns its reference.
func bind(v any, p map[string]any, ctr *int) string {
	k := "w" + strconv.Itoa(*ctr)
	*ctr++
	p[k] = v
	return "$" + k
}

// cmpClause lowers one field comparison to Cypher as evalComparison does: type is
// a label, int fields compare natively, others coerce to float for numeric operands
// and to string otherwise. A missing field yields null, so WHERE rejects the claim.
func cmpClause(field string, cmp ranke.Comparison, node string, p map[string]any, ctr *int) string {
	if field == "type" {
		return typeClause(cmp, node, p, ctr)
	}
	acc := node + "." + field // property accessor
	num := intField[field]
	at := func(v any) any { return atStoredPrecision(field, v) }
	ord := func(op string, v any) string {
		if num || isNumeric(v) {
			return "toFloat(" + acc + ") " + op + " " + bind(toFloatVal(v), p, ctr)
		}
		return acc + " " + op + " " + bind(at(v), p, ctr)
	}
	switch {
	case cmp.Eq != nil:
		return acc + " = " + bind(at(cmp.Eq), p, ctr)
	case cmp.Ne != nil:
		return acc + " <> " + bind(at(cmp.Ne), p, ctr)
	case cmp.Lt != nil:
		return ord("<", cmp.Lt)
	case cmp.Le != nil:
		return ord("<=", cmp.Le)
	case cmp.Gt != nil:
		return ord(">", cmp.Gt)
	case cmp.Ge != nil:
		return ord(">=", cmp.Ge)
	case cmp.In != nil:
		in := make([]any, len(cmp.In))
		for i, v := range cmp.In {
			in[i] = at(v)
		}
		return acc + " IN " + bind(in, p, ctr)
	case cmp.Glob != "":
		return acc + " =~ " + bind(globToRegex(cmp.Glob), p, ctr)
	default:
		return "true"
	}
}

// typeClause lowers a comparison on type, which neo4j stores as a node label.
func typeClause(cmp ranke.Comparison, node string, p map[string]any, ctr *int) string {
	labels := "labels(" + node + ")"
	switch {
	case cmp.Eq != nil:
		return bind(cmp.Eq, p, ctr) + " IN " + labels
	case cmp.Ne != nil:
		return "NOT " + bind(cmp.Ne, p, ctr) + " IN " + labels
	case cmp.In != nil:
		return "any(x IN " + bind(cmp.In, p, ctr) + " WHERE x IN " + labels + ")"
	case cmp.Glob != "":
		return "any(l IN " + labels + " WHERE l =~ " + bind(globToRegex(cmp.Glob), p, ctr) + ")"
	case cmp.Lt != nil:
		return "any(l IN " + labels + " WHERE l < " + bind(cmp.Lt, p, ctr) + ")"
	case cmp.Le != nil:
		return "any(l IN " + labels + " WHERE l <= " + bind(cmp.Le, p, ctr) + ")"
	case cmp.Gt != nil:
		return "any(l IN " + labels + " WHERE l > " + bind(cmp.Gt, p, ctr) + ")"
	case cmp.Ge != nil:
		return "any(l IN " + labels + " WHERE l >= " + bind(cmp.Ge, p, ctr) + ")"
	default:
		return "true"
	}
}

// orderLimitClause renders ORDER BY + LIMIT over node as the reference does: the
// named fields then (created_at, id) for a total order, missing values last.
func orderLimitClause(keys []ranke.OrderKey, limit int, node string) string {
	var b strings.Builder
	b.WriteString("\nORDER BY ")
	for _, k := range keys {
		b.WriteString(orderTerm(k, node) + ", ")
	}
	// Natural order (created_at, id) — the tiebreak, always last.
	b.WriteString(node + ".created_at, " + node + ".id")
	if limit > 0 {
		// One past the cap: the extra row is how the caller learns more existed, and
		// it is trimmed before the results are returned.
		b.WriteString("\nLIMIT " + strconv.Itoa(limit+1))
	}
	return b.String()
}

// orderTerm renders one sort key as Cypher: missing values last, numeric keys
// coerced with toFloat, type sorted on its label.
func orderTerm(k ranke.OrderKey, node string) string {
	dir := ""
	if k.Dir == ranke.SortDesc {
		dir = " DESC"
	}
	if k.Field == "type" {
		lbl := "head(labels(" + node + "))"
		return lbl + " IS NULL, " + lbl + dir
	}
	if k.Compare == ranke.CompareTemporal && k.Field == "dated" {
		// datedMidProperty is projected at write time (claimParam) — the storage
		// layer's own tactic, since Cypher cannot parse EDTF (R-QTEMPORAL).
		mid := node + "." + datedMidProperty
		return mid + " IS NULL, " + mid + dir
	}
	acc := node + "." + k.Field
	key := acc
	if k.Compare == ranke.CompareNumeric || intField[k.Field] {
		key = "toFloat(" + acc + ")"
	}
	return acc + " IS NULL, " + key + dir
}

// isNumeric reports whether v is a Go numeric type, the operand kind that makes a
// comparison numeric (compareOrdered's asFloat path).
func isNumeric(v any) bool {
	switch v.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return true
	default:
		return false
	}
}

// toFloatVal coerces a numeric any to float64 for a bound parameter.
func toFloatVal(v any) float64 {
	switch n := v.(type) {
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case uint64:
		return float64(n)
	case float64:
		return n
	case float32:
		return float64(n)
	default:
		return 0
	}
}
