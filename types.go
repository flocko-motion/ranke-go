package ranke

// Closed-vocabulary enums and the helper functions that build full
// "class/sub" type strings from them. Open-vocabulary subtypes are
// passed as plain strings. The concrete implementation structs that
// used to live here moved to their respective entity files.

// NodeClass is the closed top-level vocabulary for node types (spec §4.8).
type NodeClass string

const (
	NodeSource       NodeClass = "source"
	NodeDerivation   NodeClass = "derivation"
	NodeEntity       NodeClass = "entity"
	NodeRelation     NodeClass = "relation"
	NodeContribution NodeClass = "contribution"
)

// EdgeClass is the closed top-level vocabulary for edge types (spec §4.8).
type EdgeClass string

const (
	EdgeDerivation   EdgeClass = "derivation"
	EdgeRelation     EdgeClass = "relation"
	EdgeContribution EdgeClass = "contribution"
)

// EncodingClass is the closed top-level vocabulary for MIME types
// (RFC 6838 IANA-registered top-level media types). Subtype is open.
type EncodingClass string

const (
	encApplication EncodingClass = "application"
	encAudio       EncodingClass = "audio"
	encExample     EncodingClass = "example"
	encFont        EncodingClass = "font"
	encImage       EncodingClass = "image"
	encMessage     EncodingClass = "message"
	encModel       EncodingClass = "model"
	encMultipart   EncodingClass = "multipart"
	encText        EncodingClass = "text"
	encVideo       EncodingClass = "video"
)

// Encoding prefix functions for the ten RFC 6838 top-level MIME
// classes: pass the subtype, get the full media type string.
//
//	ranke.EncodingText("plain")      // "text/plain"
//	ranke.EncodingMessage("rfc822")  // "message/rfc822"
func EncodingApplication(sub string) string { return encType(encApplication, sub) }
func EncodingAudio(sub string) string       { return encType(encAudio, sub) }
func EncodingExample(sub string) string     { return encType(encExample, sub) }
func EncodingFont(sub string) string        { return encType(encFont, sub) }
func EncodingImage(sub string) string       { return encType(encImage, sub) }
func EncodingMessage(sub string) string     { return encType(encMessage, sub) }
func EncodingModel(sub string) string       { return encType(encModel, sub) }
func EncodingMultipart(sub string) string   { return encType(encMultipart, sub) }
func EncodingText(sub string) string        { return encType(encText, sub) }
func EncodingVideo(sub string) string       { return encType(encVideo, sub) }

func encType(class EncodingClass, sub string) string { return string(class) + "/" + sub }

// Type prefix functions for the four open-vocabulary node classes
// (spec §4.8). For the closed contribution/* set, use the Node* /
// Edge* string constants below.
//
//	ranke.TypeSource("email")     // "source/email"
//	ranke.TypeRelation("likes")   // "relation/likes"
func TypeSource(sub string) string     { return nodeType(NodeSource, sub) }
func TypeDerivation(sub string) string { return nodeType(NodeDerivation, sub) }
func TypeEntity(sub string) string     { return nodeType(NodeEntity, sub) }
func TypeRelation(sub string) string   { return nodeType(NodeRelation, sub) }

func nodeType(class NodeClass, sub string) string { return string(class) + "/" + sub }

// Full "class/sub" type strings for the closed contribution/* set
// (spec §Type Vocabulary). Use these as the value of ClaimBuilder.Type
// or EdgeConfig.Type.
const (
	NodeContributor = string(NodeContribution) + "/contributor"
	NodeHead        = string(NodeContribution) + "/head"
	NodeBranches    = string(NodeContribution) + "/branches"

	// Edge types. Branch and Prune are edge-only — no claim counterpart.
	EdgeContributor = string(EdgeContribution) + "/contributor"
	EdgeHead        = string(EdgeContribution) + "/head"
	EdgeBranches    = string(EdgeContribution) + "/branches"
	EdgeBranch      = string(EdgeContribution) + "/branch"
	EdgePrune       = string(EdgeContribution) + "/prune"
)

// RelationDirection tags an entity's role on a relation/* edge (§4.7).
// Zero = not a relation edge. RelationFrom (+1) / RelationTo (-1) for
// relation/* edges. All-from or all-to expresses a symmetric relation.
type RelationDirection int8

const (
	RelationFrom RelationDirection = 1
	RelationTo   RelationDirection = -1
)
