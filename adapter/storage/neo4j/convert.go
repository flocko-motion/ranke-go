// package: neo4j / persistence-cache
// type:    logic
// job:     map ranke claims to/from neo4j node + relationship property maps (the cache's on-graph shape)
// limits:  pure mapping, no I/O; the Cypher and driver calls live in neo4j.go. Inline content is carried as a legible text property (text encodings only), not base64 — the graph is meant to be read in the browser
package neo4j

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/flocko-motion/ranke-go"
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
	// text too. Edges carry no encoding yet, so eligibility is UTF-8 validity
	// alone (pass IsTextEncoding here once edges expose an encoding).
	edges := make([]map[string]any, 0, len(c.Edges()))
	for _, e := range c.Edges() {
		ep := map[string]any{
			"edge_id":      e.ID().String(),
			"reference":    e.Reference().String(),
			"type":         labelFor(e.Type()),
			"direction":    int64(e.RelationDirection()),
			"content_hash": idStr(e.GetContentHash()),
			"content_size": int64(e.GetContentSize()),
			"content":      u.inlineText(true, e.GetContentSize(), e.IsContentExternal(), e.GetInlineContent),
		}
		setFields(ep, e.Fields(), e.GetField)
		setContentFlag(ep, e.GetContentHash(), e.IsContentExternal())
		edges = append(edges, ep)
	}

	np := map[string]any{
		"id":           c.ID().String(),
		"type":         labelFor(n.Type()),
		"encoding":     n.Encoding(),
		"created_at":   n.CreatedAt().UTC().UnixNano(),
		"height":       int64(n.Height()),
		"content_hash": idStr(n.GetContentHash()),
		"content_size": int64(n.GetContentSize()),
		"content":      u.inlineText(ranke.IsTextEncoding(n.Encoding()), n.GetContentSize(), n.IsContentExternal(), n.GetInlineContent),
		"edges":        edges,
	}
	setFields(np, n.Fields(), n.GetField)
	setContentFlag(np, n.GetContentHash(), n.IsContentExternal())
	return np
}

// setContentFlag stamps exactly one content-location flag when the node/edge
// carries content: _ci (inline) or _ce (external). A content-bearing record
// with neither is a broken node (partsFromNode rejects it), so the retrieval
// path never guesses.
func setContentFlag(m map[string]any, hash ranke.Id, external bool) {
	if hash == nil {
		return // no content — no flag
	}
	if external {
		m[ranke.ContentExternalKey] = true
	} else {
		m[ranke.ContentInternalKey] = true
	}
}

// contentFlag reads the content-location flag from a content-bearing record's
// props: exactly one of _ci/_ce must be set. It returns whether the content is
// external, or an error when neither (or both) is set — a broken node, surfaced
// rather than guessed.
func contentFlag(props map[string]any, what string) (external bool, err error) {
	ci := asBool(props[ranke.ContentInternalKey])
	ce := asBool(props[ranke.ContentExternalKey])
	if ci == ce { // neither set, or both — malformed
		return false, fmt.Errorf("adapter/neo4j: %s carries content but not exactly one of %s/%s — broken node",
			what, ranke.ContentInternalKey, ranke.ContentExternalKey)
	}
	return ce, nil
}

// inlineText returns the content bytes as a legible string when the content is
// inline, within cap, eligible to be text, and valid UTF-8 — so the browser
// shows readable content (a claim's body, or an edge's reasoning/proof/detail
// like "is the sister of"). Otherwise nil: the property is absent and the claim
// reconstructs as external for the durable layer to serve. Storing readable
// text (not base64) is the point — the graph is meant to be inspected.
//
// eligible gates text-ness: for a node it is ranke.IsTextEncoding(encoding);
// for an edge (which carries no encoding yet) it is true, so valid UTF-8 is the
// sole signal. Once edges expose an encoding, pass IsTextEncoding for them too.
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
// `content` text property; content the cache lacks is left off, so the claim
// reconstructs as external and the durable layer serves it.
func partsFromNode(id ranke.Id, props map[string]any, labels []any, edgeRecs []any) (ranke.ClaimParts, error) {
	p := ranke.ClaimParts{
		ID:        id,
		Type:      typeFromLabels(labels),
		Encoding:  asString(props["encoding"]),
		CreatedAt: time.Unix(0, asInt(props["created_at"])).UTC(),
		Height:    uint64(asInt(props["height"])),
	}
	ch, err := parseOptID(props["content_hash"])
	if err != nil {
		return ranke.ClaimParts{}, err
	}
	if ch != nil {
		p.ContentHash = ch
		p.ContentSize = uint64(asInt(props["content_size"]))
		ext, err := contentFlag(props, "claim "+id.String())
		if err != nil {
			return ranke.ClaimParts{}, err
		}
		p.ContentExternal = ext // authoritative: neo4j may hold the hash without the bytes
		if s := asString(props["content"]); s != "" {
			p.InlineContent = []byte(s) // legible text, stored verbatim
		}
	}
	p.Fields = fieldsFrom(props)
	p.Tags = tagsFrom(props)

	for _, raw := range edgeRecs {
		er, ok := raw.(map[string]any)
		if !ok {
			return ranke.ClaimParts{}, fmt.Errorf("adapter/neo4j: bad edge record %T", raw)
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
			RelationDirection: ranke.RelationDirection(asInt(eprops["direction"])),
			Fields:            fieldsFrom(eprops),
		}
		ech, err := parseOptID(eprops["content_hash"])
		if err != nil {
			return ranke.ClaimParts{}, err
		}
		if ech != nil {
			ep.ContentHash = ech
			ep.ContentSize = uint64(asInt(eprops["content_size"]))
			ext, err := contentFlag(eprops, "edge "+asString(eprops["edge_id"]))
			if err != nil {
				return ranke.ClaimParts{}, err
			}
			ep.ContentExternal = ext
			if s := asString(eprops["content"]); s != "" {
				ep.InlineContent = []byte(s) // legible text, stored verbatim
			}
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

// setFields stores a name→value map as parallel string arrays, omitting them
// entirely when empty (neo4j rejects empty-typed list properties).
func setFields(m map[string]any, names []string, get func(string) (string, error)) {
	if len(names) == 0 {
		return
	}
	keys := make([]string, len(names))
	vals := make([]string, len(names))
	for i, k := range names {
		v, _ := get(k)
		keys[i] = k
		vals[i] = v
	}
	m["field_keys"] = keys
	m["field_vals"] = vals
}

// fieldsFrom rebuilds the field map from the parallel arrays (nil when absent).
func fieldsFrom(props map[string]any) map[string]string {
	keys := asStringSlice(props["field_keys"])
	vals := asStringSlice(props["field_vals"])
	if len(keys) == 0 {
		return nil
	}
	out := make(map[string]string, len(keys))
	for i, k := range keys {
		if i < len(vals) {
			out[k] = vals[i]
		}
	}
	return out
}

// tagsFrom extracts the tag overlay from a node's properties — those under the
// reserved (ranke.ReservedPrefix) namespace, minus codec flags. nil when none.
func tagsFrom(props map[string]any) map[string]string {
	var out map[string]string
	for k, v := range props {
		if !strings.HasPrefix(k, ranke.ReservedPrefix) {
			continue
		}
		if k == ranke.ContentInternalKey || k == ranke.ContentExternalKey {
			continue // reserved codec flags (_ci/_ce), not tags
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = asString(v) // key carries the "_" prefix — stored verbatim
	}
	return out
}

// idStr renders an id for a property, or nil (neo4j null) when absent.
func idStr(id ranke.Id) any {
	if id == nil {
		return nil
	}
	return id.String()
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

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
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

func asStringSlice(v any) []string {
	switch xs := v.(type) {
	case []string:
		return xs
	case []any:
		out := make([]string, 0, len(xs))
		for _, x := range xs {
			out = append(out, asString(x))
		}
		return out
	}
	return nil
}
