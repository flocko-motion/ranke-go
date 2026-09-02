// package: ranke / taxonomy
// type:    logic
// job:     the node-type vocabulary (§4.8) — the closed class set, the subtypes the ADT defines,
// and their compact aliases — with enumeration/validation helpers
// limits:  vocabulary only; node construction and content live elsewhere (-> node)
package ranke

// --- Node type vocabulary (spec §4.8) ---

// NodeClass is the closed top-level vocabulary for node types.
type NodeClass string

const (
	NodeClassContribution      NodeClass = "contribution"
	NodeClassContributionAlias NodeClass = "c"
	NodeClassSource            NodeClass = "source"
	NodeClassSourceAlias       NodeClass = "s"
	NodeClassDerivation        NodeClass = "derivation"
	NodeClassDerivationAlias   NodeClass = "d"
	NodeClassEntity            NodeClass = "entity"
	NodeClassEntityAlias       NodeClass = "e"
	NodeClassRelation          NodeClass = "relation"
	NodeClassRelationAlias     NodeClass = "r"
)

// nodeClassToAlias maps a canonical node class to its single-char alias;
// unknown / already-aliased values pass through unchanged.
func nodeClassToAlias(c NodeClass) NodeClass {
	switch c {
	case NodeClassContribution:
		return NodeClassContributionAlias
	case NodeClassSource:
		return NodeClassSourceAlias
	case NodeClassDerivation:
		return NodeClassDerivationAlias
	case NodeClassEntity:
		return NodeClassEntityAlias
	case NodeClassRelation:
		return NodeClassRelationAlias
	default:
		return c
	}
}

// nodeClassFromAlias maps a single-char alias back to its canonical node
// class; canonical / unknown values pass through unchanged.
func nodeClassFromAlias(c NodeClass) NodeClass {
	switch c {
	case NodeClassContributionAlias:
		return NodeClassContribution
	case NodeClassSourceAlias:
		return NodeClassSource
	case NodeClassDerivationAlias:
		return NodeClassDerivation
	case NodeClassEntityAlias:
		return NodeClassEntity
	case NodeClassRelationAlias:
		return NodeClassRelation
	default:
		return c
	}
}

// NodeSubtype is the second-level node-type vocabulary (the "/sub" part).
type NodeSubtype string

// "branch" and "diff" are absent: both are edge subtypes alone. A branch is named
// by a contribution/branch edge on the table, and diff-ness lives in the
// contribution/diff edge, so no node carries either. @tbl:aliases still assigns
// them b and d — one table shared by nodes and edges — which the edge side holds.
const (
	NodeSubtypeBranches         NodeSubtype = "branches"
	NodeSubtypeBranchesAlias    NodeSubtype = "B"
	NodeSubtypeContributor      NodeSubtype = "contributor"
	NodeSubtypeContributorAlias NodeSubtype = "c"
	NodeSubtypeHead             NodeSubtype = "head"
	NodeSubtypeHeadAlias        NodeSubtype = "h"
	// The limiting claims (paper 1 §Type Vocabulary). Each takes the letter its
	// edge subtype takes, as contributor, head and diff already do.
	NodeSubtypeDelete      NodeSubtype = "delete"
	NodeSubtypeDeleteAlias NodeSubtype = "x"
	NodeSubtypeExpiry      NodeSubtype = "expiry"
	NodeSubtypeExpiryAlias NodeSubtype = "e"
)

// nodeSubtypeToAlias / nodeSubtypeFromAlias convert the closed node
// subtypes; open-vocabulary subtypes pass through unchanged.
func nodeSubtypeToAlias(s NodeSubtype) NodeSubtype {
	switch s {
	case NodeSubtypeContributor:
		return NodeSubtypeContributorAlias
	case NodeSubtypeBranches:
		return NodeSubtypeBranchesAlias
	case NodeSubtypeHead:
		return NodeSubtypeHeadAlias
	case NodeSubtypeDelete:
		return NodeSubtypeDeleteAlias
	case NodeSubtypeExpiry:
		return NodeSubtypeExpiryAlias
	default:
		return s
	}
}

func nodeSubtypeFromAlias(s NodeSubtype) NodeSubtype {
	switch s {
	case NodeSubtypeContributorAlias:
		return NodeSubtypeContributor
	case NodeSubtypeBranchesAlias:
		return NodeSubtypeBranches
	case NodeSubtypeHeadAlias:
		return NodeSubtypeHead
	case NodeSubtypeDeleteAlias:
		return NodeSubtypeDelete
	case NodeSubtypeExpiryAlias:
		return NodeSubtypeExpiry
	default:
		return s
	}
}

// Closed contribution/* node type strings, for ClaimBuilder.Type.
const (
	NodeTypeContributor = string(NodeClassContribution) + "/" + string(NodeSubtypeContributor)
	NodeTypeHead        = string(NodeClassContribution) + "/" + string(NodeSubtypeHead)
	NodeTypBranches     = string(NodeClassContribution) + "/" + string(NodeSubtypeBranches)
)
