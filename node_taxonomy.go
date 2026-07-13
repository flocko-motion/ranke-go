package ranke

// --- Node type vocabulary (spec §4.8) ---

// NodeClass is the closed top-level vocabulary for node types.
type NodeClass string

const (
	NodeSource       NodeClass = "source"
	NodeDerivation   NodeClass = "derivation"
	NodeEntity       NodeClass = "entity"
	NodeRelation     NodeClass = "relation"
	NodeContribution NodeClass = "contribution"
)

// EncodingClass is the closed top-level MIME vocabulary (RFC 6838
// media types); the subtype is open.
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
