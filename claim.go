// package: ranke / claim
// type:    logic
// job:     the Claim type and the concrete claim with its methods
// limits:  the Contributor extension lives in claim_type_contributor.go;
// construction/signing in claim_builder.go; helpers in claim_helpers.go; codec in codec.go
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
	// Edges returns the edges in canonical order, keeping those every filter matches (AND).
	Edges(filters ...Filter) []Edge

	Tags() map[string]string
	// Tag returns value of tag best effort, with "" as fallback
	Tag(key string) string
	HasTag(key string) bool
	SetTag(ctx context.Context, u Universe) error

	// Contributor returns this claim's contribution/contributor claim, which for
	// the root (no-edge) contributor is itself.
	Contributor() Contributor
	IsContributor() bool
	// AsContributor returns a contribution/contributor claim as a Contributor,
	// caching its pubkey so later signing needs no Universe.
	AsContributor(ctx context.Context, u Universe, signingKey ...crypto.Signer) (Contributor, error)
	ID() Id

	// GetContent reads the claim's content unscoped: inline from the claim,
	// external streamed from u, as is a dropped inline body.
	GetContent(ctx context.Context, u Universe) (io.Reader, error)

	// EncodeJSON renders every record slot as text, content base64. It reports a
	// claim; the id is verified against the CBOR form's bytes.
	EncodeJSON(form Form) ([]byte, error)
	// EncodeCBOR returns the claim as canonical CBOR: FormOriginal the record as
	// written, which persistence stores; FormMaterialized its overlay resolved.
	EncodeCBOR(form Form) ([]byte, error)
	// verifyID checks the claim's id is a valid signature by pubkey over
	// H(S(node)), preimaged from the caller's stored CBOR, never a re-encoding.
	verifyID(pubkey, raw []byte) error
	// unwrap returns the underlying concrete *claim, peeling any wrapper, so
	// in-package machinery reaches it without a cast.
	unwrap() *claim
}

type claim struct {
	node        *node
	edges       []*edge // same order as node.edges
	contributor Contributor

	// raw is the stored record this claim was decoded from — the bytes its id was
	// signed over. Nil for a claim built in memory or from a lossy projection.
	raw []byte

	// Diff materialisation, set by the loader: the materialised predecessor and
	// merged edge view. The delta stays intact, so ID()/Encode() hold.
	diffClaim *claim
	diffEdges []*edge

	// tags is the runtime tag overlay a tag-aware Universe injects on the way out
	// of GetClaims, outside the id and the encoded bytes.
	tags map[string]string
}

func (c *claim) Node() Node     { return c.node }
func (c *claim) ID() Id         { return c.node.id }
func (c *claim) unwrap() *claim { return c }

func (c *claim) GetContent(ctx context.Context, u Universe) (io.Reader, error) {
	return c.node.GetContent(ctx, u)
}

func (c *claim) Tags() map[string]string {
	if c.tags == nil {
		c.tags = map[string]string{}
	}
	return c.tags
}

func (c *claim) Tag(key string) string { return c.tags[key] }

func (c *claim) HasTag(key string) bool {
	_, ok := c.tags[key]
	return ok
}

func (c *claim) SetTag(ctx context.Context, u Universe) error {
	return u.SetClaimsTags(ctx, nil, map[string]map[string]string{c.ID().String(): c.tags})
}

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

// computeDiffEdges builds diffEdges keyed by name (`V-DIFFEDGE`): inherit the
// predecessor's named edges, drop edges_diff_omit, overlay self's named, append
// self's unnamed singletons.
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
		return nil, WithDetail(errNotContributorClaim, c.node.Type())
	}
	// Resolved once — inline from the node, external streamed from u (§5.7) — and cached.
	rdr, err := c.node.GetContent(ctx, u)
	if err != nil {
		return nil, Wrap(errResolveContributorPubkey, err)
	}
	pubkey, err := io.ReadAll(rdr)
	if err != nil {
		return nil, Wrap(errResolveContributorPubkey, err)
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
			return nil, Wrap(errEncodeSigningKey, err)
		}
		if !bytes.Equal(keyPubkey, pubkey) {
			return nil, errSigningKeyMismatch
		}
	}
	return &signedContributor{contributor: c, key: key, pubkey: pubkey}, nil
}
