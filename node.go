package ranke

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

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
