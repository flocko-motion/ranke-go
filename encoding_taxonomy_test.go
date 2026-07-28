package ranke

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Foundation unit tests for the encoding (MIME media-type) wire aliases (§5.1)
// and the named media-type constants. Uses the shared checkAliasRoundTrip
// helper (node_taxonomy_test.go, same package) to pin the bijection /
// no-collision / pass-through properties for the closed class set and the
// well-known subtypes.

// TestEncodingConstants confirms each named media-type constant equals what its
// class constructor produces — so the shortcuts stay in lockstep with the
// constructors and can be used interchangeably.
func TestEncodingConstants(t *testing.T) {
	cases := []struct {
		name  string
		got   string
		equiv string
	}{
		{"JSON", EncodingJSON, EncodingApplication("json")},
		{"JSONLD", EncodingJSONLD, EncodingApplication("ld+json")},
		{"PDF", EncodingPDF, EncodingApplication("pdf")},
		{"ZIP", EncodingZIP, EncodingApplication("zip")},
		{"DOCX", EncodingDOCX, EncodingApplication("vnd.openxmlformats-officedocument.wordprocessingml.document")},
		{"Plain", EncodingPlain, EncodingText("plain")},
		{"HTML", EncodingHTML, EncodingText("html")},
		{"CSS", EncodingCSS, EncodingText("css")},
		{"Markdown", EncodingMarkdown, EncodingText("markdown")},
		{"PNG", EncodingPNG, EncodingImage("png")},
		{"JPEG", EncodingJPEG, EncodingImage("jpeg")},
		{"SVG", EncodingSVG, EncodingImage("svg+xml")},
		{"MP3", EncodingMP3, EncodingAudio("mpeg")},
		{"OggAudio", EncodingOggAudio, EncodingAudio("ogg")},
		{"MP4", EncodingMP4, EncodingVideo("mp4")},
		{"WebmVideo", EncodingWebmVideo, EncodingVideo("webm")},
		{"WOFF2", EncodingWOFF2, EncodingFont("woff2")},
	}
	for _, c := range cases {
		require.Equalf(t, c.equiv, c.got, "%s constant matches its constructor form", c.name)
		require.Containsf(t, c.got, "/", "%s is a well-formed media type", c.name)
		require.Falsef(t, strings.HasPrefix(c.got, "/"), "%s has a top-level type", c.name)
	}
}

func TestEncodingClassAliases(t *testing.T) {
	checkAliasRoundTrip(t, map[EncodingClass]EncodingClass{
		encApplication: encApplicationAlias,
		encAudio:       encAudioAlias,
		encExample:     encExampleAlias,
		encFont:        encFontAlias,
		encImage:       encImageAlias,
		encMessage:     encMessageAlias,
		encModel:       encModelAlias,
		encMultipart:   encMultipartAlias,
		encText:        encTextAlias,
		encVideo:       encVideoAlias,
	}, encodingClassToAlias, encodingClassFromAlias, EncodingClass("chemical"))
}

// TestEncodingSubtypeAliases pins the whole well-known-subtype table: bijective
// round-trip, no colliding alias, and an open subtype (rfc822) passing through
// untouched so a literal is never mistaken for an alias.
func TestEncodingSubtypeAliases(t *testing.T) {
	checkAliasRoundTrip(t, encodingSubAliases,
		encodingSubToAlias, encodingSubFromAlias,
		EncodingSubtype("rfc822")) // open vocabulary — not aliased
}

// TestIsTextEncoding covers the text-vs-binary media-type split used to decide
// whether content is legible enough to inline (e.g. in the neo4j graph).
func TestIsTextEncoding(t *testing.T) {
	for _, enc := range []string{
		EncodingPlain, EncodingHTML, EncodingCSS, EncodingJavaScript, EncodingMarkdown,
		EncodingJSON, EncodingJSONLD, EncodingXML, EncodingXHTML, EncodingWebManifest,
		EncodingMessage("rfc822"), EncodingApplication("vnd.custom+xml"),
	} {
		require.Truef(t, IsTextEncoding(enc), "%q is a text media type", enc)
	}
	for _, enc := range []string{
		"", EncodingPNG, EncodingJPEG, EncodingMP4, EncodingMP3, EncodingPDF,
		EncodingZIP, EncodingOctetStream, EncodingWOFF2,
	} {
		require.Falsef(t, IsTextEncoding(enc), "%q is not a text media type", enc)
	}
}

// TestEncodingSubtypeAliasesAreSingleCharacter: subtype aliases are one
// character (§5.1), like the class aliases — a "."-prefixed single char is the
// most compact wire form, and it caps the table at the 62 [a-zA-Z0-9] codes.
func TestEncodingSubtypeAliasesAreSingleCharacter(t *testing.T) {
	for full, alias := range encodingSubAliases {
		require.Lenf(t, string(alias), 1, "alias for %q must be one character", full)
	}
}

// TestEveryNamedTypeHasSubtypeAlias enforces the rule that a predefined media
// type earns its keep only by encoding compactly: every named constant's
// subtype must carry an alias, else defining the constant is pointless.
func TestEveryNamedTypeHasSubtypeAlias(t *testing.T) {
	all := []string{
		EncodingJSON, EncodingJSONLD, EncodingXML, EncodingXHTML, EncodingPDF,
		EncodingOctetStream, EncodingWebManifest, EncodingRTF,
		EncodingZIP, EncodingGZIP, EncodingTAR, EncodingBzip2, Encoding7Z, EncodingRAR, EncodingJAR, EncodingEPUB,
		EncodingMSWord, EncodingMSExcel, EncodingMSPowerPoint, EncodingDOCX, EncodingXLSX, EncodingPPTX, EncodingODT, EncodingODS, EncodingODP,
		EncodingPlain, EncodingHTML, EncodingCSS, EncodingJavaScript, EncodingCSV, EncodingMarkdown, EncodingCalendar,
		EncodingPNG, EncodingAPNG, EncodingJPEG, EncodingGIF, EncodingWebP, EncodingAVIF, EncodingSVG, EncodingBMP, EncodingTIFF, EncodingICO,
		EncodingMP3, EncodingAAC, EncodingWAV, EncodingOggAudio, EncodingWebmAudio, EncodingMIDI,
		EncodingMP4, EncodingMPEG, EncodingWebmVideo, EncodingOggVideo, EncodingAVI, EncodingMP2T, Encoding3GP,
		EncodingWOFF, EncodingWOFF2, EncodingTTF, EncodingOTF,
	}
	for _, mt := range all {
		_, sub, ok := strings.Cut(mt, "/")
		require.Truef(t, ok, "media type %q has a subtype", mt)
		_, aliased := encodingSubAliases[EncodingSubtype(sub)]
		require.Truef(t, aliased, "subtype %q (from %q) must carry an alias", sub, mt)
	}
}
