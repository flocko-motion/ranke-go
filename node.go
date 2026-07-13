// package: ranke / node
// type:    data
// job:     the Node structural component of a claim, plus the closed node/encoding type vocabularies
// limits:  does not build nodes (-> claim) or serialize them (-> serialize)
package ranke

import (
	"errors"
	"fmt"
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
	// ContentHash is H(content); nil if the node carries no content.
	ContentHash() Id
	// Size is the byte-length of the content addressed by ContentHash;
	// 0 when no content. Paired with ContentHash to defend against
	// truncation/extension and let storage layers know size without
	// loading the bytes.
	Size() uint64
	// Content returns the node's content bytes, or (nil, nil) if none.
	Content() ([]byte, error)
	Encoding() string
	EncodingClass() EncodingClass
	EncodingSub() string
	CreatedAt() time.Time
	// Edges returns the ids of edges created with this claim, in
	// canonical (sort) order.
	Edges() []Id
	HasField(name string) bool
	GetField(name string) (string, error)
	Fields() []string
	// Pubkey returns the multikey-encoded public key on this node
	// (§5.7). Non-empty only on signed contributor claims.
	Pubkey() []byte
	// Title is the node's optional short text label, or "" if unset.
	// Omitted from the canonical encoding when empty.
	Title() string
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
	title         string
	contentHash   Id     // nil when no content
	content       []byte // raw content bytes, kept with the node
	size          uint64 // = len(content); paired with contentHash to defend against truncation/extension
	createdAt     time.Time
	edges         []Id // edge ids, sorted canonically
	fields        map[string]string
	pubkey        []byte // multikey-encoded pubkey on contributor nodes (§5.7); empty otherwise
	id            Id     // = Sign(H(S(node))); also the claim id
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
	if n.encodingClass == "" && n.encodingSub == "" {
		return ""
	}
	return string(n.encodingClass) + "/" + n.encodingSub
}
func (n *node) EncodingClass() EncodingClass { return n.encodingClass }
func (n *node) EncodingSub() string          { return n.encodingSub }

func (n *node) ContentHash() Id      { return n.contentHash }
func (n *node) Size() uint64         { return n.size }
func (n *node) CreatedAt() time.Time { return n.createdAt }
func (n *node) ID() Id               { return n.id }

func (n *node) Edges() []Id {
	out := make([]Id, len(n.edges))
	copy(out, n.edges)
	return out
}

func (n *node) Content() ([]byte, error) {
	if n.contentHash == nil {
		return nil, nil
	}
	if n.content == nil {
		return nil, errors.New("ranke.Node.Content: content not loaded")
	}
	return n.content, nil
}

func (n *node) HasField(name string) bool {
	_, ok := n.fields[name]
	return ok
}

func (n *node) GetField(name string) (string, error) {
	v, ok := n.fields[name]
	if !ok {
		return "", fmt.Errorf("ranke.Node.GetField: %q not set", name)
	}
	return v, nil
}

func (n *node) Fields() []string {
	names := make([]string, 0, len(n.fields))
	for k := range n.fields {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

func (n *node) Pubkey() []byte {
	if len(n.pubkey) == 0 {
		return nil
	}
	out := make([]byte, len(n.pubkey))
	copy(out, n.pubkey)
	return out
}

func (n *node) Title() string { return n.title }

// EncodingApplication returns the "application/<sub>" media type.
//
//deadcode:keep
func EncodingApplication(sub string) string { return encType(encApplication, sub) }

// EncodingAudio returns the "audio/<sub>" media type.
//
//deadcode:keep
func EncodingAudio(sub string) string { return encType(encAudio, sub) }

// EncodingExample returns the "example/<sub>" media type.
//
//deadcode:keep
func EncodingExample(sub string) string { return encType(encExample, sub) }

// EncodingFont returns the "font/<sub>" media type.
//
//deadcode:keep
func EncodingFont(sub string) string { return encType(encFont, sub) }

// EncodingImage returns the "image/<sub>" media type.
//
//deadcode:keep
func EncodingImage(sub string) string { return encType(encImage, sub) }

// EncodingMessage returns the "message/<sub>" media type.
func EncodingMessage(sub string) string { return encType(encMessage, sub) }

// EncodingModel returns the "model/<sub>" media type.
//
//deadcode:keep
func EncodingModel(sub string) string { return encType(encModel, sub) }

// EncodingMultipart returns the "multipart/<sub>" media type.
//
//deadcode:keep
func EncodingMultipart(sub string) string { return encType(encMultipart, sub) }

// EncodingText returns the "text/<sub>" media type.
//
//deadcode:keep
func EncodingText(sub string) string { return encType(encText, sub) }

// EncodingVideo returns the "video/<sub>" media type.
//
//deadcode:keep
func EncodingVideo(sub string) string { return encType(encVideo, sub) }

func encType(class EncodingClass, sub string) string { return string(class) + "/" + sub }

// TypeSource returns the "source/<sub>" node type.
func TypeSource(sub string) string { return nodeType(NodeSource, sub) }

// TypeDerivation returns the "derivation/<sub>" node type.
func TypeDerivation(sub string) string { return nodeType(NodeDerivation, sub) }

// TypeEntity returns the "entity/<sub>" node type.
func TypeEntity(sub string) string { return nodeType(NodeEntity, sub) }

// TypeRelation returns the "relation/<sub>" node type.
func TypeRelation(sub string) string { return nodeType(NodeRelation, sub) }

func nodeType(class NodeClass, sub string) string { return string(class) + "/" + sub }

// Closed contribution/* node type strings, for ClaimBuilder.Type.
const (
	NodeContributor = string(NodeContribution) + "/contributor"
	NodeHead        = string(NodeContribution) + "/head"
	NodeBranches    = string(NodeContribution) + "/branches"
)
