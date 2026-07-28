// package: tests/rql / integration
// type:    tool
// job:     execute a corpus query against any Universe and render its answer as a deterministic fingerprint — the comparable form the conformance matrix diffs across backends
// limits:  no assertions and no backend knowledge; the corpus lives alongside (-> corpus.go) and the comparing is the matrix's job (-> tests/matrix)
package rql

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/flocko-motion/ranke-go"
)

// Answer is one backend's response to one query: a fingerprint per result, in
// the order the stream emitted them. Order is part of the contract, so the slice
// is compared as a sequence, never as a set.
type Answer []string

// Run executes q through the Archive at archiveHead and returns its Answer. It
// goes through the Archive, not Universe.Query, so the query meets the library's
// own scope resolution — a hand-rolled Scope would test the helper's idea of
// $universe/$archive/branch rather than the library's.
func Run(ctx context.Context, u ranke.Universe, q ranke.Query, archiveHead ranke.Id) (Answer, error) {
	arc, err := ranke.NewArchive(ctx, u, archiveHead)
	if err != nil {
		return nil, err
	}
	rs, err := arc.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	var out Answer
	for rs.Next() {
		out = append(out, Fingerprint(rs.Result()))
	}
	if err := rs.Err(); err != nil {
		_ = rs.Close()
		return nil, err
	}
	if err := rs.Close(); err != nil {
		return nil, err
	}
	return out, nil
}

// Fingerprint renders one result deterministically — id, path, claim fields and
// edges, content — so the shape axes are compared, not just which claims came
// back. Two backends can return the same ids and disagree on their contents.
func Fingerprint(r ranke.QueryResult) string {
	var b strings.Builder
	b.WriteString(idOf(r.Id))

	if len(r.Path) > 0 {
		b.WriteString(" path[")
		for i, c := range r.Path {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(idOf(c.ID()))
		}
		b.WriteByte(']')
	}

	// Claim is nil under DetailID — there is nothing to reconstruct — so the
	// fingerprint is the id alone, which is exactly what that detail promises.
	if r.Claim != nil {
		b.WriteString(" claim{")
		b.WriteString(claimFields(r.Claim))
		b.WriteString(edgeList(r.Claim))
		b.WriteByte('}')
	}

	// Content is present only when Output.Content asked for it; hash rather than
	// inline the bytes so a mismatch stays readable, and carry the length so a
	// cutoff at the cap is visible.
	if r.Content != nil {
		sum := sha256.Sum256(r.Content)
		b.WriteString(fmt.Sprintf(" content{len=%d sha=%s}", len(r.Content), hex.EncodeToString(sum[:6])))
	}
	return b.String()
}

// claimFields renders the node's projected fields — the ones RQL can filter and
// order on, so a divergence in any of them shows up here.
func claimFields(c ranke.Claim) string {
	n := c.Node()
	return fmt.Sprintf("type=%s height=%d size=%d encoding=%s created=%s",
		n.Type(), n.Height(), n.GetContentSize(), n.Encoding(),
		n.CreatedAt().UTC().Format("2006-01-02T15:04:05.999999999Z"))
}

// edgeList renders the claim's edges in the order the claim carries them. Order
// is significant: a claim's id commits to its serialized edge sequence, so a
// reconstruction that reorders them would not round-trip to the same id.
func edgeList(c ranke.Claim) string {
	edges := c.Edges()
	if len(edges) == 0 {
		return " edges[]"
	}
	var b strings.Builder
	b.WriteString(" edges[")
	for i, e := range edges {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%s->%s", e.Type(), idOf(e.Reference()))
		if d := e.RelationDirection(); d != 0 {
			fmt.Fprintf(&b, "(%d)", d)
		}
	}
	b.WriteByte(']')
	return b.String()
}

// idOf renders an id, or "-" when absent, so a nil never formats as a panic or
// an empty gap in a fingerprint.
func idOf(id ranke.Id) string {
	if id == nil {
		return "-"
	}
	return id.String()
}

// Describe renders a query's RQL on one line — the compact form the timing
// report prints. Just the meaningful parts: scope, path/closure, filter, order,
// limit.
func Describe(q ranke.Query) string {
	parts := []string{"branch=" + q.Select.Branch}
	if len(q.Select.Path) == 0 {
		parts = append(parts, "closure")
	} else {
		for _, s := range q.Select.Path {
			seg := "path("
			if s.Dir != "" {
				seg += "dir=" + string(s.Dir) + " "
			}
			if len(s.Edges) > 0 {
				seg += "edges=" + strings.Join(s.Edges, ",") + " "
			}
			if len(s.Nodes) > 0 {
				seg += "nodes=" + strings.Join(s.Nodes, ",") + " "
			}
			if s.Max > 0 {
				seg += fmt.Sprintf("depth=%d", s.Max)
			}
			parts = append(parts, strings.TrimSpace(seg)+")")
		}
	}
	if q.Where != nil {
		parts = append(parts, "where "+describeWhere(q.Where))
	}
	for _, k := range q.Order {
		dir := "asc"
		if k.Dir == ranke.SortDesc {
			dir = "desc"
		}
		parts = append(parts, "order("+k.Field+" "+dir+")")
	}
	if q.Limit.Results > 0 {
		parts = append(parts, fmt.Sprintf("limit=%d", q.Limit.Results))
	}
	return strings.Join(parts, " ")
}

func describeWhere(w *ranke.Where) string {
	switch {
	case len(w.And) > 0:
		return "(" + joinWheres(w.And, " and ") + ")"
	case len(w.Or) > 0:
		return "(" + joinWheres(w.Or, " or ") + ")"
	case w.Not != nil:
		return "not " + describeWhere(w.Not)
	case w.Test != nil:
		return w.Field + describeCmp(w.Test)
	}
	return "?"
}

func joinWheres(ws []ranke.Where, sep string) string {
	p := make([]string, len(ws))
	for i := range ws {
		p[i] = describeWhere(&ws[i])
	}
	return strings.Join(p, sep)
}

func describeCmp(c *ranke.Comparison) string {
	switch {
	case c.Glob != "":
		return " glob " + c.Glob
	case c.Eq != nil:
		return fmt.Sprintf(" == %v", c.Eq)
	case c.Ne != nil:
		return fmt.Sprintf(" != %v", c.Ne)
	case c.Lt != nil:
		return fmt.Sprintf(" < %v", c.Lt)
	case c.Le != nil:
		return fmt.Sprintf(" <= %v", c.Le)
	case c.Gt != nil:
		return fmt.Sprintf(" > %v", c.Gt)
	case c.Ge != nil:
		return fmt.Sprintf(" >= %v", c.Ge)
	case len(c.In) > 0:
		return fmt.Sprintf(" in %v", c.In)
	}
	return "?"
}
