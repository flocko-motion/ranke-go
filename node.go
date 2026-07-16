// package: ranke / node
// type:    data
// job:     the Node structural component of a claim, plus the closed node/encoding type vocabularies
// limits:  does not build nodes (-> claim) or serialize them (-> serialize)
package ranke

import (
	"bytes"
	"context"
	"io"
	"sort"
	"time"
)

// Node is the structural component of a claim. A node's id is the
// claim id. Two nodes with identical content but different
// provenance produce different ids (spec §4.1).
type Node interface {
	Type() string
	TypeClass() NodeClass
	TypeSub() string
	// GetContentHash is the address of EXTERNAL content, H(content); nil for
	// inline content (whose bytes are in the node, §Content) and for no content.
	GetContentHash() Id
	// GetContentSize is the content's byte length (0 when no content).
	// Paired with the hash to defend against truncation/extension and to
	// let storage layers know the size without loading the bytes.
	GetContentSize() uint64
	// ContentKind reports whether and where the node's content lives — Inline,
	// External, or None. A node always knows its own kind (never Unknown);
	// GetClaimContent uses it to route. Reads through a diff overlay via the
	// effective content source.
	ContentKind() ContentKind
	// GetInlineContent returns the inline content bytes (nil when the node
	// carries no content); it errors when the content is external — check
	// ContentKind first, or use GetContent with a Universe.
	GetInlineContent() ([]byte, error)
	// GetContent returns a reader over the content, transparently
	// streaming external content from u; u may be nil for inline content.
	GetContent(ctx context.Context, u Universe) (io.Reader, error)
	Encoding() string
	EncodingClass() EncodingClass
	EncodingSub() string
	CreatedAt() time.Time
	// Height is the claim's generation number in the provenance DAG: 0 for
	// an initial node (no edges), else 1 + max over the heights of the
	// claims this one references (§4.1). Fixed at creation and covered by
	// the node hash, so the id commits to it; the verifier re-derives and
	// enforces it against the closure.
	Height() uint64
	// Edges returns the ids of edges created with this claim, in
	// canonical (sort) order.
	Edges() []Id
	HasField(name string) bool
	GetField(name string) (string, error)
	Fields() []string
	ID() Id
}

// node is the concrete implementation of Node. The edges field carries
// the ids of edges created with the owning claim, in canonical sort
// order; this set participates in the node hash.
type node struct {
	typeClass     NodeClass
	typeSub       string
	encodingClass EncodingClass
	encodingSub   string
	contentHash   Id     // external content: its address; nil for inline or no content
	content       []byte // inline content bytes, kept with the node; nil when external or none
	contentSize   uint64 // paired with content/contentHash to defend against truncation/extension
	createdAt     time.Time
	height        uint64 // generation number: 0 for an initial node, else 1 + max(reference heights)
	edges         []Id   // edge ids, sorted canonically
	fields        map[string]string
	id            Id // = Sign(H(S(node))); also the claim id

	// Diff materialisation (set by the loader when the owning claim is a
	// contribution/diff overlay): diffNode is the materialised predecessor
	// node; diffFields is the merged field map (diffNode's fields, minus
	// fields_diff_omit, overlaid with self's), computed once at load. The
	// delta (fields, contentHash, edges) is left untouched so id/Encode
	// stay the claim's own bytes.
	diffNode   *node
	diffFields map[string]string
}

// fieldMap is the effective field set: the delta for a plain node, the
// merged map for a diff node.
func (n *node) fieldMap() map[string]string {
	if n.diffNode == nil {
		return n.fields
	}
	return n.diffFields
}

// computeDiffFields builds diffFields (inherit diffNode → drop
// fields_diff_omit → overlay self). Called once at materialisation.
func (n *node) computeDiffFields() {
	m := make(map[string]string, len(n.diffNode.fieldMap())+len(n.fields))
	for k, v := range n.diffNode.fieldMap() {
		m[k] = v
	}
	for name := range splitLines(n.fields[FieldFieldsDiffOmit]) {
		delete(m, name)
	}
	for k, v := range n.fields {
		m[k] = v
	}
	n.diffFields = m
}

// contentSource is the node that supplies this node's content: self if it
// sets its own content — inline (content) or external (content_hash) — else the
// diff predecessor's source (content is inherited unless a diff restates it).
func (n *node) contentSource() *node {
	if n.content != nil || n.contentHash != nil || n.diffNode == nil {
		return n
	}
	return n.diffNode.contentSource()
}

// node accessor methods. Construction lives in claim.go (the node is
// built as part of NewClaim) since a node's id is the claim id and
// its edge list is finalized at claim construction.

func (n *node) Type() string {
	return string(n.typeClass) + "/" + n.typeSub
}
func (n *node) TypeClass() NodeClass { return n.typeClass }
func (n *node) TypeSub() string      { return n.typeSub }

func (n *node) Encoding() string {
	cs := n.contentSource()
	if cs.encodingClass == "" && cs.encodingSub == "" {
		return ""
	}
	return string(cs.encodingClass) + "/" + cs.encodingSub
}
func (n *node) EncodingClass() EncodingClass { return n.contentSource().encodingClass }
func (n *node) EncodingSub() string          { return n.contentSource().encodingSub }

func (n *node) GetContentHash() Id     { return n.contentSource().contentHash }
func (n *node) GetContentSize() uint64 { return n.contentSource().contentSize }
func (n *node) CreatedAt() time.Time   { return n.createdAt }

// ContentKind derives from the effective source's fields. content_hash marks
// External (§Content: content and content_hash are mutually exclusive, and the
// hash is retained even by a structure-only cache). Otherwise inline bytes — or
// a non-zero content_size — mark Inline: the size lets a structure-only cache
// that dropped the inline bytes still report Inline, so GetClaimContent falls
// through to the byte layer for them. Neither ⇒ None. Never Unknown — a node
// always knows its own kind.
func (n *node) ContentKind() ContentKind {
	cs := n.contentSource()
	switch {
	case cs.contentHash != nil:
		return ContentExternal
	case cs.content != nil || cs.contentSize > 0:
		return ContentInline
	default:
		return ContentNone
	}
}

// Height returns the node's own generation number. Unlike content and the
// field map, height is never inherited through a diff overlay — each claim
// (delta or not) carries its own height, so this reads the node directly.
func (n *node) Height() uint64 { return n.height }
func (n *node) ID() Id         { return n.id }

func (n *node) Edges() []Id {
	out := make([]Id, len(n.edges))
	copy(out, n.edges)
	return out
}

func (n *node) GetInlineContent() ([]byte, error) {
	cs := n.contentSource()
	if cs.content == nil && cs.contentHash != nil {
		return nil, errContentExternal
	}
	return cs.content, nil
}

func (n *node) GetContent(ctx context.Context, u Universe) (io.Reader, error) {
	cs := n.contentSource()
	switch n.ContentKind() {
	case ContentExternal:
		if u == nil {
			return nil, errNoUniverseForContent
		}
		return u.StreamContent(ctx, cs.contentHash, cs.contentSize)
	case ContentInline:
		if cs.content != nil {
			return bytes.NewReader(cs.content), nil // complete: bytes in hand
		}
		if u == nil {
			return nil, errNoUniverseForContent
		}
		return claimInlineReader(ctx, u, cs.id) // structure-only: re-fetch dropped bytes
	default: // ContentNone
		return bytes.NewReader(nil), nil
	}
}

func (n *node) HasField(name string) bool {
	_, ok := n.fieldMap()[name]
	return ok
}

func (n *node) GetField(name string) (string, error) {
	v, ok := n.fieldMap()[name]
	if !ok {
		return "", withDetail(errFieldNotSet, name)
	}
	return v, nil
}

func (n *node) Fields() []string {
	m := n.fieldMap()
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// TypeSource returns the "source/<sub>" node type.
func TypeSource(sub string) string { return nodeType(NodeClassSource, sub) }

// TypeDerivation returns the "derivation/<sub>" node type.
func TypeDerivation(sub string) string { return nodeType(NodeClassDerivation, sub) }

// TypeEntity returns the "entity/<sub>" node type.
func TypeEntity(sub string) string { return nodeType(NodeClassEntity, sub) }

// TypeRelation returns the "relation/<sub>" node type.
func TypeRelation(sub string) string { return nodeType(NodeClassRelation, sub) }

func nodeType(class NodeClass, sub string) string { return string(class) + "/" + sub }

// Closed contribution/* node type strings, for ClaimBuilder.Type.
const (
	NodeContributor = string(NodeClassContribution) + "/contributor"
	NodeHead        = string(NodeClassContribution) + "/head"
	NodeBranches    = string(NodeClassContribution) + "/branches"
)
