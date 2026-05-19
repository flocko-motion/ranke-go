package ranke

import (
	"fmt"
	"time"

	"github.com/fxamacker/cbor/v2"
)

// Canonical serialization: CBOR Deterministic Encoding (RFC 8949
// §4.2). Field order is fixed by numeric keys (`cbor:"N,keyasint"`)
// to avoid map-key sort issues; omitempty drops zero-valued optional
// fields deterministically.
//
// Two encoded record shapes:
//
//	encNode — the structural component of a claim (§4.1)
//	encEdge — a directed reference (§4.2)
//
// Both shapes participate in id computation: id = H(canonicalCBOR(record)).

// encodingMode is the singleton encoder configured for CBOR
// Deterministic. Encoders are safe for concurrent use.
var encodingMode cbor.EncMode

func init() {
	mode, err := cbor.CoreDetEncOptions().EncMode()
	if err != nil {
		panic(fmt.Sprintf("ranke: build CBOR Deterministic encoder: %v", err))
	}
	encodingMode = mode
}

// encNode is the wire shape of a node (§4.1). Field tags are stable
// numeric keys.
type encNode struct {
	TypeClass     string   `cbor:"1,keyasint"`
	TypeSub       string   `cbor:"2,keyasint"`
	EncodingClass string   `cbor:"3,keyasint,omitempty"`
	EncodingSub   string   `cbor:"4,keyasint,omitempty"`
	ContentHash   []byte   `cbor:"5,keyasint,omitempty"` // raw multihash bytes
	CreatedAt     string   `cbor:"6,keyasint"`           // RFC3339 nano UTC
	Edges         [][]byte `cbor:"7,keyasint,omitempty"` // sorted edge id bytes
	// Fields appear under tag 8 as a map sorted by key (CBOR
	// Deterministic ensures sort order).
	Fields map[string]string `cbor:"8,keyasint,omitempty"`
	// Pubkey is the contributor's multikey-encoded public key (§5.7).
	// Empty (omitted) on non-contributor claims and unsigned
	// contributors. Participates in id computation like any field.
	Pubkey []byte `cbor:"9,keyasint,omitempty"`
	// Title is an optional short text label. Omitted from the
	// encoding (and thus from the id) when empty, so adding the
	// field doesn't change existing claims' bytes.
	Title string `cbor:"10,keyasint,omitempty"`
	// Size is the byte-length of the content addressed by
	// ContentHash. Paired with ContentHash so a verifier can detect
	// truncation/extension without rehashing — and so storage layers
	// can know the size without loading the bytes. Emitted iff
	// ContentHash is present (see buildEncNode).
	Size uint64 `cbor:"11,keyasint,omitempty"`
}

// encEdge is the wire shape of an edge (§4.2 simplified schema):
// type + content (inline) + reference + additional fields.
type encEdge struct {
	Reference         []byte            `cbor:"1,keyasint"`
	TypeClass         string            `cbor:"2,keyasint"`
	TypeSub           string            `cbor:"3,keyasint"`
	Content           []byte            `cbor:"4,keyasint,omitempty"`
	RelationDirection int8              `cbor:"5,keyasint,omitempty"`
	Fields            map[string]string `cbor:"6,keyasint,omitempty"`
}

// encodeNode serializes a node and returns its canonical bytes.
func encodeNode(n *node) ([]byte, error) {
	en, err := buildEncNode(n)
	if err != nil {
		return nil, err
	}
	return encodingMode.Marshal(en)
}

// encodeEdge serializes an edge and returns its canonical bytes.
func encodeEdge(e *edge) ([]byte, error) {
	ee, err := buildEncEdge(e)
	if err != nil {
		return nil, err
	}
	return encodingMode.Marshal(ee)
}

// buildEncNode constructs the encNode payload for a *node — used
// both in id computation (encodeNode wraps this) and when persisting
// claim files to disk.
func buildEncNode(n *node) (encNode, error) {
	en := encNode{
		TypeClass:     string(n.typeClass),
		TypeSub:       n.typeSub,
		EncodingClass: string(n.encodingClass),
		EncodingSub:   n.encodingSub,
		CreatedAt:     n.createdAt.UTC().Format("2006-01-02T15:04:05.000000000Z"),
		Fields:        n.fields,
		Pubkey:        n.pubkey,
		Title:         n.title,
	}
	if n.contentHash != nil {
		en.ContentHash = idBytes(n.contentHash)
		en.Size = n.size
	}
	if len(n.edges) > 0 {
		en.Edges = make([][]byte, len(n.edges))
		for i, e := range n.edges {
			en.Edges[i] = idBytes(e)
		}
	}
	return en, nil
}

// buildEncEdge constructs the encEdge payload for an *edge.
func buildEncEdge(e *edge) (encEdge, error) {
	return encEdge{
		Reference:         idBytes(e.reference),
		TypeClass:         string(e.typeClass),
		TypeSub:           e.typeSub,
		Content:           e.content,
		RelationDirection: int8(e.relationDirection),
		Fields:            e.fields,
	}, nil
}

// parseRFC3339Nano parses the timestamp format we emit for CreatedAt.
func parseRFC3339Nano(s string) (time.Time, error) {
	return time.Parse("2006-01-02T15:04:05.000000000Z", s)
}

// idBytes extracts the raw multihash bytes from an Id. Used only by
// canonical encoding paths within this package.
func idBytes(id Id) []byte {
	if id == nil {
		return nil
	}
	if h, ok := id.(*hash); ok {
		return h.raw
	}
	// Fallback: round-trip through ParseId on the string form. Only
	// hit if a non-package Id implementation flows in, which we
	// don't expect.
	parsed, err := ParseId(id.String())
	if err != nil {
		return nil
	}
	return parsed.(*hash).raw
}
