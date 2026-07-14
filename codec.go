// package: ranke / codec
// type:    io
// job:     canonical CBOR (de)serialization at two levels — the node/edge record encoding that ids are computed over, and the whole-claim storage codec (Claim.Encode / DecodeClaim)
// limits:  persists nothing (-> universe, adapter); content integrity lives in content.go
package ranke

import (
	"fmt"
	"time"

	"github.com/fxamacker/cbor/v2"
)

// ─── Records: the canonical encoding an id is computed over ───────────
//
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

// buildEncNode constructs the encNode payload for a *node — used both
// in id computation (encodeNode wraps this) and in the claim codec
// (Claim.Encode), independent of where the bytes are later stored.
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

// idBytes extracts the raw self-describing payload bytes from an Id.
// Used only by canonical encoding paths within this package.
func idBytes(v Id) []byte {
	if v == nil {
		return nil
	}
	if h, ok := v.(*id); ok {
		return h.raw
	}
	// Fallback: round-trip through ParseId on the string form. Only
	// hit if a non-package Id implementation flows in, which we
	// don't expect.
	parsed, err := ParseId(v.String())
	if err != nil {
		return nil
	}
	return parsed.(*id).raw
}

// ─── Claim: the storage codec, built on the record shapes above ───────
//
// Claim.Encode and the package-level DecodeClaim are inverses; the same
// bytes represent a claim whether it lives in memory, is written to a
// file, sent over a wire, or stored in an object store. Persistence
// adapters use them so they never need to know a claim's internal
// representation; they move opaque bytes.

// encClaimFile is the canonical serialized shape of a claim: its node
// plus edges, CBOR Deterministic per the record shapes above.
type encClaimFile struct {
	Node  encNode   `cbor:"1,keyasint"`
	Edges []encEdge `cbor:"2,keyasint,omitempty"`
}

// Encode serializes the claim to its canonical CBOR bytes — the same
// bytes its id is derived from, and the inverse of DecodeClaim.
func (c *claim) Encode() ([]byte, error) {
	en, err := buildEncNode(c.node)
	if err != nil {
		return nil, err
	}
	ee := make([]encEdge, len(c.edges))
	for i, e := range c.edges {
		ee[i], err = buildEncEdge(e)
		if err != nil {
			return nil, err
		}
	}
	data, err := encodingMode.Marshal(encClaimFile{Node: en, Edges: ee})
	if err != nil {
		return nil, fmt.Errorf("ranke: encode claim %s: %w", c.node.id.String(), err)
	}
	return data, nil
}

// DecodeClaim decodes the canonical CBOR serialization of a claim (the
// inverse of Claim.Encode) into a Claim with its id set. Exposed for
// tooling that inspects claims directly (e.g. the ranke CLI) and for
// persistence adapters — it returns an error when the bytes aren't a
// valid claim, which callers use to dispatch claim vs content.
func DecodeClaim(id Id, b []byte) (Claim, error) {
	var ec encClaimFile
	if err := cbor.Unmarshal(b, &ec); err != nil {
		return nil, fmt.Errorf("DecodeClaim: %w", err)
	}
	n, err := decodeNode(ec.Node)
	if err != nil {
		return nil, fmt.Errorf("DecodeClaim: node: %w", err)
	}
	n.id = id
	edges := make([]*edge, len(ec.Edges))
	for i, ee := range ec.Edges {
		e, err := decodeEdge(ee)
		if err != nil {
			return nil, fmt.Errorf("DecodeClaim: edge %d: %w", i, err)
		}
		if i < len(n.edges) {
			e.id = n.edges[i]
		}
		edges[i] = e
	}
	return &claim{node: n, edges: edges}, nil
}

func decodeNode(en encNode) (*node, error) {
	createdAt, err := parseRFC3339Nano(en.CreatedAt)
	if err != nil {
		return nil, err
	}
	n := &node{
		typeClass:     NodeClass(en.TypeClass),
		typeSub:       en.TypeSub,
		encodingClass: EncodingClass(en.EncodingClass),
		encodingSub:   en.EncodingSub,
		title:         en.Title,
		createdAt:     createdAt,
		fields:        en.Fields,
		pubkey:        en.Pubkey,
	}
	if len(en.ContentHash) > 0 {
		ch, err := hashFromMultihashBytes(en.ContentHash)
		if err != nil {
			return nil, err
		}
		n.contentHash = ch
		n.size = en.Size
	}
	if len(en.Edges) > 0 {
		n.edges = make([]Id, len(en.Edges))
		for i, raw := range en.Edges {
			h, err := idFromBytes(raw)
			if err != nil {
				return nil, err
			}
			n.edges[i] = h
		}
	}
	return n, nil
}

func decodeEdge(ee encEdge) (*edge, error) {
	ref, err := idFromBytes(ee.Reference)
	if err != nil {
		return nil, err
	}
	return &edge{
		reference:         ref,
		typeClass:         EdgeClass(ee.TypeClass),
		typeSub:           ee.TypeSub,
		content:           ee.Content,
		relationDirection: RelationDirection(ee.RelationDirection),
		fields:            ee.Fields,
	}, nil
}
