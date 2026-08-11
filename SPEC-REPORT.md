# Spec report — V-DIFFEDGE

From td-0636a5, which checked V-DIFF and V-DIFFEDGE against `computeDiffFields`,
`computeDiffEdges` and `contentSource`. One disagreement, and one item that was
investigated and is not one.

A courier, not an artifact: delete it once the spec team has it.

## Finding — V-DIFFEDGE describes a tolerance the implementation does not have

### The rule

> Its own unnamed edges — its `contribution/contributor` and `contribution/diff`
> among them — are its alone and never inherit.

### The code

`checkDiffEdgeNames` (`claim_builder.go:321`) refuses every unnamed edge on a diff
claim except two, matched by type rather than by name:

```go
if e.typeClass == EdgeClassContribution &&
    (e.typeSub == "contributor" || e.typeSub == string(EdgeSubtypeDiff)) {
    continue
}
name, ok := e.fields[FieldName]
if !ok || name == "" {
    return errDiffEdgeUnnamed
}
```

Anything else lacking a non-empty name fails at construction with
`errDiffEdgeUnnamed` (`errors.go:60`). The exempt set has exactly two members and
is hard-coded.

### The mismatch

"Among them" reads as two examples drawn from a larger set, so the rule describes a
diff claim that may carry arbitrary unnamed edges which then never inherit. The
implementation admits exactly two, and no third is possible.

The properties the rule states for a diff claim's *own* unnamed edges are therefore
unreachable in practice. The branch in `computeDiffEdges` (`claim.go:145`) that
appends self's unnamed edges runs only for the contributor and diff edges.

### Recommendation — narrow the rule

The restriction is load-bearing, so the rule is the side to change. Suggested
wording:

> A diff claim carries no unnamed edges beyond its `contribution/contributor` and
> `contribution/diff` edges, which are its alone and never inherit.

That states the closed set rather than implying an open one.

Two reasons the code should keep the restriction.

**Overlay is name-keyed, so an unnamed edge would be unrevisable.**
`computeDiffEdges` inherits, omits and overlays through a `map[string]*edge` keyed
by the `name` field. An unnamed edge has no key, so no successor delta could
replace it, omit it, or restate it. Admitting one would create an edge that is
permanently fixed from the moment it is written — in a format whose whole purpose
is revision by successive deltas.

**The two exemptions are principled, and the reasoning does not extend.**
`checkEdgeCardinality` (`claim_builder.go:342`) caps a claim at one
`contribution/contributor` and one `contribution/diff` edge. Those two therefore
take their identity from their type: "the contributor edge" names exactly one edge
without a `name` field. No other edge type carries that cap, so no other type could
be identified without a name. The closed set is not an arbitrary limit; it is the
set of edges that can work under name-keyed overlay.

Against relaxing the code: an unnamed edge on a delta might look convenient for
adding provenance without inventing a name — a `derivation/*` edge citing a new
source, say. Naming it costs a single field, and the unnamed form buys a
write-once edge no later revision can touch. There is no case for it that
naming does not serve better.

One consequence worth noting, since it shows the restriction is workable rather
than merely tolerable. A `derivation/*` claim must carry at least one
`derivation/*` edge (the §3.5 provenance invariant), so a diff delta of a
derivation claim must carry a *named* derivation edge. That combination is
reachable and is what `TestDiffEdgeUnnamedDoesNotInherit` builds.

### Evidence

`TestDiffClaimRefusesUnnamedEdge` (`claim_diff_rules_test.go:219`) asserts the
refusal, expecting `errDiffEdgeUnnamed`.

`TestDiffEdgeOmitCannotReachTheStructuralEdges` shows the two exempt edges survive
an `edges_diff_omit` list that names them, since omission deletes from the by-name
map they are absent from.

## Investigated and not a finding — the omit list

An earlier draft of this work reported the omit list's inheritance as a second
disagreement. **It is not one, and no rule change is called for.** It is recorded
here so the item is closed rather than reaching the spec team secondhand.

V-DIFF reads:

> An omit list is an ordinary field, inherited as data, and applies only where the
> claim itself states it.

Both halves hold in `computeDiffFields` (`node.go:102`):

- *inherited as data* — the merged map is seeded from `n.diffNode.fieldMap()`, so a
  claim stating no omit of its own still reports its predecessor's list.
- *applies only where the claim itself states it* — the drop reads
  `n.fields[FieldFieldsDiffOmit]`, the claim's own fields, never the merged map. An
  inherited list drops nothing.

The earlier report came from a paraphrase whose closing clause said the list does
not inherit down a chain. The rule as written says the opposite and is correct.

Both halves are pinned by tests: `TestDiffOmitListIsItselfAnInheritedField` and
`TestDiffOmitEffectDoesNotInheritDownAChain`.

The behaviour is worth knowing even though it needs no change. A materialised claim
reports an omit list it never stated, so a client reading `fields_diff_omit` off one
learns nothing about what was dropped at that link.
