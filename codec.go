// package: ranke / codec
// type:    io
// job:     canonical CBOR (de)serialization at two levels — the node/edge record encoding that ids are computed over, and the whole-claim storage codec (Claim.Encode / DecodeClaim)
// limits:  persists nothing (-> universe, adapter); content integrity lives in content.go
package ranke

import (
	"fmt"
	"strconv"
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
	// ContentSize is the byte-length of the content addressed by
	// ContentHash. Paired with ContentHash so a verifier can detect
	// truncation/extension without rehashing — and so storage layers
	// can know the size without loading the bytes. Emitted iff
	// ContentHash is present (see buildEncNode).
	ContentSize uint64 `cbor:"11,keyasint,omitempty"`
	// Height is the claim's generation number (§4.1): 0 for an initial
	// node, else 1 + max(reference heights). omitempty drops height 0, so
	// an initial node encodes no height field — height 0 and absent are the
	// same on decode, matching the semantics (only initial nodes are 0).
	Height uint64 `cbor:"12,keyasint,omitempty"`
}

// encEdge is the canonical record of an edge (§4.2), the shape the edge
// id is computed over. Content is addressed by ContentHash+ContentSize (never
// inline bytes) so an edge's id is the same whether its content is
// inline or external; the inline bytes, when any, live in the storage
// shape (encStorEdge), not here.
type encEdge struct {
	Reference         []byte            `cbor:"1,keyasint"`
	TypeClass         string            `cbor:"2,keyasint"`
	TypeSub           string            `cbor:"3,keyasint"`
	ContentHash       []byte            `cbor:"4,keyasint,omitempty"` // raw multihash bytes
	RelationDirection int8              `cbor:"5,keyasint,omitempty"`
	Fields            map[string]string `cbor:"6,keyasint,omitempty"`
	ContentSize       uint64            `cbor:"7,keyasint,omitempty"` // content byte length; emitted iff ContentHash present
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

// --- alias pass (§180): the canonical encoding stores well-known types
// and field names in their compact alias form, so the id is over the
// aliased bytes. Aliases are bare in the taxonomy; on the wire they carry a
// leading "." — the reserved prefix a literal can never have (charset), so
// alias and literal never collide. The alias table is fixed, so ids are
// stable under it.

// aliasToWire returns v's wire form: the "."-prefixed bare alias when v has
// one (toAlias changed it), else v literally.
func aliasToWire[T ~string](v T, toAlias func(T) T) string {
	if a := toAlias(v); a != v {
		return "." + string(a)
	}
	return string(v)
}

// aliasFromWire reverses aliasToWire: a "."-prefixed value is de-aliased,
// anything else is a literal.
func aliasFromWire[T ~string](w string, fromAlias func(T) T) T {
	if len(w) > 0 && w[0] == '.' {
		return fromAlias(T(w[1:]))
	}
	return T(w)
}

// aliasFieldKeys / unaliasFieldKeys map a field map's keys through the
// field-name alias (values are unchanged). nil in → nil out; the input map
// is not mutated.
func aliasFieldKeys(fields map[string]string) map[string]string {
	if fields == nil {
		return nil
	}
	out := make(map[string]string, len(fields))
	for k, v := range fields {
		out[aliasToWire(Field(k), fieldNameToAlias)] = v
	}
	return out
}

func unaliasFieldKeys(fields map[string]string) map[string]string {
	if fields == nil {
		return nil
	}
	out := make(map[string]string, len(fields))
	for k, v := range fields {
		out[string(aliasFromWire(k, fieldNameFromAlias))] = v
	}
	return out
}

// buildEncNode constructs the encNode payload for a *node — used both
// in id computation (encodeNode wraps this) and in the claim codec
// (Claim.Encode), independent of where the bytes are later stored.
func buildEncNode(n *node) (encNode, error) {
	en := encNode{
		TypeClass:     aliasToWire(n.typeClass, nodeClassToAlias),
		TypeSub:       aliasToWire(NodeSubtype(n.typeSub), nodeSubtypeToAlias),
		EncodingClass: aliasToWire(n.encodingClass, encodingClassToAlias),
		EncodingSub:   n.encodingSub, // encoding subtypes have no aliases yet
		CreatedAt:     n.createdAt.UTC().Format("2006-01-02T15:04:05.000000000Z"),
		Height:        n.height,
		Fields:        aliasFieldKeys(n.fields),
	}
	if n.contentHash != nil {
		en.ContentHash = idBytes(n.contentHash)
		en.ContentSize = n.contentSize
	}
	if len(n.edges) > 0 {
		en.Edges = make([][]byte, len(n.edges))
		for i, e := range n.edges {
			en.Edges[i] = idBytes(e)
		}
	}
	return en, nil
}

// buildEncEdge constructs the encEdge record for an *edge. Content is
// addressed by hash+size; inline bytes (if any) are added by the caller
// to the storage shape.
func buildEncEdge(e *edge) (encEdge, error) {
	ee := encEdge{
		Reference:         idBytes(e.reference),
		TypeClass:         aliasToWire(e.typeClass, edgeClassToAlias),
		TypeSub:           aliasToWire(EdgeSubtype(e.typeSub), edgeSubtypeToAlias),
		RelationDirection: int8(e.relationDirection),
		Fields:            aliasFieldKeys(e.fields),
	}
	if e.contentHash != nil {
		ee.ContentHash = idBytes(e.contentHash)
		ee.ContentSize = e.contentSize
	}
	return ee, nil
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
	return v.rawBytes()
}

// ─── Claim: the storage codec, built on the record shapes above ───────
//
// Claim.Encode and the package-level DecodeClaim are inverses; the same
// bytes represent a claim whether it lives in memory, is written to a
// file, sent over a wire, or stored in an object store. Persistence
// adapters use them so they never need to know a claim's internal
// representation; they move opaque bytes.

// encStorEdge is the storage shape of an edge: its canonical record
// (encEdge, which the edge id is computed over) plus the inline content
// bytes when the edge carries them inline. Keeping the bytes outside the
// record means inlining never changes the edge id.
type encStorEdge struct {
	Edge          encEdge `cbor:"1,keyasint"`
	InlineContent []byte  `cbor:"2,keyasint,omitempty"`
}

// encClaimFile is the canonical serialized shape of a claim: the node
// record plus edges, and — separately from the id-defining records —
// the inline content bytes for the node and any edges that carry them.
// Absent inline fields mean the content is external (referenced by hash)
// or the claim has no content at all.
type encClaimFile struct {
	Node        encNode       `cbor:"1,keyasint"`
	NodeContent []byte        `cbor:"2,keyasint,omitempty"` // inline node content; absent when external or none
	Edges       []encStorEdge `cbor:"3,keyasint,omitempty"`
}

// Encode serializes the claim to its canonical CBOR bytes — the same
// bytes its id is derived from, and the inverse of DecodeClaim.
func (c *claim) Encode() ([]byte, error) {
	en, err := buildEncNode(c.node)
	if err != nil {
		return nil, err
	}
	// c.node.content / e.content are non-nil only for inline content;
	// omitempty drops them for external and content-less claims.
	file := encClaimFile{Node: en, NodeContent: c.node.content}
	file.Edges = make([]encStorEdge, len(c.edges))
	for i, e := range c.edges {
		ee, err := buildEncEdge(e)
		if err != nil {
			return nil, err
		}
		file.Edges[i] = encStorEdge{Edge: ee, InlineContent: e.content}
	}
	data, err := encodingMode.Marshal(file)
	if err != nil {
		return nil, wrapDetail(errEncodeClaim, c.node.id.String(), err)
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
		return nil, wrap(errDecodeClaim, err)
	}
	n, err := decodeNode(ec.Node)
	if err != nil {
		return nil, wrapDetail(errDecodeClaim, "node", err)
	}
	n.id = id
	if len(ec.NodeContent) > 0 {
		n.content = ec.NodeContent // inline; contentHash+size set by decodeNode
	}
	edges := make([]*edge, len(ec.Edges))
	for i, se := range ec.Edges {
		e, err := decodeEdge(se.Edge)
		if err != nil {
			return nil, wrapDetail(errDecodeClaim, "edge "+strconv.Itoa(i), err)
		}
		if len(se.InlineContent) > 0 {
			e.content = se.InlineContent
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
		typeClass:     aliasFromWire(en.TypeClass, nodeClassFromAlias),
		typeSub:       string(aliasFromWire(en.TypeSub, nodeSubtypeFromAlias)),
		encodingClass: aliasFromWire(en.EncodingClass, encodingClassFromAlias),
		encodingSub:   en.EncodingSub,
		createdAt:     createdAt,
		height:        en.Height,
		fields:        unaliasFieldKeys(en.Fields),
	}
	if len(en.ContentHash) > 0 {
		ch, err := hashFromMultihashBytes(en.ContentHash)
		if err != nil {
			return nil, err
		}
		n.contentHash = ch
		n.contentSize = en.ContentSize
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
	e := &edge{
		reference:         ref,
		typeClass:         aliasFromWire(ee.TypeClass, edgeClassFromAlias),
		typeSub:           string(aliasFromWire(ee.TypeSub, edgeSubtypeFromAlias)),
		relationDirection: RelationDirection(ee.RelationDirection),
		fields:            unaliasFieldKeys(ee.Fields),
	}
	// Inline content bytes (if any) are attached by DecodeClaim from the
	// storage shape; here we restore only the hash+size reference.
	if len(ee.ContentHash) > 0 {
		ch, err := hashFromMultihashBytes(ee.ContentHash)
		if err != nil {
			return nil, err
		}
		e.contentHash = ch
		e.contentSize = ee.ContentSize
	}
	return e, nil
}
