// package: neo4j / persistence-cache
// type:    logic
// job:     map ranke claims to/from neo4j node + relationship property maps (the cache's on-graph shape)
// limits:  pure mapping, no I/O; the Cypher and driver calls live in neo4j.go. Inline content is carried as a legible text property (text encodings only), not base64 — the graph is meant to be read in the browser
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

// A claim is deconstructed into neo4j's native typed graph: the node is
// labelled with node.Type() (e.g. `source/email`) and each edge is a
// relationship whose type IS edge.Type() (e.g. `derivation/source`) — not a
// generic :Claim/:REFERENCES layer. A node MERGE'd only as a reference target
// (before its own PutClaims) is a labelless stub, so `size(labels(n)) > 0`
// distinguishes a cached claim from a stub.

// invalidType is the label/relationship type used when a type is empty. A valid
// claim or edge always has a type (an empty one would not verify), so this
// marker only ever surfaces genuinely malformed data — visibly, in the browser,
// rather than as an opaque Cypher error.
const invalidType = "INVALID"

// labelFor is the neo4j label / relationship type for a claim's node or edge:
// its type verbatim, or invalidType when empty.
func labelFor(t string) string {
	if t == "" {
		return invalidType
	}
	return t
}

// claimParam builds the UNWIND parameter for one claim: the node's properties
// (with its `type` carried for the dynamic label and any legible inline content
// as the `content` property) and its edges nested (each with its `type` for the
// dynamic relationship type and its own inline `content`). Content that is
// external, binary, or over cap is not emitted — it is served by the lower
// layer, and nothing is ever stored outside the claim's own node/relationship.
func (u *neo4jUniverse) claimParam(c ranke.Claim) map[string]any {
	n := c.Node()

	// Edge content is meaningful — an agent's reasoning, a proof, or detail like
	// "is the sister of" on a relation/family edge — so it is inlined as legible
	// text too, gated on the edge's own encoding being a text type (edges now
	// carry an encoding, exactly like nodes).
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

// inlineText returns the content bytes as a legible string when the content is
// inline, within cap, eligible to be text, and valid UTF-8 — so the browser
// shows readable content (a claim's body, or an edge's reasoning/proof/detail
// like "is the sister of"). Otherwise nil: the property is absent, and the claim
// still reconstructs as internal (content_size marks that content exists) with
// the bytes served from the byte layer by claim. Storing readable text (not
// base64) is the point — the graph is meant to be inspected.
//
// eligible gates text-ness — ranke.IsTextEncoding(encoding) for both a node and
// an edge (both carry an encoding): binary content is left out, never base64'd.
func (u *neo4jUniverse) inlineText(eligible bool, size uint64, external bool, get func() ([]byte, error)) any {
	if external || u.contentCap == 0 || size > uint64(u.contentCap) || !eligible {
		return nil
	}
	b, err := get()
	if err != nil || len(b) == 0 || len(b) > u.contentCap || !utf8.Valid(b) {
		return nil
	}
	return string(b)
}

// partsFromNode reconstructs ClaimParts from a claim node's properties, its
// labels (the node's type is its label), and its edge records (each carrying
// its relationship type as rtype). id is the caller-supplied id (a signature —
// never re-derived). Inline content, when the cache holds it, is carried as the
// `content` text property; binary/over-cap content the cache didn't inline is
// left off but still reconstructs as internal (content_size marks it), with the
// bytes served from the byte layer by claim.
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
	// Content location follows §Content: a content_hash means external; an inline
	// `content` property (or, when the cache didn't hold the bytes, just a
	// content_size) means internal. No flag — AssembleClaim derives external from
	// content_hash presence.
	switch {
	case ch != nil: // external — the byte layer serves the blob by hash
		p.ContentHash = ch
		p.ContentSize = uint64(asInt(props["content_size"]))
	case asString(props["content"]) != "": // inline text the cache holds
		p.InlineContent = []byte(asString(props["content"]))
		p.ContentSize = uint64(asInt(props["content_size"]))
	case asInt(props["content_size"]) > 0: // inline, but not held here (binary/over-cap)
		p.ContentSize = uint64(asInt(props["content_size"]))
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
		// The derived edge id is cached on the relationship (edge_id); hand it
		// to AssembleClaim so it need not recompute (and can't drift).
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

// typeFromLabels returns the claim/edge type carried as the node's label (a
// stored claim has exactly its type label; a labelless node is a stub).
func typeFromLabels(labels []any) string {
	for _, l := range labels {
		if s := asString(l); s != "" {
			return s
		}
	}
	return ""
}

// iso8601Nano is the created_at property format: an ISO-8601 UTC timestamp with
// nanosecond precision — legible in the neo4j browser and an exact round-trip of
// the claim's created_at (which feeds the id preimage), unlike a raw unix count.
const iso8601Nano = "2006-01-02T15:04:05.000000000Z"

// A claim's extension fields are projected as first-class neo4j properties —
// each under its own key, so the browser shows and can query them — rather than
// opaque parallel arrays. Reconstruction recovers them as every property that is
// neither a structural projection key (nodeStructural/edgeStructural) nor a
// reserved "_" key (tags/flags).
var nodeStructural = map[string]bool{
	"id": true, "encoding": true, "created_at": true, "height": true,
	"content": true, "content_hash": true, "content_size": true,
}

var edgeStructural = map[string]bool{
	"edge_id": true, "encoding": true, "direction": true,
	"content": true, "content_hash": true, "content_size": true,
}

// fieldsMapOf builds the extension-field property map for a node/edge param
// (empty, never nil, so the Cypher `SET n += fields` is a clean no-op).
func fieldsMapOf(names []string, get func(string) (string, error)) map[string]string {
	m := make(map[string]string, len(names))
	for _, k := range names {
		v, _ := get(k)
		m[k] = v
	}
	return m
}

// fieldsFrom rebuilds a record's extension fields from its properties: every key
// that is neither a structural projection key nor a reserved "_" key. nil when
// none.
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

// tagsFrom extracts the tag overlay from a node's properties — those under the
// reserved (ranke.ReservedPrefix) namespace. nil when none.
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

// tagParam stores a tag value in its natural neo4j type: an integer-valued tag
// (_br revision, _b_<branch> height) as a native integer — legible and
// range-queryable in the browser — otherwise a string. Only canonical integer
// strings convert (round-trip-safe), and tagValue reads them back as strings,
// since the tag contract is string-valued.
func tagParam(v string) any {
	if n, err := strconv.ParseInt(v, 10, 64); err == nil && strconv.FormatInt(n, 10) == v {
		return n
	}
	return v
}

// tagValue reads a tag property back as its string form: a native integer tag
// (see tagParam) is rendered as its digits.
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

// strProp renders a string property, or nil (omitted) when empty — e.g.
// encoding, which per §Content/§Nodes exists only alongside content, so a
// content-less claim carries none.
func strProp(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// directionProp is the relation_direction property: set only on relation/*
// edges, which carry ±1 (§Relations — from=1/to=-1). Other edges have 0 and
// omit it, so there is no meaningless "direction: 0" on a contribution/diff or
// derivation/source edge.
func directionProp(d ranke.RelationDirection) any {
	if d == 0 {
		return nil
	}
	return int64(d)
}

// contentSizeProp is the content_size property value: the byte length when the
// record carries content (§Content — mandatory with content, inline or
// external), else nil so the property is omitted. Without this, content-less
// claims (contributor heads, branch tables) would carry a meaningless
// content_size: 0.
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
