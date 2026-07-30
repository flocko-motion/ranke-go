// package: ranke / codec
// type:    io
// job:     canonical CBOR (de)serialization at two levels — the node record encoding that ids are computed over (edges inlined), and the whole-claim storage codec (Claim.Encode / DecodeClaim)
// limits:  persists nothing (-> universe, adapter); content integrity lives in content.go
package ranke

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/fxamacker/cbor/v2"
)

// ─── Records: the canonical encoding an id is computed over ───────────

// CBOR Deterministic Encoding (RFC 8949 §4.2), field order fixed by numeric
// keys. id(v) = Sign(H(S(v))), id(e) = H(S(e)) (§Primitives).

// encodingMode is the shared CBOR Deterministic encoder, safe for concurrent use.
var encodingMode cbor.EncMode

func init() {
	mode, err := cbor.CoreDetEncOptions().EncMode()
	if err != nil {
		panic("ranke: build CBOR Deterministic encoder: " + err.Error())
	}
	encodingMode = mode
}

// encNode is the wire shape of a node (§4.1); tags are stable numeric keys.
type encNode struct {
	TypeClass     string `cbor:"1,keyasint"`
	TypeSub       string `cbor:"2,keyasint"`
	EncodingClass string `cbor:"3,keyasint,omitempty"`
	EncodingSub   string `cbor:"4,keyasint,omitempty"`
	ContentHash   []byte `cbor:"5,keyasint,omitempty"` // external content: its address H(c); mutually exclusive with Content
	CreatedAt     string `cbor:"6,keyasint"`           // RFC3339 nano UTC
	// Edge records inlined in canonical order, each element one edge's S(e).
	Edges []cbor.RawMessage `cbor:"7,keyasint,omitempty"`
	// Fields is a map under tag 8, key-sorted by CBOR Deterministic.
	Fields map[string]string `cbor:"8,keyasint,omitempty"`
	// Content holds inline content bytes; exclusive with ContentHash (§Content)
	Content []byte `cbor:"9,keyasint,omitempty"`
	// ContentSize is mandatory whenever the node carries content (§Content)
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

// encodeNode returns the canonical S(v) bytes a node id is computed over.
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

// --- alias pass (§180): the id is over the aliased bytes. On the wire an alias
// carries a leading "." — a prefix no literal can have, so the two never collide.

// aliasToWire returns v's wire form: "."+alias when toAlias changes v, else v.
func aliasToWire[T ~string](v T, toAlias func(T) T) string {
	if a := toAlias(v); a != v {
		return "." + string(a)
	}
	return string(v)
}

// aliasFromWire reverses aliasToWire: a "."-prefixed value is de-aliased.
func aliasFromWire[T ~string](w string, fromAlias func(T) T) T {
	if len(w) > 0 && w[0] == '.' {
		return fromAlias(T(w[1:]))
	}
	return T(w)
}

// aliasFieldKeys / unaliasFieldKeys map a field map's keys through the
// field-name alias into a fresh map; nil in → nil out.
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

// buildEncNode builds the encNode for n with each edge serialized to its S(e)
// and embedded raw, so S(v) commits to the edge records and their content
// (§Content).
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

// iso8601Nano is the created_at wire format: fixed-width nanoseconds in UTC, so
// the canonical serialization is byte-stable.
const iso8601Nano = "2006-01-02T15:04:05.000000000Z"

// parseRFC3339Nano parses the timestamp format we emit for CreatedAt.
func parseRFC3339Nano(s string) (time.Time, error) {
	return time.Parse(iso8601Nano, s)
}

// idBytes extracts the raw self-describing payload bytes from an Id.
func idBytes(v Id) []byte {
	if v == nil {
		return nil
	}
	return v.rawBytes()
}

// ─── Claim: the storage codec, built on the record shapes above ───────

// Claim.Encode and DecodeClaim are inverses, so persistence adapters move
// opaque bytes and stay ignorant of a claim's internal representation.

// encClaimFile is the canonical serialized shape of a claim: the node record.
type encClaimFile struct {
	Node encNode `cbor:"1,keyasint"`
}

// EncodeCBOR serializes the claim to its canonical CBOR record — the delta a diff
// claim stores, so the bytes stay the ones its id derives from and persistence can
// round-trip them. A decoded claim serves the record the decode kept.
func (c *claim) EncodeCBOR() ([]byte, error) {
	if c.raw != nil {
		return c.raw, nil
	}
	en, err := buildEncNode(c.node, c.edges)
	if err != nil {
		return nil, err
	}
	// Inline content lives inside the records (§Content).
	data, err := encodingMode.Marshal(encClaimFile{Node: en})
	if err != nil {
		return nil, WrapDetail(errEncodeClaim, c.node.id.String(), err)
	}
	return data, nil
}

// EncodeJSON renders the claim's ADT fields, named as the taxonomy names them, so
// JSON and CBOR carry the same information. Content is base64, as JSON renders bytes.
func (c *claim) EncodeJSON() ([]byte, error) {
	n := c.node
	m := map[string]any{
		"id":         c.ID().String(),
		"type":       n.Type(),
		"created_at": n.CreatedAt().UTC().Format(iso8601Nano),
		"height":     n.Height(),
	}
	if enc := n.Encoding(); enc != "" {
		m["encoding"] = enc
	}
	if n.ContentKind() != ContentNone {
		m[FieldContentSize] = n.GetContentSize()
	}
	if h := n.GetContentHash(); h != nil {
		m[FieldContentHash] = h.String()
	}
	if b, err := n.GetInlineContent(); err == nil && len(b) > 0 {
		m[FieldContent] = b
	}
	for _, k := range n.Fields() {
		if v, err := n.GetField(k); err == nil {
			m[k] = v
		}
	}
	if edges := c.Edges(); len(edges) > 0 {
		list := make([]map[string]any, 0, len(edges))
		for _, e := range edges {
			list = append(list, map[string]any{"type": e.Type(), "reference": e.Reference().String()})
		}
		m["edges"] = list
	}
	return json.Marshal(m)
}

// DecodeClaim decodes a claim's canonical CBOR into a Claim with its id set.
// An error tells callers the bytes are content rather than a claim.
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
	// Each edge id is H(S(e)) over the stored raw bytes, never re-encoded, so it
	// stays stable as the alias taxonomy grows.
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
	// Keep the stored record: it is the canonical CBOR, so a read asking for that
	// encoding is served from what the decode already held.
	return &claim{node: n, edges: edges, raw: b}, nil
}

// nodePreimage extracts S(node) — field 1 — from a claim's stored CBOR as raw
// bytes, so verification hashes the exact bytes the id was signed over.
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
	// n.edges is populated by DecodeClaim from the inlined edge records.
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
