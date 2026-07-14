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

// EdgeSubtype is a contribution/* edge subtype. Only contribution/* has a
// closed subtype set; derivation/* and relation/* subtypes are open
// vocabulary.
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
)

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
)

// RelationDirection tags an entity's role on a relation/* edge (§4.7):
// zero = not a relation edge, RelationFrom (+1) / RelationTo (-1)
// otherwise. All-from or all-to expresses a symmetric relation.
type RelationDirection int8

const (
	RelationFrom RelationDirection = 1
	RelationTo   RelationDirection = -1
)
