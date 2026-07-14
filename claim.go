// package: ranke / claim
// type:    logic
// job:     the Claim type and the concrete claim with its methods
// limits:  the Contributor extension lives in claim_type_contributor.go; construction/signing in claim_builder.go; pure helpers in claim_helpers.go; codec in codec.go
package ranke

import (
	"bytes"
	"crypto"
	"fmt"
)

// Claim is a node together with the edges in its edges set.
// Atomically created (spec §4.3); immutable after.
type Claim interface {
	Node() Node
	// Edges returns the edges in canonical order. With filters,
	// only edges matching every filter (AND) are returned.
	Edges(filters ...Filter) []Edge
	// Contributor returns the contributor for this claim — always
	// a contribution/contributor claim. Self-attribution for the
	// root (no-edge) contributor.
	Contributor() Contributor
	IsContributor() bool
	// AsContributor returns this claim as a Contributor. Errors if
	// the claim isn't of type contribution/contributor. If a signing
	// key is supplied it must match the contributor's pubkey; the
	// returned Contributor is wrapped via WithSigningKey so
	// subsequent claims attributed to it sign automatically.
	AsContributor(signingKey ...crypto.Signer) (Contributor, error)
	ID() Id
	// Encode returns the claim's canonical CBOR serialization — the
	// same bytes its id is derived from, storage-agnostic. Inverse of
	// the package-level DecodeClaim. Persistence adapters use it to
	// store a claim as opaque bytes.
	Encode() ([]byte, error)
}

type claim struct {
	node        *node
	edges       []*edge // same order as node.edges
	contributor Contributor
}

func (c *claim) Node() Node { return c.node }
func (c *claim) ID() Id     { return c.node.id }

func (c *claim) Edges(filters ...Filter) []Edge {
	if len(filters) == 0 {
		out := make([]Edge, len(c.edges))
		for i, e := range c.edges {
			out[i] = e
		}
		return out
	}
	out := make([]Edge, 0, len(c.edges))
	for _, e := range c.edges {
		if matchAll(e, filters) {
			out = append(out, e)
		}
	}
	return out
}

func (c *claim) Contributor() Contributor {
	if c.contributor == nil {
		return nil
	}
	return c.contributor
}

func (c *claim) IsContributor() bool {
	return c.node.typeClass == NodeClassContribution && NodeSubtype(c.node.typeSub) == NodeSubtypeContributor
}

func (c *claim) AsContributor(signingKey ...crypto.Signer) (Contributor, error) {
	if !c.IsContributor() {
		return nil, withDetail(errNotContributorClaim, c.node.Type())
	}
	if len(signingKey) == 0 || signingKey[0] == nil {
		return c, nil // unwrapped — caller didn't ask to bind a key
	}
	if len(c.node.pubkey) == 0 {
		return nil, errSigningKeyNoPubkey
	}
	keyPubkey, err := EncodePublicKey(signingKey[0].Public())
	if err != nil {
		return nil, fmt.Errorf("ranke.Claim.AsContributor: encode signing key pubkey: %w", err)
	}
	if !bytes.Equal(keyPubkey, c.node.pubkey) {
		return nil, errSigningKeyMismatch
	}
	return WithSigningKey(c, signingKey[0]), nil
}

func (c *claim) IsBranch() bool {
	return c.node.typeClass == NodeClassContribution && NodeSubtype(c.node.typeSub) == NodeSubtypeBranch
}

// AsBranch views this claim as a Branch scoped to u — the Universe its
// closure is read from (handed over explicitly; the claim itself holds no
// storage reference).
func (c *claim) AsBranch(u Universe) (Branch, error) {
	if !c.IsBranch() {
		return nil, errNotBranch
	}
	return &branch{claim: c, u: u}, nil
}
