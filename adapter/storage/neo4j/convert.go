// package: neo4j / persistence-cache
// type:    logic
// job:     map ranke claims to/from neo4j node + relationship property maps (the cache's on-graph shape)
// limits:  pure mapping, no I/O; the Cypher and driver calls live in neo4j.go. Content bytes are carried as base64 strings (neo4j has no byte-array property type)
package neo4j

import (
	"encoding/base64"
	"fmt"
	"time"

	"github.com/flocko-motion/ranke-go"
)

// Graph vocabulary. Full claims carry a `type`; a node MERGE'd only as a
// reference target (before its own PutClaims) is a stub with just `id`, so
// `type IS NOT NULL` distinguishes a cached claim from a stub.
const (
	labelClaim    = "Claim"
	labelContent  = "Content"
	relReferences = "REFERENCES"
)

// claimParam builds the UNWIND parameter for one claim — the :Claim node
// properties with its edges nested — plus the inline-content parameters (node
// and edge content within cap) to upsert as :Content nodes. Content that is
// external or over cap is intentionally not emitted (served by the lower
// layer).
func (u *neo4jUniverse) claimParam(c ranke.Claim) (map[string]any, []map[string]any) {
	n := c.Node()
	var contents []map[string]any
	if ct := u.contentParam(n.GetContentHash(), n.GetContentSize(), n.IsContentExternal(), n.GetInlineContent); ct != nil {
		contents = append(contents, ct)
	}

	edges := make([]map[string]any, 0, len(c.Edges()))
	for _, e := range c.Edges() {
		ep := map[string]any{
			"edge_id":      e.ID().String(),
			"reference":    e.Reference().String(),
			"type":         e.Type(),
			"direction":    int64(e.RelationDirection()),
			"content_hash": idStr(e.GetContentHash()),
			"content_size": int64(e.GetContentSize()),
		}
		setFields(ep, e.Fields(), e.GetField)
		edges = append(edges, ep)
		if ct := u.contentParam(e.GetContentHash(), e.GetContentSize(), e.IsContentExternal(), e.GetInlineContent); ct != nil {
			contents = append(contents, ct)
		}
	}

	np := map[string]any{
		"id":           c.ID().String(),
		"type":         n.Type(),
		"encoding":     n.Encoding(),
		"created_at":   n.CreatedAt().UTC().UnixNano(),
		"content_hash": idStr(n.GetContentHash()),
		"content_size": int64(n.GetContentSize()),
		"edges":        edges,
	}
	setFields(np, n.Fields(), n.GetField)
	return np, contents
}

// contentParam yields a {hash, size, b64} :Content parameter when the content
// is inline and within cap, else nil (external / over-cap / structure-only).
func (u *neo4jUniverse) contentParam(hash ranke.Id, size uint64, external bool, get func() ([]byte, error)) map[string]any {
	if hash == nil || external || u.contentCap == 0 || size > uint64(u.contentCap) {
		return nil
	}
	b, err := get()
	if err != nil || len(b) == 0 || len(b) > u.contentCap {
		return nil
	}
	return map[string]any{
		"hash": hash.String(),
		"size": int64(size),
		"b64":  base64.StdEncoding.EncodeToString(b),
	}
}

// partsFromNode reconstructs ClaimParts from a claim node's properties, its
// edge records, and a hash→bytes map of any inline content the cache held.
// id is the caller-supplied id (a signature — never re-derived). Content the
// map lacks is left off, so the claim reconstructs as external.
func partsFromNode(id ranke.Id, props map[string]any, edgeRecs []any, content map[string][]byte) (ranke.ClaimParts, error) {
	p := ranke.ClaimParts{
		ID:        id,
		Type:      asString(props["type"]),
		Encoding:  asString(props["encoding"]),
		CreatedAt: time.Unix(0, asInt(props["created_at"])).UTC(),
	}
	ch, err := parseOptID(props["content_hash"])
	if err != nil {
		return ranke.ClaimParts{}, err
	}
	if ch != nil {
		p.ContentHash = ch
		p.ContentSize = uint64(asInt(props["content_size"]))
		if b, ok := content[ch.String()]; ok {
			p.InlineContent = b
		}
	}
	p.Fields = fieldsFrom(props)

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
		ep := ranke.EdgeParts{
			Reference:         ref,
			Type:              asString(eprops["type"]),
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
			if b, ok := content[ech.String()]; ok {
				ep.InlineContent = b
			}
		}
		p.Edges = append(p.Edges, ep)
	}
	return p, nil
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
