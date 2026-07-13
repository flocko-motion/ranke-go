package ranke

// EdgeClass is the closed top-level vocabulary for edge types.
type EdgeClass string

const (
	EdgeDerivation   EdgeClass = "derivation"
	EdgeRelation     EdgeClass = "relation"
	EdgeContribution EdgeClass = "contribution"
)

// Closed contribution/* edge type strings, for EdgeConfig.Type. Branch
// and Prune are edge-only — no claim counterpart.
const (
	EdgeContributor = string(EdgeContribution) + "/contributor"
	EdgeHead        = string(EdgeContribution) + "/head"
	EdgeBranches    = string(EdgeContribution) + "/branches"
	EdgeBranch      = string(EdgeContribution) + "/branch"
	EdgePrune       = string(EdgeContribution) + "/prune"
)

// RelationDirection tags an entity's role on a relation/* edge (§4.7):
// zero = not a relation edge, RelationFrom (+1) / RelationTo (-1)
// otherwise. All-from or all-to expresses a symmetric relation.
type RelationDirection int8

const (
	RelationFrom RelationDirection = 1
	RelationTo   RelationDirection = -1
)
