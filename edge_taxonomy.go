// package: ranke / taxonomy
// type:    logic
// job:     the edge-type vocabulary (§4.8) — the closed class set, the subtypes the ADT defines,
// and their compact aliases — with enumeration/validation helpers
// limits:  vocabulary only; edge construction and matching live elsewhere (-> edge, filter)
package ranke

// EdgeClass is the closed top-level vocabulary for edge types.
type EdgeClass string

const (
	EdgeClassContribution      EdgeClass = "contribution"
	EdgeClassContributionAlias EdgeClass = "c"
	EdgeClassDerivation        EdgeClass = "derivation"
	EdgeClassDerivationAlias   EdgeClass = "d"
	EdgeClassRelation          EdgeClass = "relation"
	EdgeClassRelationAlias     EdgeClass = "r"
)

// EdgeClasses lists every edge class, for validation and enumeration.
var EdgeClasses = []EdgeClass{
	EdgeClassDerivation,
	EdgeClassRelation,
	EdgeClassContribution,
}

// EdgeSubtype is a contribution/* edge subtype. Every subtype vocabulary is open
// (paper 1 §Type Vocabulary); these are the ones the ADT gives meaning to, so a
// verifier acts on them and passes any other through as an ordinary reference.
type EdgeSubtype string

const (
	EdgeSubtypeContributor      EdgeSubtype = "contributor"
	EdgeSubtypeContributorAlias EdgeSubtype = "c"
	EdgeSubtypeHead             EdgeSubtype = "head"
	EdgeSubtypeHeadAlias        EdgeSubtype = "h"
	EdgeSubtypeBranches         EdgeSubtype = "branches"
	EdgeSubtypeBranchesAlias    EdgeSubtype = "B"
	EdgeSubtypeBranch           EdgeSubtype = "branch"
	EdgeSubtypeBranchAlias      EdgeSubtype = "b"
	EdgeSubtypePrune            EdgeSubtype = "prune"
	EdgeSubtypePruneAlias       EdgeSubtype = "p"
	EdgeSubtypeDiff             EdgeSubtype = "diff"
	EdgeSubtypeDiffAlias        EdgeSubtype = "d"
	// A limiting claim points at its target through an edge of its own class
	// (paper 1 §Type Vocabulary): delete documents a gap where bytes were, expiry
	// names the last time a contributor's key is valid.
	EdgeSubtypeDelete      EdgeSubtype = "delete"
	EdgeSubtypeDeleteAlias EdgeSubtype = "x"
	EdgeSubtypeExpiry      EdgeSubtype = "expiry"
	EdgeSubtypeExpiryAlias EdgeSubtype = "e"
)

// edgeClassToAlias / edgeClassFromAlias convert the closed edge classes;
// unknown values pass through unchanged.
func edgeClassToAlias(c EdgeClass) EdgeClass {
	switch c {
	case EdgeClassContribution:
		return EdgeClassContributionAlias
	case EdgeClassDerivation:
		return EdgeClassDerivationAlias
	case EdgeClassRelation:
		return EdgeClassRelationAlias
	default:
		return c
	}
}

func edgeClassFromAlias(c EdgeClass) EdgeClass {
	switch c {
	case EdgeClassContributionAlias:
		return EdgeClassContribution
	case EdgeClassDerivationAlias:
		return EdgeClassDerivation
	case EdgeClassRelationAlias:
		return EdgeClassRelation
	default:
		return c
	}
}

// edgeSubtypeToAlias / edgeSubtypeFromAlias convert the closed
// contribution/* edge subtypes; open-vocabulary subtypes pass through
// unchanged.
func edgeSubtypeToAlias(s EdgeSubtype) EdgeSubtype {
	switch s {
	case EdgeSubtypeContributor:
		return EdgeSubtypeContributorAlias
	case EdgeSubtypeHead:
		return EdgeSubtypeHeadAlias
	case EdgeSubtypeBranches:
		return EdgeSubtypeBranchesAlias
	case EdgeSubtypeBranch:
		return EdgeSubtypeBranchAlias
	case EdgeSubtypePrune:
		return EdgeSubtypePruneAlias
	case EdgeSubtypeDiff:
		return EdgeSubtypeDiffAlias
	case EdgeSubtypeDelete:
		return EdgeSubtypeDeleteAlias
	case EdgeSubtypeExpiry:
		return EdgeSubtypeExpiryAlias
	default:
		return s
	}
}

func edgeSubtypeFromAlias(s EdgeSubtype) EdgeSubtype {
	switch s {
	case EdgeSubtypeContributorAlias:
		return EdgeSubtypeContributor
	case EdgeSubtypeHeadAlias:
		return EdgeSubtypeHead
	case EdgeSubtypeBranchesAlias:
		return EdgeSubtypeBranches
	case EdgeSubtypeBranchAlias:
		return EdgeSubtypeBranch
	case EdgeSubtypePruneAlias:
		return EdgeSubtypePrune
	case EdgeSubtypeDiffAlias:
		return EdgeSubtypeDiff
	case EdgeSubtypeDeleteAlias:
		return EdgeSubtypeDelete
	case EdgeSubtypeExpiryAlias:
		return EdgeSubtypeExpiry
	default:
		return s
	}
}

// Closed contribution/* edge type strings, for EdgeConfig.Type — each
// combined from its class and subtype constants. Branch and Prune are
// edge-only — no claim counterpart.
const (
	EdgeTypeContributor = string(EdgeClassContribution) + "/" + string(EdgeSubtypeContributor)
	EdgeTypeHead        = string(EdgeClassContribution) + "/" + string(EdgeSubtypeHead)
	EdgeTypeBranches    = string(EdgeClassContribution) + "/" + string(EdgeSubtypeBranches)
	EdgeTypeBranch      = string(EdgeClassContribution) + "/" + string(EdgeSubtypeBranch)
	EdgeTypePrune       = string(EdgeClassContribution) + "/" + string(EdgeSubtypePrune)
	EdgeTypeDiff        = string(EdgeClassContribution) + "/" + string(EdgeSubtypeDiff)
	EdgeTypeDelete      = string(EdgeClassContribution) + "/" + string(EdgeSubtypeDelete)
	EdgeTypeExpiry      = string(EdgeClassContribution) + "/" + string(EdgeSubtypeExpiry)
)

// RelationDirection tags an entity's role on a relation/* edge (§4.7):
// zero = not a relation edge, RelationFrom (+1) / RelationTo (-1)
// otherwise. All-from or all-to expresses a symmetric relation.
type RelationDirection int8

const (
	RelationFrom RelationDirection = 1
	RelationTo   RelationDirection = -1
)
