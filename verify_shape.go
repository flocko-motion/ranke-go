// package: ranke / verify
// type:    logic
// job:     the per-claim rules read off a claim's own shape — `V-TYPE` type classes, `V-REL`
// relation_direction, `V-PROV` provenance — enforced at construction and, from here, on
// anything the verifier walks
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

// ruleProvenance: `V-PROV` — a derivation/*, entity/* or relation/* node carries at
// least one derivation/* edge. A contribution/contributor edge does not satisfy it.
func ruleProvenance(_ context.Context, t *claimUnderVerification) error {
	n := t.claim.Node()
	if !requiresProvenance(NodeClass(n.TypeClass())) {
		return nil
	}
	for _, e := range t.claim.Edges() {
		if e.TypeClass() == EdgeClassDerivation {
			return nil
		}
	}
	return WithDetail(ErrProvenanceMissing, n.Type())
}
