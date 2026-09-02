// package: ranke / verify
// type:    logic
// job:     the per-claim rules read off a claim's own shape — `V-TYPE` type classes, `V-REL`
// relation_direction, `V-EORDER` the inlined edge order, `R-DREQUEST` a delete mark's target,
// `V-ARCHIVEHEIGHT` the initial branch table's height
// limits:  needs no Universe read and no reference resolution, so a record that arrived as
// bytes is judged as readily as one this library built
package ranke

import (
	"bytes"
	"context"
	"strconv"
)

// ruleEdgeOrder: `V-EORDER` — edges inlined ascending by id(e). The order is part of
// S(v) and so of the id, so another order is a second stored form of one claim. Reads
// the claim's own edges: what is judged is the record as stored.
func ruleEdgeOrder(_ context.Context, t *claimUnderVerification) error {
	edges := t.claim.unwrap().edges
	for i := 1; i < len(edges); i++ {
		if bytes.Compare(idBytes(edges[i-1].id), idBytes(edges[i].id)) > 0 {
			return WithDetail(ErrEdgeOrder, "edge "+edges[i].id.String()+" sorts before "+
				edges[i-1].id.String()+", which precedes it")
		}
	}
	return nil
}

// ruleTypeClasses: `V-TYPE` — the node's class and every edge's class is one of the
// fixed set. The subtype is open vocabulary, so nothing here judges it.
func ruleTypeClasses(_ context.Context, t *claimUnderVerification) error {
	if !validNodeClass(NodeClass(t.claim.Node().TypeClass())) {
		return WithDetail(ErrUnknownTypeClass, "node "+t.claim.Node().Type())
	}
	for _, e := range t.claim.Edges() {
		if !validEdgeClass(EdgeClass(e.TypeClass())) {
			return WithDetail(ErrUnknownTypeClass, "edge "+e.Type())
		}
	}
	return nil
}

// ruleRelationDirection (per edge): `V-REL` — a relation/* edge carries 1 (from) or
// -1 (to), an edge of any other class carries 0.
func ruleRelationDirection(_ context.Context, e Edge, _ *claimUnderVerification) error {
	dir := e.RelationDirection()
	if e.TypeClass() == EdgeClassRelation {
		if dir != RelationFrom && dir != RelationTo {
			return WithDetail(ErrRelationDirection, e.Type())
		}
		return nil
	}
	if dir != 0 {
		return WithDetail(ErrRelationDirection, e.Type())
	}
	return nil
}

// ruleDeleteMarkShape: `R-DREQUEST` — a contribution/delete claim carries a
// contribution/delete edge to its target. `R-DGAP` leans on that shape: a mark without
// it reads to a person as explaining a gap while explaining none.
func ruleDeleteMarkShape(_ context.Context, t *claimUnderVerification) error {
	if t.claim.Node().Type() != NodeDelete {
		return nil
	}
	for _, e := range t.claim.Edges() {
		if e.Type() == EdgeTypeDelete {
			return nil
		}
	}
	return WithDetail(ErrDeleteMarkNoTarget, t.claim.ID().String())
}

// ruleArchiveFirstTableHeight: `V-ARCHIVEHEIGHT` — the first branch table stands on its
// contributor edge alone, so at height 1. A lineage edge (`R-C6MERGE`) is what tells
// every later table apart, which is why no reference need be resolved.
func ruleArchiveFirstTableHeight(_ context.Context, t *claimUnderVerification) error {
	edges := t.claim.Edges()
	if t.claim.Node().Type() != NodeBranches || len(edges) != 1 || edges[0].Type() != EdgeTypeContributor {
		return nil
	}
	if h := t.claim.Node().Height(); h != 1 {
		return WithDetail(ErrArchiveFirstTableHeight, "height "+strconv.FormatUint(h, 10))
	}
	return nil
}
