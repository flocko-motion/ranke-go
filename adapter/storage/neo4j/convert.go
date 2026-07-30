// package: neo4j / persistence-cache
// type:    logic
// job:     map ranke claims to/from neo4j node + relationship property maps (the cache's on-graph shape)
// limits:  pure mapping, no I/O; the Cypher and driver calls live in neo4j.go. Inline content is
// carried as a legible text property (text encodings only), not base64 — the graph is
// meant to be read in the browser
package neo4j

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/flocko-motion/ranke-go"
)

var (
	errBadCreatedAt = errors.New("adapter/neo4j: bad created_at")
	errBadEdgeRec   = errors.New("adapter/neo4j: bad edge record")
)

// A claim maps onto neo4j's native typed graph: the node's label is node.Type()
// and each edge is a relationship typed edge.Type(). `size(labels(n)) > 0` tells a
// cached claim from a stub MERGE'd as a reference target.

// invalidType labels a record whose type is empty, so malformed data surfaces
// visibly in the browser rather than as an opaque Cypher error.
const invalidType = "INVALID"

// labelFor is the neo4j label for a node or edge: its type, or invalidType.
func labelFor(t string) string {
	if t == "" {
		return invalidType
	}
	return t
}

// claimParam builds the UNWIND parameter for one claim: the node's properties
// with its edges nested, each carrying `type` for its dynamic label and legible
// inline content as `content`. The lower layer serves everything else.
func (u *neo4jUniverse) claimParam(c ranke.Claim) map[string]any {
	n := c.Node()

	// Edge content is meaningful — an agent's reasoning, or "is the sister of" on a
	// relation/family edge — so text-encoded edge content is inlined too.
	edges := make([]map[string]any, 0, len(c.Edges()))
	for _, e := range c.Edges() {
		ep := map[string]any{
			"edge_id":      e.ID().String(),
			"reference":    e.Reference().String(),
			"type":         labelFor(e.Type()),
			"encoding":     strProp(e.Encoding()),
			"direction":    directionProp(e.RelationDirection()),
			"content_hash": idStr(e.GetContentHash()),
			"content_size": contentSizeProp(e.GetContentHash(), e.GetContentSize()),
			"content":      u.inlineText(ranke.IsTextEncoding(e.Encoding()), e.GetContentSize(), e.ContentKind() == ranke.ContentExternal, e.GetInlineContent),
		}
		ep["fields"] = fieldsMapOf(e.Fields(), e.GetField)
		edges = append(edges, ep)
	}

	np := map[string]any{
		"id":           c.ID().String(),
		"type":         labelFor(n.Type()),
		"encoding":     strProp(n.Encoding()),
		"created_at":   n.CreatedAt().UTC().Format(iso8601Nano),
		"height":       int64(n.Height()),
		"content_hash": idStr(n.GetContentHash()),
		"content_size": contentSizeProp(n.GetContentHash(), n.GetContentSize()),
		"content":      u.inlineText(ranke.IsTextEncoding(n.Encoding()), n.GetContentSize(), n.ContentKind() == ranke.ContentExternal, n.GetInlineContent),
		"fields":       fieldsMapOf(n.Fields(), n.GetField),
		"edges":        edges,
	}
	return np
}

// inlineText returns the content as a legible string, up to the cap
func (u *neo4jUniverse) inlineText(eligible bool, size uint64, external bool, get func() ([]byte, error)) any {
	if external || u.contentCap == 0 || size == 0 || !eligible {
		return nil
	}
	b, err := get()
	if err != nil || len(b) == 0 {
		return nil
	}
	if len(b) > u.contentCap {
		b = truncateUTF8(b, u.contentCap)
	}
	if len(b) == 0 || !utf8.Valid(b) {
		return nil
	}
	return string(b)
}

// contentFrom returns the stored content when its length matches content_size,
// which marks it whole; a shorter string is a prefix from some earlier cap.
func contentFrom(props map[string]any, size uint64) []byte {
	s := asString(props["content"])
	if s == "" || uint64(len(s)) != size {
		return nil
	}
	return []byte(s)
}

// truncateUTF8 cuts b to at most n bytes on a rune boundary, keeping valid text.
func truncateUTF8(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	for n > 0 && !utf8.Valid(b[:n]) {
		n--
	}
	return b[:n]
}

// partsFromNode reconstructs ClaimParts from a claim node's properties, labels
// and edge records. id is the caller's — a signature, so it is never re-derived.
func partsFromNode(id ranke.Id, props map[string]any, labels []any, edgeRecs []any) (ranke.ClaimParts, error) {
	createdAt, err := time.Parse(iso8601Nano, asString(props["created_at"]))
	if err != nil {
		return ranke.ClaimParts{}, fmt.Errorf("%w: claim %s: %w", errBadCreatedAt, id, err)
	}
	p := ranke.ClaimParts{
		ID:        id,
		Type:      typeFromLabels(labels),
		Encoding:  asString(props["encoding"]),
		CreatedAt: createdAt,
		Height:    uint64(asInt(props["height"])),
	}
	ch, err := parseOptID(props["content_hash"])
	if err != nil {
		return ranke.ClaimParts{}, err
	}
	// Content location follows §Content: a content_hash means external, an inline
	// `content` property or a bare content_size means internal.
	switch size := uint64(asInt(props["content_size"])); {
	case ch != nil: // external — the byte layer serves the blob by hash
		p.ContentHash = ch
		p.ContentSize = size
	case contentFrom(props, size) != nil: // inline text the cache holds in full
		p.InlineContent = contentFrom(props, size)
		p.ContentSize = size
	case size > 0: // inline, but not whole here — the byte layer serves it
		p.ContentSize = size
	}
	p.Fields = fieldsFrom(props, nodeStructural)
	p.Tags = tagsFrom(props)

	for _, raw := range edgeRecs {
		er, ok := raw.(map[string]any)
		if !ok {
			return ranke.ClaimParts{}, fmt.Errorf("%w: %T", errBadEdgeRec, raw)
		}
		eprops, _ := er["props"].(map[string]any)
		ref, err := ranke.ParseId(asString(er["ref"]))
		if err != nil {
			return ranke.ClaimParts{}, err
		}
		// The edge id is cached on the relationship, so AssembleClaim can't drift.
		eid, err := parseOptID(eprops["edge_id"])
		if err != nil {
			return ranke.ClaimParts{}, err
		}
		ep := ranke.EdgeParts{
			ID:                eid,
			Reference:         ref,
			Type:              asString(er["rtype"]), // the relationship type IS the edge type
			Encoding:          asString(eprops["encoding"]),
			RelationDirection: ranke.RelationDirection(asInt(eprops["direction"])),
			Fields:            fieldsFrom(eprops, edgeStructural),
		}
		ech, err := parseOptID(eprops["content_hash"])
		if err != nil {
			return ranke.ClaimParts{}, err
		}
		switch {
		case ech != nil: // external
			ep.ContentHash = ech
			ep.ContentSize = uint64(asInt(eprops["content_size"]))
		case asString(eprops["content"]) != "": // inline text the cache holds
			ep.InlineContent = []byte(asString(eprops["content"]))
			ep.ContentSize = uint64(asInt(eprops["content_size"]))
		case asInt(eprops["content_size"]) > 0: // inline, but not held here
			ep.ContentSize = uint64(asInt(eprops["content_size"]))
		}
		p.Edges = append(p.Edges, ep)
	}
	return p, nil
}

// typeFromLabels returns the type carried as the node's label; a stub has none.
func typeFromLabels(labels []any) string {
	for _, l := range labels {
		if s := asString(l); s != "" {
			return s
		}
	}
	return ""
}

// iso8601Nano is the created_at format: legible in the browser and an exact
// round-trip of the created_at that feeds the id preimage.
const iso8601Nano = "2006-01-02T15:04:05.000000000Z"

// Extension fields are projected as first-class properties, each under its own
// key, so the browser shows and can query them.
var nodeStructural = map[string]bool{
	"id": true, "encoding": true, "created_at": true, "height": true,
	"content": true, "content_hash": true, "content_size": true,
}

var edgeStructural = map[string]bool{
	"edge_id": true, "encoding": true, "direction": true,
	"content": true, "content_hash": true, "content_size": true,
}

// fieldsMapOf builds the extension-field property map: empty, never nil, so the
// Cypher `SET n += fields` stays a clean no-op.
func fieldsMapOf(names []string, get func(string) (string, error)) map[string]string {
	m := make(map[string]string, len(names))
	for _, k := range names {
		v, _ := get(k)
		m[k] = v
	}
	return m
}

// fieldsFrom rebuilds a record's extension fields: every property key that is
// neither a structural projection key nor a reserved "_" key. nil when none.
func fieldsFrom(props map[string]any, structural map[string]bool) map[string]string {
	var out map[string]string
	for k, v := range props {
		if structural[k] || strings.HasPrefix(k, ranke.ReservedPrefix) {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = asString(v)
	}
	return out
}

// tagsFrom extracts the tag overlay: properties under ranke.ReservedPrefix.
func tagsFrom(props map[string]any) map[string]string {
	var out map[string]string
	for k, v := range props {
		if !strings.HasPrefix(k, ranke.ReservedPrefix) {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = tagValue(v) // key carries the "_" prefix — stored verbatim
	}
	return out
}

// tagParam stores a tag in its natural neo4j type: a canonical integer string as
// a native integer, range-queryable in the browser, anything else as a string.
func tagParam(v string) any {
	if n, err := strconv.ParseInt(v, 10, 64); err == nil && strconv.FormatInt(n, 10) == v {
		return n
	}
	return v
}

// tagValue reads a tag property back as a string, an integer tag as its digits.
func tagValue(v any) string {
	if n, ok := v.(int64); ok {
		return strconv.FormatInt(n, 10)
	}
	return asString(v)
}

// idStr renders an id for a property, or nil (neo4j null) when absent.
func idStr(id ranke.Id) any {
	if id == nil {
		return nil
	}
	return id.String()
}

// strProp renders a string property, nil when empty — e.g. encoding, which per
// §Content/§Nodes exists only alongside content.
func strProp(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// directionProp is the relation_direction property, set only on relation/* edges,
// which carry ±1 (§Relations — from=1/to=-1); a 0 is omitted.
func directionProp(d ranke.RelationDirection) any {
	if d == 0 {
		return nil
	}
	return int64(d)
}

// contentSizeProp is the content_size property: the byte length whenever the
// record carries content (§Content), else nil so the property is omitted.
func contentSizeProp(hash ranke.Id, size uint64) any {
	if hash == nil && size == 0 {
		return nil
	}
	return int64(size)
}

// parseOptID parses an optional id property (nil/absent → nil id).
func parseOptID(v any) (ranke.Id, error) {
	s := asString(v)
	if s == "" {
		return nil, nil
	}
	return ranke.ParseId(s)
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func asInt(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}
