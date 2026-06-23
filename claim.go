// package: ranke / claim
// type:    logic
// job:     the Claim and Contributor types, the concrete claim and its methods, and the signing-key wrapper
// limits:  construction/signing live in claim_builder.go; pure helpers in claim_helpers.go; codec in claim_codec.go
package ranke

import (
	"bytes"
	"context"
	"crypto"
	"errors"
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
	// Graph materializes the claim's provenance by walking edge
	// references. For claims loaded from a Universe the walk uses
	// that Universe; in-memory claims walk only what's reachable
	// through already-wired contributor refs.
	Graph(ctx context.Context) (Graph, error)
	// Validate is the §5.10 check across the claim's provenance —
	// convenience for Graph(ctx).Validate().
	Validate(ctx context.Context) error
	// Encode returns the claim's canonical CBOR serialization — the
	// same bytes its id is derived from, storage-agnostic. Inverse of
	// the package-level DecodeClaim. Persistence adapters use it to
	// store a claim as opaque bytes.
	Encode() ([]byte, error)
}

// Contributor is a typed view over a Claim whose node type is
// "contribution/contributor". Obtain via Claim.AsContributor,
// Claim.Contributor, Branch.Contributor, or BranchEntry.Contributor.
type Contributor interface {
	Claim
	// SigningKey returns the private key matching this contributor's
	// pubkey, or nil for identity-Sign (§5.7) or unbound contributors
	// loaded from disk. Attach a key via WithSigningKey for a session.
	SigningKey() crypto.Signer
}

// claim is the concrete implementation of Claim: its node plus the full
// edge records (the node carries only edge ids). Immutable after Sign.
//
// universe is where the claim was loaded from, used by Graph to walk
// provenance; nil for in-memory-constructed claims, whose Graph returns
// only what's reachable via in-memory contributor wiring.
//
// Edge ids stay plain H(S(e)) multihashes: the claim's signed id covers
// every edge id via canonical encoding, so tampering with an edge breaks
// the claim signature that transitively authenticates it.
type claim struct {
	node        *node
	edges       []*edge // same order as node.edges
	contributor Contributor
	universe    Universe
}

// Graph materializes the claim's provenance. Universe-backed claims walk
// the full closure (every reachable claim); in-memory claims return only
// the claim and the contributor chain wired at construction — a partial
// graph, not a guaranteed closure.
func (c *claim) Graph(ctx context.Context) (Graph, error) {
	g := &graph{
		claims:     make(map[string]*claim),
		referenced: make(map[string]struct{}),
	}
	queue := []*claim{c}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cur := queue[0]
		queue = queue[1:]
		k := cur.node.id.String()
		if _, seen := g.claims[k]; seen {
			continue
		}
		g.claims[k] = cur
		for _, e := range cur.edges {
			g.referenced[e.reference.String()] = struct{}{}
			if cur.universe == nil {
				// In-memory best effort: include the resolved contributor
				// if it's this edge's target.
				if cur.contributor != nil && cur.contributor.ID() != nil &&
					cur.contributor.ID().Equal(e.reference) {
					if cc, ok := cur.contributor.(*claim); ok {
						queue = append(queue, cc)
					}
				}
				continue
			}
			next, err := loadClaimAs(ctx, cur.universe, e.reference)
			if err != nil {
				return nil, fmt.Errorf("Claim.Graph: load %s: %w", e.reference.String(), err)
			}
			queue = append(queue, next)
		}
	}
	return g, nil
}

func (c *claim) Validate(ctx context.Context) error {
	g, err := c.Graph(ctx)
	if err != nil {
		return err
	}
	return g.Validate()
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
	return c.node.typeClass == NodeContribution && c.node.typeSub == "contributor"
}

func (c *claim) AsContributor(signingKey ...crypto.Signer) (Contributor, error) {
	if !c.IsContributor() {
		return nil, fmt.Errorf("ranke.Claim.AsContributor: claim has type %s, not contribution/contributor", c.node.Type())
	}
	if len(signingKey) == 0 || signingKey[0] == nil {
		return c, nil // unwrapped — caller didn't ask to bind a key
	}
	if len(c.node.pubkey) == 0 {
		return nil, errors.New("ranke.Claim.AsContributor: signing key supplied but contributor has no pubkey (identity-Sign contributor)")
	}
	keyPubkey, err := EncodePublicKey(signingKey[0].Public())
	if err != nil {
		return nil, fmt.Errorf("ranke.Claim.AsContributor: encode signing key pubkey: %w", err)
	}
	if !bytes.Equal(keyPubkey, c.node.pubkey) {
		return nil, errors.New("ranke.Claim.AsContributor: signing key does not match contributor pubkey")
	}
	return WithSigningKey(c, signingKey[0]), nil
}

// SigningKey on a bare *claim is always nil — the claim is the persisted
// data structure and stores no private keys. Wrap with WithSigningKey
// for a session-scoped signer.
func (c *claim) SigningKey() crypto.Signer { return nil }

// WithSigningKey returns a Contributor carrying the private key matching
// c's pubkey, so subsequent NewClaim/AddGraph/Consolidate calls sign on
// the contributor's behalf without threading the key manually. Nil key
// collapses to identity-Sign. Not persisted — a runtime convenience only.
func WithSigningKey(c Contributor, key crypto.Signer) Contributor {
	return &signedContributor{contributor: c, key: key}
}

type signedContributor struct {
	contributor Contributor
	key         crypto.Signer
}

// Forward every Claim/Contributor method to the wrapped Contributor.
// Embedding the interface bare would shadow it with a same-named field —
// Go's method promotion doesn't fire on a *named* field.
func (s *signedContributor) Node() Node                     { return s.contributor.Node() }
func (s *signedContributor) Edges(filters ...Filter) []Edge { return s.contributor.Edges(filters...) }
func (s *signedContributor) Contributor() Contributor       { return s.contributor.Contributor() }
func (s *signedContributor) IsContributor() bool            { return s.contributor.IsContributor() }
func (s *signedContributor) AsContributor(key ...crypto.Signer) (Contributor, error) {
	return s.contributor.AsContributor(key...)
}
func (s *signedContributor) ID() Id { return s.contributor.ID() }
func (s *signedContributor) Graph(ctx context.Context) (Graph, error) {
	return s.contributor.Graph(ctx)
}
func (s *signedContributor) Validate(ctx context.Context) error {
	return s.contributor.Validate(ctx)
}
func (s *signedContributor) Encode() ([]byte, error)   { return s.contributor.Encode() }
func (s *signedContributor) SigningKey() crypto.Signer { return s.key }
