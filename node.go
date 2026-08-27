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

// Node is the structural component of a claim; its id is the claim id.
// Identical content under different provenance yields different ids (spec §4.1).
type Node interface {
	Type() string
	TypeClass() NodeClass
	TypeSub() string
	// GetContentHash is the address of EXTERNAL content, H(content); nil for
	// inline content (bytes in the node, §Content) and for no content.
	GetContentHash() Id
	// GetContentSize is the content's full byte length (0 when no content), whatever
	// this record holds of it: paired with the hash it defends against truncation, and
	// against GetInlineContent it says how much arrived.
	GetContentSize() uint64
	// ContentKind reports where the node's content lives — Inline, External, or
	// None — reading through a diff overlay via the effective content source.
	ContentKind() ContentKind
	// ContentComplete reports whether this record holds every byte it declares. A read
	// under a content cap serves a prefix (`R-QCONTENT`), so a caller needing the whole
	// content asks here rather than assuming what GetInlineContent returned is all of it.
	ContentComplete() bool
	// GetInlineContent returns the inline content bytes, which may be a PREFIX of the
	// content the node declares — check ContentComplete. It errors when the content is
	// external; check ContentKind first, or use GetContent with a Universe.
	GetInlineContent() ([]byte, error)
	// GetContent returns a reader over the content, transparently
	// streaming external content from u; u may be nil for inline content.
	GetContent(ctx context.Context, u Universe) (io.Reader, error)
	Encoding() string
	EncodingClass() EncodingClass
	EncodingSub() string
	CreatedAt() time.Time
	// Dated is the time the claim's subject is assumed to stem from, an EDTF Level 1
	// value (`V-DATED`); "" when absent. Unlike CreatedAt it denotes an interval, not
	// an instant, and is neither `V-TIME`- nor `V-MONO`-constrained.
	Dated() string
	// Height is the claim's generation number: 0 for an initial claim, else 1 + max
	// over referenced heights (§4.1). In the node hash, so the id commits to it.
	Height() uint64
	// Edges returns the ids of edges created with this claim, in canonical order.
	Edges() []Id
	HasField(name string) bool
	GetField(name string) (string, error)
	Fields() []string
	ID() Id
}

// node is the concrete implementation of Node; edges participates in the node hash.
type node struct {
	typeClass     NodeClass
	typeSub       string
	encodingClass EncodingClass
	encodingSub   string
	contentHash   Id     // external content: its address; nil for inline or no content
	content       []byte // inline content bytes, kept with the node; nil when external or none
	contentSize   uint64 // paired with content/contentHash to defend against truncation/extension
	createdAt     time.Time
	dated         string // EDTF Level 1 (`V-DATED`); "" when absent
	height        uint64 // generation number: 0 for an initial claim, else 1 + max(reference heights)
	edges         []Id   // edge ids, sorted canonically
	fields        map[string]string
	id            Id // = Sign(H(S(node))); also the claim id

	// Diff overlay, set by the loader once at load: the materialised predecessor
	// and the merged field map. The delta stays as the claim's own bytes for id/Encode.
	diffNode   *node
	diffFields map[string]string
}

// fieldMap is the effective field set: the delta, or the merged map for a diff node.
func (n *node) fieldMap() map[string]string {
	if n.diffNode == nil {
		return n.fields
	}
	return n.diffFields
}

// flattened is the node with its diff overlay resolved into its own fields and
// content, so an encode emits the values a materialised read presents.
func (n *node) flattened() *node {
	if n.diffNode == nil {
		return n
	}
	cs := n.contentSource()
	out := *n
	out.diffNode, out.diffFields = nil, nil
	out.fields = n.fieldMap()
	out.content, out.contentHash, out.contentSize = cs.content, cs.contentHash, cs.contentSize
	out.encodingClass, out.encodingSub = cs.encodingClass, cs.encodingSub
	return &out
}

// computeDiffFields builds diffFields (`V-DIFF`): inherit diffNode → drop
// fields_diff_omit → overlay self.
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

// contentSource is the node supplying this node's content: self when it sets its own
// (inline or content_hash), else the predecessor's — content inherits whole (`V-DIFF`).
func (n *node) contentSource() *node {
	if n.content != nil || n.contentHash != nil || n.diffNode == nil {
		return n
	}
	return n.diffNode.contentSource()
}

// Node accessors; construction lives in claim.go, since a node's id is the claim id.

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
func (n *node) Dated() string          { return n.dated }

// ContentKind derives from the effective source: content_hash ⇒ External (§Content:
// exclusive with inline), bytes or a non-zero content_size ⇒ Inline, else None.
func (n *node) ContentKind() ContentKind {
	cs := n.contentSource()
	switch {
	case cs.contentHash != nil:
		return ContentExternal
	case cs.content != nil || cs.contentSize > 0:
		return ContentInline // size alone: a structure-only cache dropped the bytes
	default:
		return ContentNone
	}
}

// ContentComplete compares what the record holds against what it declares. External
// content is never held, so it is complete only once fetched; no content is complete
// trivially, both lengths being zero.
func (n *node) ContentComplete() bool {
	cs := n.contentSource()
	if cs.contentHash != nil {
		return false
	}
	return uint64(len(cs.content)) == cs.contentSize
}

// Height returns the claim's own generation number, read straight off the node.
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
		return "", WithDetail(errFieldNotSet, name)
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
	// NodeDelete and NodeExpiry are limiting claims: each names a target claim it
	// restricts (paper 2 §Deletion, §Key rotation).
	NodeDelete = string(NodeClassContribution) + "/delete"
	NodeExpiry = string(NodeClassContribution) + "/expiry"
)
