// package: ranke / claim
// type:    logic
// job:     the Claim type and the concrete claim with its methods
// limits:  the Contributor extension lives in claim_type_contributor.go; construction/signing in claim_builder.go; pure helpers in claim_helpers.go; codec in codec.go
package ranke

import (
	"bytes"
	"context"
	"crypto"
	"io"
	"sort"
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
	// AsContributor returns this claim as a Contributor. Errors if the
	// claim isn't of type contribution/contributor. It resolves the
	// contributor's pubkey once — transparently, inline or external via u
	// (nil is fine for inline) — and caches it on the returned Contributor,
	// so later signing can check keys without a Universe. If a signing key
	// is supplied it must match that pubkey; the returned Contributor then
	// signs subsequent claims attributed to it automatically.
	AsContributor(ctx context.Context, u Universe, signingKey ...crypto.Signer) (Contributor, error)
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

	// Diff materialisation (set by the loader when this claim carries a
	// contribution/diff edge): diffClaim is the materialised predecessor;
	// diffEdges is the merged edge view (predecessor's named edges, minus
	// edges_diff_omit by name, overlaid with self's named edges, plus
	// self's own unnamed singletons). The delta (edges) is untouched so
	// ID()/Encode() stay the claim's own bytes.
	diffClaim *claim
	diffEdges []*edge
}

func (c *claim) Node() Node { return c.node }
func (c *claim) ID() Id     { return c.node.id }

// effectiveEdges is the edge set reads see: the delta for a plain claim,
// the materialised overlay for a diff claim.
func (c *claim) effectiveEdges() []*edge {
	if c.diffClaim == nil {
		return c.edges
	}
	return c.diffEdges
}

func (c *claim) Edges(filters ...Filter) []Edge {
	src := c.effectiveEdges()
	if len(filters) == 0 {
		out := make([]Edge, len(src))
		for i, e := range src {
			out[i] = e
		}
		return out
	}
	out := make([]Edge, 0, len(src))
	for _, e := range src {
		if matchAll(e, filters) {
			out = append(out, e)
		}
	}
	return out
}

// Diff materialisation (setting diffClaim / diffEdges / node.diffFields)
// lives in materialize.go — DefaultMaterialize, applied by the read path.

// computeDiffEdges builds diffEdges: inherit the predecessor's named
// edges, drop those named in edges_diff_omit, overlay self's named edges
// (by name), and append self's own unnamed edges (the per-claim singletons
// — contributor and the diff marker — never inherited). Diff is keyed by
// name; edge ids play no part.
func (c *claim) computeDiffEdges() {
	named := map[string]*edge{}
	for _, e := range c.diffClaim.effectiveEdges() {
		if n, ok := e.fields[FieldName]; ok {
			named[n] = e // only named edges inherit
		}
	}
	for name := range splitLines(c.node.fields[FieldEdgesDiffOmit]) {
		delete(named, name)
	}
	out := make([]*edge, 0, len(named)+len(c.edges))
	for _, e := range c.edges {
		if n, ok := e.fields[FieldName]; ok {
			named[n] = e // overwrite / add by name
		} else {
			out = append(out, e) // self's own singleton (contributor, diff)
		}
	}
	for _, e := range named {
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return bytes.Compare(idBytes(out[i].id), idBytes(out[j].id)) < 0
	})
	c.diffEdges = out
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

func (c *claim) AsContributor(ctx context.Context, u Universe, signingKey ...crypto.Signer) (Contributor, error) {
	if !c.IsContributor() {
		return nil, withDetail(errNotContributorClaim, c.node.Type())
	}
	// Resolve the pubkey once, transparently — inline is served from the
	// node, external is streamed from u (§5.7). Cached on the wrapper so
	// signing later needs no Universe.
	rdr, err := c.node.GetContent(ctx, u)
	if err != nil {
		return nil, wrap(errResolveContributorPubkey, err)
	}
	pubkey, err := io.ReadAll(rdr)
	if err != nil {
		return nil, wrap(errResolveContributorPubkey, err)
	}
	var key crypto.Signer
	if len(signingKey) > 0 {
		key = signingKey[0]
	}
	if key != nil {
		if len(pubkey) == 0 {
			return nil, errSigningKeyNoPubkey
		}
		keyPubkey, err := EncodePublicKey(key.Public())
		if err != nil {
			return nil, wrap(errEncodeSigningKey, err)
		}
		if !bytes.Equal(keyPubkey, pubkey) {
			return nil, errSigningKeyMismatch
		}
	}
	return &signedContributor{contributor: c, key: key, pubkey: pubkey}, nil
}
