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

func nodeClassToAlias(c NodeClass) NodeClass {
	switch c {
	case NodeClassSource:
		return NodeClassSourceAlias
	// TODO: implement full two way conversion for both nodes and edges
	default:
		return c
	}
}

type NodeSubtype string

const (
	NodeSubtypeContributor      NodeSubtype = "contributor"
	NodeSubtypeContributorAlias NodeSubtype = "c"
	NodeSubtypeBranch           NodeSubtype = "branch"
	NodeSubtypeBranchAlias      NodeSubtype = "b"
	NodeSybtypeDiff             NodeSubtype = "diff"
	NodeSybtypeDiffAlias        NodeSubtype = "d"
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
