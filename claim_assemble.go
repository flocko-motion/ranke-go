// package: ranke / claim
// type:    logic
// job:     AssembleClaim — rebuild a Claim from its parsed components + id, without CBOR and without signing (the field-oriented sibling of DecodeClaim, for non-CBOR cache backends)
// limits:  reconstructs a cache view, not a self-verifiable claim — it has no canonical bytes, so it cannot be structurally verified; a cache is checked by comparison to the authoritative layer (-> verify.go)
package ranke

import (
	"bytes"
	"sort"
	"strconv"
	"time"
)

// ClaimParts is the parsed structure of a stored claim — everything needed to
// rebuild it without the canonical CBOR. A graph-native cache (e.g. neo4j)
// stores these as node/edge properties and calls AssembleClaim to reconstruct
// the Claim. For EXTERNAL content the id commits only to content_hash +
// content_size (never the bytes), so a cache may omit the blob and still
// reconstruct id-faithfully — the durable layer serves it. For INLINE content
// the id commits to the bytes themselves (§Content), so InlineContent must be
// present to reconstruct the claim faithfully.
type ClaimParts struct {
	ID            Id                // the node/claim id (a signature — taken as given, not recomputed)
	Type          string            // "class/sub"
	Encoding      string            // "class/sub", or "" when there is no content
	CreatedAt     time.Time         // must retain nanosecond precision to re-encode identically
	Height        uint64            // §4.1 generation number (0 for an initial node); part of the id-preimage
	ContentHash   Id                // nil when the claim carries no content
	ContentSize   uint64            //
	InlineContent []byte            // present for inline content; omitted for external
	Fields        map[string]string //
	Edges         []EdgeParts       //
	Tags          map[string]string // mutable runtime tags (branch membership, revision) — not part of the id
}

// EdgeParts is the parsed structure of one edge. ID (the derived edge id) is
// required: a field-oriented cache must store it, since recomputing it means
// re-serializing S(e) from the claim CBOR the cache does not hold.
type EdgeParts struct {
	ID                Id // required — the cached edge id
	Reference         Id
	Type              string // "class/sub"
	Encoding          string // "class/sub", or "" when the edge has no content
	RelationDirection RelationDirection
	ContentHash       Id
	ContentSize       uint64
	InlineContent     []byte
	Fields            map[string]string
}

// AssembleClaim rebuilds a Claim from parsed parts and known ids, without CBOR
// and without signing. The node id and each edge id are taken as given (the
// node id is a signature; edge ids are cached by the field-oriented store — a
// requirement, since recomputing them would need the claim CBOR the store
// lacks), and the edges are ordered canonically — so, if the parts are
// faithful, the result re-encodes to identical bytes and verifies against the
// id. It performs no verification itself: a cache built from AssembleClaim is
// checked by the closure verifier over the authoritative Universe.
func AssembleClaim(parts ClaimParts) (Claim, error) {
	if parts.ID == nil {
		return nil, errNilID
	}
	nClass, nSub, err := splitType(parts.Type)
	if err != nil {
		return nil, wrapDetail(errAssemble, "type", err)
	}
	n := &node{
		typeClass:   NodeClass(nClass),
		typeSub:     nSub,
		height:      parts.Height,
		contentHash: parts.ContentHash,
		contentSize: parts.ContentSize,
		content:     parts.InlineContent,
		createdAt:   parts.CreatedAt.UTC(),
		fields:      cloneFields(parts.Fields),
		id:          parts.ID,
	}
	if parts.Encoding != "" {
		eClass, eSub, err := splitType(parts.Encoding)
		if err != nil {
			return nil, wrapDetail(errAssemble, "encoding", err)
		}
		n.encodingClass = EncodingClass(eClass)
		n.encodingSub = eSub
	}
	edges := make([]*edge, len(parts.Edges))
	for i, ep := range parts.Edges {
		if ep.Reference == nil {
			return nil, wrapDetail(errAssemble, "edge "+strconv.Itoa(i), errEdgeRefRequired)
		}
		class, sub, err := splitType(ep.Type)
		if err != nil {
			return nil, wrapDetail(errAssemble, "edge "+strconv.Itoa(i)+" type", err)
		}
		e := &edge{
			reference:         ep.Reference,
			typeClass:         EdgeClass(class),
			typeSub:           sub,
			contentHash:       ep.ContentHash,
			contentSize:       ep.ContentSize,
			content:           ep.InlineContent,
			relationDirection: ep.RelationDirection,
			fields:            cloneFields(ep.Fields),
		}
		if ep.Encoding != "" {
			eeClass, eeSub, err := splitType(ep.Encoding)
			if err != nil {
				return nil, wrapDetail(errAssemble, "edge "+strconv.Itoa(i)+" encoding", err)
			}
			e.encodingClass = EncodingClass(eeClass)
			e.encodingSub = eeSub
		}
		if ep.ID == nil {
			return nil, withDetail(errAssemble, "edge "+strconv.Itoa(i)+": id required")
		}
		e.id = ep.ID
		edges[i] = e
	}
	// Canonical edge order (by raw multihash), matching construction, so
	// node.edges and Encode() reproduce the original bytes.
	sort.SliceStable(edges, func(i, j int) bool {
		return bytes.Compare(idBytes(edges[i].id), idBytes(edges[j].id)) < 0
	})
	n.edges = make([]Id, len(edges))
	for i, e := range edges {
		n.edges[i] = e.id
	}
	return &claim{node: n, edges: edges, tags: cloneFields(parts.Tags)}, nil
}
