// package: ranke / codec
// type:    io
// job:     canonical CBOR (de)serialization at two levels — the node record encoding that ids are computed over (edges inlined), and the whole-claim storage codec (Claim.Encode / DecodeClaim)
// limits:  persists nothing (-> universe, adapter); content integrity lives in content.go
package ranke

import (
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
//	encNode — the structural component of a claim (§4.1), with its edges
//	          inlined so S(v) includes them
//	encEdge — a directed reference (§4.2)
//
// Identity (§Primitives): id(v) = Sign(H(S(v))) for a node, id(e) = H(S(e))
// for an edge. Because a node's edges are inlined in S(v), the node's id
// commits to the edges' contents directly; an edge id is just the hash of
// its own inlined record.

// encodingMode is the singleton encoder configured for CBOR
// Deterministic. Encoders are safe for concurrent use.
var encodingMode cbor.EncMode

func init() {
	mode, err := cbor.CoreDetEncOptions().EncMode()
	if err != nil {
		panic("ranke: build CBOR Deterministic encoder: " + err.Error())
	}
	encodingMode = mode
}

// encNode is the wire shape of a node (§4.1). Field tags are stable
// numeric keys.
type encNode struct {
	TypeClass     string `cbor:"1,keyasint"`
	TypeSub       string `cbor:"2,keyasint"`
	EncodingClass string `cbor:"3,keyasint,omitempty"`
	EncodingSub   string `cbor:"4,keyasint,omitempty"`
	ContentHash   []byte `cbor:"5,keyasint,omitempty"` // external content: its address H(c); mutually exclusive with Content
	CreatedAt     string `cbor:"6,keyasint"`           // RFC3339 nano UTC
	// Edge records inlined in canonical order — each element is one edge's
	// S(e), so S(v) commits to the edges directly. Content bytes stay out (in
	// EdgeContents), keeping edge ids inline/external-invariant.
	Edges []cbor.RawMessage `cbor:"7,keyasint,omitempty"`
	// Fields appear under tag 8 as a map sorted by key (CBOR
	// Deterministic ensures sort order).
	Fields map[string]string `cbor:"8,keyasint,omitempty"`
	// Content holds inline content bytes directly (§Content)
	// Mutually exclusive with ContentHash
	Content []byte `cbor:"9,keyasint,omitempty"`
	// ContentSize is the byte-length of the content, mandatory whenever the
	// node carries content (inline or external — §Content)
	ContentSize uint64 `cbor:"11,keyasint,omitempty"`
	// Height is the longest possible path, defined in paper 1 (§4.1)
	Height uint64 `cbor:"12,keyasint,omitempty"`
}

// encEdge is the canonical record of an edge (§4.2)
type encEdge struct {
	Reference         []byte            `cbor:"1,keyasint"`
	TypeClass         string            `cbor:"2,keyasint"`
	TypeSub           string            `cbor:"3,keyasint"`
	ContentHash       []byte            `cbor:"4,keyasint,omitempty"` // external content address; mutually exclusive with Content
	RelationDirection int8              `cbor:"5,keyasint,omitempty"`
	Fields            map[string]string `cbor:"6,keyasint,omitempty"`
	ContentSize       uint64            `cbor:"7,keyasint,omitempty"` // content byte length; emitted iff content present
	EncodingClass     string            `cbor:"8,keyasint,omitempty"`
	EncodingSub       string            `cbor:"9,keyasint,omitempty"`
	Content           []byte            `cbor:"10,keyasint,omitempty"`
}

// encodeNode serializes a node together with its (inlined) edges and returns
// the canonical S(v) bytes the node id is computed over.
func encodeNode(n *node, edges []*edge) ([]byte, error) {
	en, err := buildEncNode(n, edges)
	if err != nil {
		return nil, err
	}
	return encodingMode.Marshal(en)
}

// encodeEdge serializes an edge and returns its canonical S(e) bytes.
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

// buildEncNode constructs the encNode payload for a *node with its edges
// inlined (in the given canonical order). Used both in id computation
// (encodeNode wraps this) and in the claim codec (Claim.Encode). Each edge is
// serialized to its S(e) and embedded as a raw item, so S(v) — and the node
// id — commit to the edge records directly, including any inline content they
// carry (§Content).
func buildEncNode(n *node, edges []*edge) (encNode, error) {
	en := encNode{
		TypeClass:     aliasToWire(n.typeClass, nodeClassToAlias),
		TypeSub:       aliasToWire(NodeSubtype(n.typeSub), nodeSubtypeToAlias),
		EncodingClass: aliasToWire(n.encodingClass, encodingClassToAlias),
		EncodingSub:   aliasToWire(EncodingSubtype(n.encodingSub), encodingSubToAlias),
		CreatedAt:     n.createdAt.UTC().Format("2006-01-02T15:04:05.000000000Z"),
		Height:        n.height,
		Fields:        aliasFieldKeys(n.fields),
	}
	switch {
	case n.content != nil: // inline: the id commits to the bytes (§Content)
		en.Content = n.content
		en.ContentSize = n.contentSize
	case n.contentHash != nil: // external: the id commits to the address
		en.ContentHash = idBytes(n.contentHash)
		en.ContentSize = n.contentSize
	}
	if len(edges) > 0 {
		en.Edges = make([]cbor.RawMessage, len(edges))
		for i, e := range edges {
			raw, err := encodeEdge(e)
			if err != nil {
				return encNode{}, err
			}
			en.Edges[i] = raw
		}
	}
	return en, nil
}

// buildEncEdge constructs the encEdge record for an *edge
func buildEncEdge(e *edge) (encEdge, error) {
	ee := encEdge{
		Reference:         idBytes(e.reference),
		TypeClass:         aliasToWire(e.typeClass, edgeClassToAlias),
		TypeSub:           aliasToWire(EdgeSubtype(e.typeSub), edgeSubtypeToAlias),
		EncodingClass:     aliasToWire(e.encodingClass, encodingClassToAlias),
		EncodingSub:       aliasToWire(EncodingSubtype(e.encodingSub), encodingSubToAlias),
		RelationDirection: int8(e.relationDirection),
		Fields:            aliasFieldKeys(e.fields),
	}
	switch {
	case e.content != nil:
		ee.Content = e.content
		ee.ContentSize = e.contentSize
	case e.contentHash != nil:
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

// encClaimFile is the canonical serialized shape of a claim: just the node
// record, which inlines the edge records
type encClaimFile struct {
	Node encNode `cbor:"1,keyasint"`
}

// Encode serializes the claim to its canonical CBOR bytes — the same
// bytes its id is derived from, and the inverse of DecodeClaim.
func (c *claim) Encode() ([]byte, error) {
	en, err := buildEncNode(c.node, c.edges)
	if err != nil {
		return nil, err
	}
	// Inline content (node and edges) is already inside the records (§Content),
	// so the storage shape is just the node record.
	data, err := encodingMode.Marshal(encClaimFile{Node: en})
	if err != nil {
		return nil, WrapDetail(errEncodeClaim, c.node.id.String(), err)
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
		return nil, Wrap(errDecodeClaim, err)
	}
	n, err := decodeNode(ec.Node)
	if err != nil {
		return nil, WrapDetail(errDecodeClaim, "node", err)
	}
	n.id = id
	// Node inline content (if any) is set by decodeNode from the node record.
	// Edges are inlined in the node record. Each edge id is H(S(e)) over the
	// stored edge bytes — computed here from the raw slice, never re-encoded,
	// so it is stable as the alias taxonomy grows. Inline edge content, if
	// any, is positional in EdgeContents.
	edges := make([]*edge, len(ec.Node.Edges))
	n.edges = make([]Id, len(ec.Node.Edges))
	for i, raw := range ec.Node.Edges {
		eid, err := hashContent(raw)
		if err != nil {
			return nil, WrapDetail(errDecodeClaim, "edge "+strconv.Itoa(i)+" id", err)
		}
		var ee encEdge
		if err := cbor.Unmarshal(raw, &ee); err != nil {
			return nil, WrapDetail(errDecodeClaim, "edge "+strconv.Itoa(i), err)
		}
		e, err := decodeEdge(ee)
		if err != nil {
			return nil, WrapDetail(errDecodeClaim, "edge "+strconv.Itoa(i), err)
		}
		e.id = eid
		edges[i] = e
		n.edges[i] = eid
	}
	return &claim{node: n, edges: edges}, nil
}

// nodePreimage extracts S(node) — field 1, the node record with its inlined
// edges — from a claim's stored CBOR as raw bytes, so verification hashes the
// exact bytes the id was signed over. Re-deriving via buildEncNode would drift
// as the alias taxonomy grows.
func nodePreimage(raw []byte) ([]byte, error) {
	var rf struct {
		Node cbor.RawMessage `cbor:"1,keyasint"`
	}
	if err := cbor.Unmarshal(raw, &rf); err != nil {
		return nil, Wrap(errDecodeClaim, err)
	}
	if len(rf.Node) == 0 {
		return nil, Wrap(errDecodeClaim, errNodePreimage)
	}
	return rf.Node, nil
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
		encodingSub:   string(aliasFromWire(en.EncodingSub, encodingSubFromAlias)),
		createdAt:     createdAt,
		height:        en.Height,
		fields:        unaliasFieldKeys(en.Fields),
	}
	switch {
	case len(en.Content) > 0: // inline (§Content)
		n.content = en.Content
		n.contentSize = en.ContentSize
	case len(en.ContentHash) > 0: // external
		ch, err := hashFromMultihashBytes(en.ContentHash)
		if err != nil {
			return nil, err
		}
		n.contentHash = ch
		n.contentSize = en.ContentSize
	}
	// n.edges (the edge id list) is populated by DecodeClaim, which decodes
	// the inlined edge records and computes each id.
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
		encodingClass:     aliasFromWire(ee.EncodingClass, encodingClassFromAlias),
		encodingSub:       string(aliasFromWire(ee.EncodingSub, encodingSubFromAlias)),
		relationDirection: RelationDirection(ee.RelationDirection),
		fields:            unaliasFieldKeys(ee.Fields),
	}
	switch {
	case len(ee.Content) > 0: // inline (§Content)
		e.content = ee.Content
		e.contentSize = ee.ContentSize
	case len(ee.ContentHash) > 0: // external
		ch, err := hashFromMultihashBytes(ee.ContentHash)
		if err != nil {
			return nil, err
		}
		e.contentHash = ch
		e.contentSize = ee.ContentSize
	}
	return e, nil
}
