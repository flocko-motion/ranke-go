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

type NodeSubtype string

const (
	NodeSubtypeBranches         NodeSubtype = "branches"
	NodeSubtypeBranchAlias      NodeSubtype = "b"
	NodeSubtypeContributor      NodeSubtype = "contributor"
	NodeSubtypeContributorAlias NodeSubtype = "c"
	NodeSubtypeDiff             NodeSubtype = "diff"
	NodeSubtypeDiffAlias        NodeSubtype = "d"
	NodeSubtypeHead             NodeSubtype = "head"
	NodeSubtypeHeadAlias        NodeSubtype = "h"
)

// nodeSubtypeToAlias / nodeSubtypeFromAlias convert the closed node
// subtypes; open-vocabulary subtypes pass through unchanged.
func nodeSubtypeToAlias(s NodeSubtype) NodeSubtype {
	switch s {
	case NodeSubtypeContributor:
		return NodeSubtypeContributorAlias
	case NodeSubtypeBranch:
		return NodeSubtypeBranchAlias
	case NodeSubtypeDiff:
		return NodeSubtypeDiffAlias
	default:
		return s
	}
}

func nodeSubtypeFromAlias(s NodeSubtype) NodeSubtype {
	switch s {
	case NodeSubtypeContributorAlias:
		return NodeSubtypeContributor
	case NodeSubtypeBranchAlias:
		return NodeSubtypeBranch
	case NodeSubtypeDiffAlias:
		return NodeSubtypeDiff
	default:
		return s
	}
}

// Closed contribution/* node type strings, for ClaimBuilder.Type.
const (
	NodeTypeContributor = string(NodeClassContribution) + "/" + string(NodeSubtypeContributor)
	NodeTypeHead        = string(NodeClassContribution) + "/" + string(NodeSubtypeHead)
	NodeTypBranches     = string(NodeClassContribution) + "/" + string(NodeSybtypeBranches)
)

// EncodingClass is the closed top-level MIME vocabulary (RFC 6838
// media types); the subtype is open.
type EncodingClass string

const (
	encApplication      EncodingClass = "application"
	encApplicationAlias EncodingClass = "a"
	encAudio            EncodingClass = "audio"
	encAudioAlias       EncodingClass = "A"
	encExample          EncodingClass = "example"
	encExampleAlias     EncodingClass = "e"
	encFont             EncodingClass = "font"
	encFontAlias        EncodingClass = "f"
	encImage            EncodingClass = "image"
	encImageAlias       EncodingClass = "i"
	encMessage          EncodingClass = "message"
	encMessageAlias     EncodingClass = "m"
	encModel            EncodingClass = "model"
	encModelAlias       EncodingClass = "l"
	encMultipart        EncodingClass = "multipart"
	encMultipartAlias   EncodingClass = "M"
	encText             EncodingClass = "text"
	encTextAlias        EncodingClass = "t"
	encVideo            EncodingClass = "video"
	encVideoAlias       EncodingClass = "V"
)

// encodingClassToAlias / encodingClassFromAlias convert the closed MIME
// top-level types; unknown values pass through unchanged.
func encodingClassToAlias(c EncodingClass) EncodingClass {
	switch c {
	case encApplication:
		return encApplicationAlias
	case encAudio:
		return encAudioAlias
	case encExample:
		return encExampleAlias
	case encFont:
		return encFontAlias
	case encImage:
		return encImageAlias
	case encMessage:
		return encMessageAlias
	case encModel:
		return encModelAlias
	case encMultipart:
		return encMultipartAlias
	case encText:
		return encTextAlias
	case encVideo:
		return encVideoAlias
	default:
		return c
	}
}

func encodingClassFromAlias(c EncodingClass) EncodingClass {
	switch c {
	case encApplicationAlias:
		return encApplication
	case encAudioAlias:
		return encAudio
	case encExampleAlias:
		return encExample
	case encFontAlias:
		return encFont
	case encImageAlias:
		return encImage
	case encMessageAlias:
		return encMessage
	case encModelAlias:
		return encModel
	case encMultipartAlias:
		return encMultipart
	case encTextAlias:
		return encText
	case encVideoAlias:
		return encVideo
	default:
		return c
	}
}

// EncodingSubtype is open, but well known subtypes should exist with aliases
type EncodingSubtype string

const (
// TODO: add well known subtypes and aliases plus a two way alias resolution
)
