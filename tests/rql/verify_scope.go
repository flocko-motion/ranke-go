// package: tests/rql / integration
// type:    tool
// job:     the answer rule of §select — every claim an answer returns is in the scope's graph,
// asked of ClaimsInBranches so the check is a membership test rather than a walk
// limits:  a $universe scope and the narrowing by Select.Head go unchecked: both are membership
// in a closure, which only a walk answers, and a walk here would be a second engine
package rql

import (
	"context"
	"errors"
	"fmt"

	"github.com/rankegraph/ranke-go"
)

// ruleScopeMembership: `R-QSCOPE` — only the scope's graph is read, so every claim
// returned belongs to it. One batched ClaimsInBranches call answers the whole answer;
// each returned id is still judged on its own membership bit, and the scope's contents
// are never enumerated.
func ruleScopeMembership(ctx context.Context, t *answerUnderVerification) []Violation {
	if t.arc == nil || t.u == nil {
		return nil // nothing to ask membership of
	}
	scope, ok, err := membershipScope(ctx, t)
	if err != nil {
		return []Violation{{Index: -1, Err: err}}
	}
	if !ok {
		return nil // $universe: nothing cheap to ask
	}

	ids, at := returnedIds(t.results)
	if len(ids) == 0 {
		return nil
	}
	in, err := t.u.ClaimsInBranches(ctx, scope, ids)
	if err != nil {
		if errors.Is(err, ranke.ErrUnsupported) {
			return nil // the backend cannot answer membership; the rule stays silent
		}
		return []Violation{{Index: -1, Err: fmt.Errorf("membership lookup: %w", err)}}
	}
	if len(in) != len(ids) {
		return []Violation{{Index: -1, Err: fmt.Errorf(
			"membership lookup answered %d of %d ids", len(in), len(ids))}}
	}

	var out []Violation
	for i, held := range in {
		if !held {
			out = append(out, Violation{Index: at[i], Err: fmt.Errorf(
				"claim %s is not in scope %q", ids[i].String(), t.q.Select.Branch)})
		}
	}
	return out
}

// membershipScope is the name→head map that names this query's scope, and whether
// ClaimsInBranches can answer for it at all.
func membershipScope(ctx context.Context, t *answerUnderVerification) (map[string]ranke.Id, bool, error) {
	name := t.q.Select.Branch
	switch name {
	case ranke.BranchUniverse:
		return nil, false, nil
	case ranke.BranchArchive:
		return map[string]ranke.Id{ranke.BranchArchive: t.arc.Head()}, true, nil
	}
	b, err := t.arc.GetBranch(ctx, name)
	if err != nil {
		return nil, false, fmt.Errorf("resolve scope %q: %w", name, err)
	}
	return map[string]ranke.Id{name: b.Head()}, true, nil
}

// returnedIds is every claim id the answer names — the endpoint and each hop of a
// route — with the element each came from, so a violation points at its element.
func returnedIds(results []ranke.QueryResult) ([]ranke.Id, []int) {
	var ids []ranke.Id
	var at []int
	seen := map[string]bool{}
	add := func(id ranke.Id, i int) {
		if id == nil || seen[id.String()] {
			return
		}
		seen[id.String()] = true
		ids = append(ids, id)
		at = append(at, i)
	}
	for i, r := range results {
		add(r.ClaimId, i)
		for _, id := range r.PathId {
			add(id, i)
		}
		if r.ClaimNative != nil {
			add(r.ClaimNative.ID(), i)
		}
		for _, c := range r.PathNative {
			if c != nil {
				add(c.ID(), i)
			}
		}
	}
	return ids, at
}
