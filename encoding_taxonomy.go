// package: ranke / taxonomy
// type:    logic
// job:     the encoding (MIME media-type) vocabulary — the closed top-level class set with compact
// aliases, well-known subtype aliases with two-way resolution, the class constructors, and
// named constants for the popular media types
// limits:  vocabulary + alias resolution only; the codec applies the aliases into the canonical
// bytes and the node holds the value (-> codec, node)
package ranke

import "strings"

// IsTextEncoding reports whether a media type carries human-legible text —
// text/*, message/* (e.g. rfc822), and structured-text application types
// (application/json, application/xml, and any +json / +xml suffix). Binary
// types (image/audio/video/font, application/octet-stream, …) return false. An
// empty encoding is treated as non-text. Used to decide whether content is
// worth inlining as readable text vs left to a byte store.
func IsTextEncoding(encoding string) bool {
	switch {
	case encoding == "":
		return false
	case strings.HasPrefix(encoding, "text/"),
		strings.HasPrefix(encoding, "message/"),
		strings.HasSuffix(encoding, "+json"),
		strings.HasSuffix(encoding, "+xml"),
		encoding == EncodingJSON,
		encoding == EncodingXML:
		return true
	}
	return false
}

// --- Top-level class vocabulary (RFC 6838 media types; the subtype is open) ---

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

// --- Subtype vocabulary (open, with compact aliases for well-known types) ---

// EncodingSubtype is the open second-level media type. Every well-known
// subtype carries a compact alias resolved two ways for the canonical encoding.
type EncodingSubtype string

// encodingSubAliases gives every predefined subtype (the sub of each named
// media-type constant below) a single-character alias (one of [a-zA-Z0-9],
// like the class aliases): a predefined type earns its keep only by encoding
// compactly (§180), so every one carries an alias. The codec adds the reserved
// "." prefix on the wire (see aliasToWire), and a literal subtype can never
// start with "." (RFC 6838), so an alias and a literal never collide. Subtype
// aliases live in their own field (EncodingSub), so they may reuse letters the
// class aliases use; the full type's compact form is class-char + sub-char.
//
// APPEND ONLY, never re-map an entry: ids are computed over the aliased bytes,
// so changing a mapping would change the ids of already-stored claims. The
// single-character space caps the table at 62 entries (56 used). A variant
// implementation (e.g. the Python reference) must carry the identical table
// for cross-implementation byte-for-byte reproducibility.
var encodingSubAliases = map[EncodingSubtype]EncodingSubtype{
	// application/*
	"json":              "j",
	"ld+json":           "J",
	"xml":               "x",
	"xhtml+xml":         "X",
	"pdf":               "p",
	"octet-stream":      "o",
	"manifest+json":     "w",
	"rtf":               "r",
	"zip":               "z",
	"gzip":              "g",
	"x-tar":             "t",
	"x-bzip2":           "b",
	"x-7z-compressed":   "7",
	"vnd.rar":           "R",
	"java-archive":      "k",
	"epub+zip":          "e",
	"msword":            "W",
	"vnd.ms-excel":      "E",
	"vnd.ms-powerpoint": "Y",
	"vnd.openxmlformats-officedocument.wordprocessingml.document":   "d",
	"vnd.openxmlformats-officedocument.spreadsheetml.sheet":         "s",
	"vnd.openxmlformats-officedocument.presentationml.presentation": "P",
	"vnd.oasis.opendocument.text":                                   "T",
	"vnd.oasis.opendocument.spreadsheet":                            "S",
	"vnd.oasis.opendocument.presentation":                           "Q",
	// text/*
	"plain":      "n",
	"html":       "h",
	"css":        "c",
	"javascript": "a",
	"csv":        "v",
	"markdown":   "m",
	"calendar":   "l",
	// image/*
	"png":                "G",
	"apng":               "A",
	"jpeg":               "i",
	"gif":                "f",
	"webp":               "B",
	"avif":               "V",
	"svg+xml":            "y",
	"bmp":                "M",
	"tiff":               "F",
	"vnd.microsoft.icon": "I",
	// audio/* (mpeg, ogg, webm are shared with video/* — one sub, one alias)
	"mpeg": "q",
	"aac":  "C",
	"wav":  "U",
	"ogg":  "O",
	"webm": "K",
	"midi": "D",
	// video/*
	"mp4":       "4",
	"x-msvideo": "Z",
	"mp2t":      "2",
	"3gpp":      "3",
	// font/*
	"woff":  "L",
	"woff2": "H",
	"ttf":   "5",
	"otf":   "6",
}

// encodingSubFromAliasMap is the reverse of encodingSubAliases, built once.
var encodingSubFromAliasMap = func() map[EncodingSubtype]EncodingSubtype {
	m := make(map[EncodingSubtype]EncodingSubtype, len(encodingSubAliases))
	for full, alias := range encodingSubAliases {
		m[alias] = full
	}
	return m
}()

// encodingSubToAlias / encodingSubFromAlias convert well-known subtypes to and
// from their bare compact alias; unknown values pass through unchanged.
func encodingSubToAlias(s EncodingSubtype) EncodingSubtype {
	if a, ok := encodingSubAliases[s]; ok {
		return a
	}
	return s
}

func encodingSubFromAlias(s EncodingSubtype) EncodingSubtype {
	if full, ok := encodingSubFromAliasMap[s]; ok {
		return full
	}
	return s
}

// --- Class constructors ---

// EncodingApplication returns the "application/<sub>" media type.
func EncodingApplication(sub string) string { return encType(encApplication, sub) }

// EncodingAudio returns the "audio/<sub>" media type.
func EncodingAudio(sub string) string { return encType(encAudio, sub) }

// EncodingExample returns the "example/<sub>" media type.
func EncodingExample(sub string) string { return encType(encExample, sub) }

// EncodingFont returns the "font/<sub>" media type.
func EncodingFont(sub string) string { return encType(encFont, sub) }

// EncodingImage returns the "image/<sub>" media type.
func EncodingImage(sub string) string { return encType(encImage, sub) }

// EncodingMessage returns the "message/<sub>" media type.
func EncodingMessage(sub string) string { return encType(encMessage, sub) }

// EncodingModel returns the "model/<sub>" media type.
func EncodingModel(sub string) string { return encType(encModel, sub) }

// EncodingMultipart returns the "multipart/<sub>" media type.
func EncodingMultipart(sub string) string { return encType(encMultipart, sub) }

// EncodingText returns the "text/<sub>" media type.
func EncodingText(sub string) string { return encType(encText, sub) }

// EncodingVideo returns the "video/<sub>" media type.
func EncodingVideo(sub string) string { return encType(encVideo, sub) }

func encType(class EncodingClass, sub string) string { return string(class) + "/" + sub }

// --- Named media-type constants (the popular MDN/IANA "common types" set) ---
//
// Each names a full media type so callers write ranke.EncodingJSON rather than
// EncodingApplication("json"); each equals what the matching class constructor
// produces, so they are interchangeable. For a type not listed, call the class
// constructor directly, e.g. EncodingApplication("vnd.custom+json").

// Application media types (application/*): structured data, documents, archives.
const (
	EncodingJSON        = "application/json"
	EncodingJSONLD      = "application/ld+json"
	EncodingXML         = "application/xml"
	EncodingXHTML       = "application/xhtml+xml"
	EncodingPDF         = "application/pdf"
	EncodingOctetStream = "application/octet-stream"
	EncodingWebManifest = "application/manifest+json"
	EncodingRTF         = "application/rtf"

	// Archives / compression.
	EncodingZIP   = "application/zip"
	EncodingGZIP  = "application/gzip"
	EncodingTAR   = "application/x-tar"
	EncodingBzip2 = "application/x-bzip2"
	Encoding7Z    = "application/x-7z-compressed"
	EncodingRAR   = "application/vnd.rar"
	EncodingJAR   = "application/java-archive"
	EncodingEPUB  = "application/epub+zip"

	// Office / OpenDocument.
	EncodingMSWord       = "application/msword"
	EncodingMSExcel      = "application/vnd.ms-excel"
	EncodingMSPowerPoint = "application/vnd.ms-powerpoint"
	EncodingDOCX         = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	EncodingXLSX         = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	EncodingPPTX         = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	EncodingODT          = "application/vnd.oasis.opendocument.text"
	EncodingODS          = "application/vnd.oasis.opendocument.spreadsheet"
	EncodingODP          = "application/vnd.oasis.opendocument.presentation"
)

// Text media types (text/*).
const (
	EncodingPlain      = "text/plain"
	EncodingHTML       = "text/html"
	EncodingCSS        = "text/css"
	EncodingJavaScript = "text/javascript"
	EncodingCSV        = "text/csv"
	EncodingMarkdown   = "text/markdown"
	EncodingCalendar   = "text/calendar"
)

// Image media types (image/*).
const (
	EncodingPNG  = "image/png"
	EncodingAPNG = "image/apng"
	EncodingJPEG = "image/jpeg"
	EncodingGIF  = "image/gif"
	EncodingWebP = "image/webp"
	EncodingAVIF = "image/avif"
	EncodingSVG  = "image/svg+xml"
	EncodingBMP  = "image/bmp"
	EncodingTIFF = "image/tiff"
	EncodingICO  = "image/vnd.microsoft.icon"
)

// Audio media types (audio/*).
const (
	EncodingMP3       = "audio/mpeg"
	EncodingAAC       = "audio/aac"
	EncodingWAV       = "audio/wav"
	EncodingOggAudio  = "audio/ogg"
	EncodingWebmAudio = "audio/webm"
	EncodingMIDI      = "audio/midi"
)

// Video media types (video/*).
const (
	EncodingMP4       = "video/mp4"
	EncodingMPEG      = "video/mpeg"
	EncodingWebmVideo = "video/webm"
	EncodingOggVideo  = "video/ogg"
	EncodingAVI       = "video/x-msvideo"
	EncodingMP2T      = "video/mp2t"
	Encoding3GP       = "video/3gpp"
)

// Font media types (font/*).
const (
	EncodingWOFF  = "font/woff"
	EncodingWOFF2 = "font/woff2"
	EncodingTTF   = "font/ttf"
	EncodingOTF   = "font/otf"
)
