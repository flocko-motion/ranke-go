// package: ranke / claim
// type:    io
// job:     canonical claim (de)serialization — Claim.Encode and DecodeClaim, storage-agnostic inverses
// limits:  stores nothing (-> universe, adapter); content integrity lives in content.go
package ranke

import (
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

// Claim codec — the canonical serialization of a claim, storage-agnostic.
// Claim.Encode and the package-level DecodeClaim are inverses; the same
// bytes represent a claim whether it lives in memory, is written to a
// file, sent over a wire, or stored in an object store. Persistence
// adapters use them so they never need to know a claim's internal
// representation; they move opaque bytes. Content is content — it does
// not care where it is stored.

// encClaimFile is the canonical serialized shape of a claim: its node
// plus edges, CBOR Deterministic per serialize.go.
type encClaimFile struct {
	Node  encNode   `cbor:"1,keyasint"`
	Edges []encEdge `cbor:"2,keyasint,omitempty"`
}

// Encode serializes the claim to its canonical CBOR bytes — the same
// bytes its id is derived from, and the inverse of DecodeClaim.
func (c *claim) Encode() ([]byte, error) {
	en, err := buildEncNode(c.node)
	if err != nil {
		return nil, err
	}
	ee := make([]encEdge, len(c.edges))
	for i, e := range c.edges {
		ee[i], err = buildEncEdge(e)
		if err != nil {
			return nil, err
		}
	}
	data, err := encodingMode.Marshal(encClaimFile{Node: en, Edges: ee})
	if err != nil {
		return nil, fmt.Errorf("ranke: encode claim %s: %w", c.node.id.String(), err)
	}
	return data, nil
}

// DecodeClaim decodes the canonical CBOR serialization of a claim (the
// inverse of Claim.Encode) into a Claim with its id set. Exposed for
// tooling that inspects claims directly (e.g. the ranke CLI) and for
// persistence adapters — it returns an error when the bytes aren't a
// valid claim, which callers use to dispatch claim vs content.
func DecodeClaim(id Id, b []byte) (Claim, error) {
	var ec encClaimFile
	if err := cbor.Unmarshal(b, &ec); err != nil {
		return nil, fmt.Errorf("DecodeClaim: %w", err)
	}
	n, err := decodeNode(ec.Node)
	if err != nil {
		return nil, fmt.Errorf("DecodeClaim: node: %w", err)
	}
	n.id = id
	edges := make([]*edge, len(ec.Edges))
	for i, ee := range ec.Edges {
		e, err := decodeEdge(ee)
		if err != nil {
			return nil, fmt.Errorf("DecodeClaim: edge %d: %w", i, err)
		}
		if i < len(n.edges) {
			e.id = n.edges[i]
		}
		edges[i] = e
	}
	return &claim{node: n, edges: edges}, nil
}

func decodeNode(en encNode) (*node, error) {
	createdAt, err := parseRFC3339Nano(en.CreatedAt)
	if err != nil {
		return nil, err
	}
	n := &node{
		typeClass:     NodeClass(en.TypeClass),
		typeSub:       en.TypeSub,
		encodingClass: EncodingClass(en.EncodingClass),
		encodingSub:   en.EncodingSub,
		title:         en.Title,
		createdAt:     createdAt,
		fields:        en.Fields,
		pubkey:        en.Pubkey,
	}
	if len(en.ContentHash) > 0 {
		ch, err := hashFromMultihashBytes(en.ContentHash)
		if err != nil {
			return nil, err
		}
		n.contentHash = ch
		n.size = en.Size
	}
	if len(en.Edges) > 0 {
		n.edges = make([]Id, len(en.Edges))
		for i, raw := range en.Edges {
			h, err := idFromBytes(raw)
			if err != nil {
				return nil, err
			}
			n.edges[i] = h
		}
	}
	return n, nil
}

func decodeEdge(ee encEdge) (*edge, error) {
	ref, err := idFromBytes(ee.Reference)
	if err != nil {
		return nil, err
	}
	return &edge{
		reference:         ref,
		typeClass:         EdgeClass(ee.TypeClass),
		typeSub:           ee.TypeSub,
		content:           ee.Content,
		relationDirection: RelationDirection(ee.RelationDirection),
		fields:            ee.Fields,
	}, nil
}
