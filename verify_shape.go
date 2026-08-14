// package: ranke / verify
// type:    logic
// job:     the per-claim rules read off a claim's own shape — `V-TYPE` type classes, `V-REL`
// relation_direction, `R-DREQUEST` a delete mark's target — the first two enforced at
// construction too, the third only from here
// limits:  needs no Universe read and no reference resolution, so it judges a record that
// arrived as bytes or through AssembleClaim as readily as one this library built
package ranke

import "context"

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

// ruleDeleteMarkShape: `R-DREQUEST` — a contribution/delete claim documents a deletion
// by carrying a contribution/delete edge to its target. One without that edge marks
// nothing, while reading to a person as though it did.
//
// `R-DGAP` leans on this shape: a malformed mark leaves the gap it was meant to explain
// unexplained, and nothing else would say so.
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
