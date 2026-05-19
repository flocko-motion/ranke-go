package ranke

import (
	"bytes"
	"crypto"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Edge ids stay as plain H(S(e)) multihashes regardless of signing.
// The claim's signed id covers every edge id via canonical
// serialization (encNode.Edges), so tampering with any edge changes
// the claim's hash input and breaks its signature. The paper's
// "id(e) = Sign(H(S(e)))" reduces to this in the identity-Sign case
// and is functionally equivalent in the signed case: the claim's
// signature transitively authenticates the edges it owns.

// NewClaim constructs a Claim atomically per §4.3:
//
//   - validates type, encoding, content/contenthash exclusion
//   - rejects derivation/, entity/, relation/* nodes that lack a
//     derivation/* edge (provenance invariant, §3.5)
//   - auto-builds the contribution/contributor edge from
//     cfg.Contributor (omitted only for the root contribution/contributor
//     claim, which self-attributes per §4.3)
//   - sorts edges canonically (by id bytes), computes the node id
//     as H(canonical(node))
//
// The returned Claim is immutable.
func NewClaim(cfg ClaimConfig) (Claim, error) {
	if cfg.TypeClass == "" || cfg.TypeSub == "" {
		return nil, errors.New("ranke.NewClaim: TypeClass and TypeSub are required")
	}
	if !validNodeClass(cfg.TypeClass) {
		return nil, fmt.Errorf("ranke.NewClaim: unknown NodeClass %q", cfg.TypeClass)
	}
	if cfg.EncodingClass != "" && !validEncodingClass(cfg.EncodingClass) {
		return nil, fmt.Errorf("ranke.NewClaim: unknown EncodingClass %q", cfg.EncodingClass)
	}
	if cfg.Content != nil && cfg.ContentHash != nil {
		return nil, errors.New("ranke.NewClaim: Content and ContentHash are mutually exclusive")
	}

	isRootContributor := cfg.TypeClass == NodeContribution &&
		cfg.TypeSub == "contributor" &&
		cfg.Contributor == nil

	// Every non-root claim needs a contributor.
	if !isRootContributor && cfg.Contributor == nil {
		return nil, errors.New("ranke.NewClaim: Contributor is required (only the root contribution/contributor may omit it)")
	}

	// Collect the edges. NewClaim auto-builds the
	// contribution/contributor edge unless this is the root.
	edges := make([]*edge, 0, len(cfg.Edges)+1)
	for _, e := range cfg.Edges {
		ce, err := asConcreteEdge(e)
		if err != nil {
			return nil, fmt.Errorf("ranke.NewClaim: %w", err)
		}
		edges = append(edges, ce)
	}
	if !isRootContributor {
		ce, err := buildContributorEdge(cfg.Contributor)
		if err != nil {
			return nil, fmt.Errorf("ranke.NewClaim: build contribution/contributor edge: %w", err)
		}
		edges = append(edges, ce)
	}

	// Provenance invariant (§3.5): derivation/, entity/, and
	// relation/ claims must carry at least one derivation/* edge.
	if requiresProvenance(cfg.TypeClass) {
		if !hasDerivationEdge(edges) {
			return nil, fmt.Errorf("ranke.NewClaim: %s/%s claims must carry at least one derivation/* edge (§3.5 provenance invariant)", cfg.TypeClass, cfg.TypeSub)
		}
	}

	// Canonical edge order: by raw multihash bytes.
	sort.SliceStable(edges, func(i, j int) bool {
		return bytes.Compare(idBytes(edges[i].id), idBytes(edges[j].id)) < 0
	})

	createdAt := cfg.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	} else {
		createdAt = createdAt.UTC()
	}

	n := &node{
		typeClass:     cfg.TypeClass,
		typeSub:       cfg.TypeSub,
		encodingClass: cfg.EncodingClass,
		encodingSub:   cfg.EncodingSub,
		createdAt:     createdAt,
		fields:        cloneFields(cfg.Fields),
		content:       cfg.Content,
		pubkey:        cfg.Pubkey,
	}

	// Resolve content hash.
	if cfg.ContentHash != nil {
		n.contentHash = cfg.ContentHash
	} else if cfg.Content != nil {
		ch, err := hashContent(cfg.Content)
		if err != nil {
			return nil, fmt.Errorf("ranke.NewClaim: content hash: %w", err)
		}
		n.contentHash = ch
	}

	// Edge ids on the node, in sorted order.
	n.edges = make([]Id, len(edges))
	for i, e := range edges {
		n.edges[i] = e.id
	}

	// Resolve the pubkey that this claim's signature must match
	// (paper §4.1, §5.7). For the initial contributor (no
	// contribution/contributor edge), the pubkey is on this node
	// itself. For every other claim, it is on the referenced
	// contributor's node.
	resolvedPubkey, err := resolveSigningPubkey(isRootContributor, cfg)
	if err != nil {
		return nil, fmt.Errorf("ranke.NewClaim: %w", err)
	}
	// If the caller didn't pass an explicit SigningKey, fall back
	// to the one the Contributor carries. A bare contributor
	// (unwrapped, or freshly loaded from disk) returns nil here,
	// which collapses to identity Sign per §5.7.
	if cfg.SigningKey == nil && cfg.Contributor != nil {
		cfg.SigningKey = cfg.Contributor.SigningKey()
	}
	if err := checkSigningConsistency(cfg.SigningKey, resolvedPubkey); err != nil {
		return nil, fmt.Errorf("ranke.NewClaim: %w", err)
	}

	// Compute the node id: Sign(H(S(node))). For the identity-Sign
	// case (no signing key, empty pubkey) signHash returns the hash
	// bytes unchanged, so id is just the multihash — backwards
	// compatible with unsigned graphs.
	encoded, err := encodeNode(n)
	if err != nil {
		return nil, fmt.Errorf("ranke.NewClaim: canonical encode: %w", err)
	}
	hash, err := hashContent(encoded)
	if err != nil {
		return nil, fmt.Errorf("ranke.NewClaim: hash: %w", err)
	}
	idPayload, err := signHash(cfg.SigningKey, hash.raw)
	if err != nil {
		return nil, fmt.Errorf("ranke.NewClaim: sign: %w", err)
	}
	id, err := idFromBytes(idPayload)
	if err != nil {
		return nil, fmt.Errorf("ranke.NewClaim: wrap id: %w", err)
	}
	n.id = id

	c := &claim{
		node:  n,
		edges: edges,
	}

	// Wire the contributor: self for root, else the supplied contributor.
	if isRootContributor {
		c.contributor = c // self-attribute
	} else {
		c.contributor = cfg.Contributor
	}
	return c, nil
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

func matchAll(e Edge, filters []Filter) bool {
	for _, f := range filters {
		if !f.Match(e) {
			return false
		}
	}
	return true
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

func (c *claim) AsContributor() (Contributor, error) {
	if !c.IsContributor() {
		return nil, fmt.Errorf("ranke.Claim.AsContributor: claim has type %s, not contribution/contributor", c.node.Type())
	}
	return c, nil
}

// SigningKey on a bare *claim is always nil — the claim type is the
// persisted data structure and stores no private keys. Wrap with
// WithSigningKey for a session-scoped signer.
func (c *claim) SigningKey() crypto.Signer { return nil }

// --- helpers ---

// resolveSigningPubkey returns the multikey-encoded pubkey whose
// matching private key must produce this claim's signature. For the
// initial (root) contributor, the pubkey is set on this very claim
// via cfg.Pubkey. For every other claim, the pubkey is read from
// the contributor referenced by cfg.Contributor.
func resolveSigningPubkey(isRootContributor bool, cfg ClaimConfig) ([]byte, error) {
	if isRootContributor {
		return cfg.Pubkey, nil
	}
	if cfg.Contributor == nil {
		return nil, errors.New("missing contributor")
	}
	return cfg.Contributor.Node().Pubkey(), nil
}

// checkSigningConsistency rejects mismatches between the supplied
// SigningKey and the resolved contributor pubkey:
//   - signing key set but resolved pubkey empty → caller wants to
//     sign for a contributor that has no key on record
//   - signing key nil but resolved pubkey non-empty → caller didn't
//     supply the key the contributor requires
//   - both set: signing key's public part must match the resolved
//     pubkey (prevents Bob's private key from signing claims whose
//     contributor field names Alice)
func checkSigningConsistency(signingKey interface{ Public() crypto.PublicKey }, resolvedPubkey []byte) error {
	hasSigner := signingKey != nil && !isTypedNil(signingKey)
	hasPubkey := len(resolvedPubkey) > 0
	switch {
	case !hasSigner && !hasPubkey:
		return nil // identity-Sign case
	case hasSigner && !hasPubkey:
		return errors.New("SigningKey supplied but resolved contributor has no pubkey")
	case !hasSigner && hasPubkey:
		return errors.New("resolved contributor has a pubkey but no SigningKey was supplied")
	}
	encoded, err := EncodePublicKey(signingKey.Public())
	if err != nil {
		return fmt.Errorf("encode signing key's pubkey: %w", err)
	}
	if !bytes.Equal(encoded, resolvedPubkey) {
		return errors.New("SigningKey's public key does not match the resolved contributor pubkey")
	}
	return nil
}

// isTypedNil reports whether i is a nil interface value or wraps a
// nil concrete pointer. A nil ed25519.PrivateKey passed as crypto.Signer
// would otherwise sneak past `signingKey != nil`.
func isTypedNil(i any) bool {
	if i == nil {
		return true
	}
	// crypto.Signer concrete types are usually slices ([]byte for
	// ed25519); nil slice is a valid empty value here.
	if s, ok := i.(interface{ Public() crypto.PublicKey }); ok {
		// Calling Public on a nil ed25519.PrivateKey panics, so
		// inspect via reflection-free duck typing: a usable signer
		// must return a non-nil pubkey.
		defer func() { _ = recover() }()
		if s.Public() == nil {
			return true
		}
	}
	return false
}

// WithSigningKey returns a Contributor that carries the private key
// matching c's pubkey, so subsequent NewClaim/SetBranch/Consolidate
// calls can sign on the contributor's behalf without the caller
// threading the key through manually. Nil key collapses to the
// identity-Sign behaviour the bare contributor already exposes.
//
// The Ranke-Graph itself stores no private keys; this wrapper is a
// runtime convenience, not persisted to disk.
func WithSigningKey(c Contributor, key crypto.Signer) Contributor {
	return &signedContributor{contributor: c, key: key}
}

type signedContributor struct {
	contributor Contributor
	key         crypto.Signer
}

// Forward every Claim/Contributor method to the wrapped Contributor.
// Embedding the interface bare would shadow it with a same-named
// field — Go's method promotion doesn't fire on a *named* field.
func (s *signedContributor) Node() Node                          { return s.contributor.Node() }
func (s *signedContributor) Edges(filters ...Filter) []Edge      { return s.contributor.Edges(filters...) }
func (s *signedContributor) Contributor() Contributor            { return s.contributor.Contributor() }
func (s *signedContributor) IsContributor() bool                 { return s.contributor.IsContributor() }
func (s *signedContributor) AsContributor() (Contributor, error) { return s.contributor.AsContributor() }
func (s *signedContributor) ID() Id                              { return s.contributor.ID() }
func (s *signedContributor) SigningKey() crypto.Signer           { return s.key }

// requiresProvenance reports whether claims of this class need at
// least one derivation/* edge per §3.5.
func requiresProvenance(c NodeClass) bool {
	switch c {
	case NodeDerivation, NodeEntity, NodeRelation:
		return true
	}
	return false
}

func hasDerivationEdge(edges []*edge) bool {
	for _, e := range edges {
		if e.typeClass == EdgeDerivation {
			return true
		}
	}
	return false
}

// asConcreteEdge unwraps an Edge interface into our concrete *edge
// type. We only know how to handle our own implementation.
func asConcreteEdge(e Edge) (*edge, error) {
	if e == nil {
		return nil, errors.New("nil edge")
	}
	ce, ok := e.(*edge)
	if !ok {
		return nil, errors.New("edge from foreign implementation")
	}
	return ce, nil
}

// buildContributorEdge constructs the contribution/contributor edge
// referencing the given contributor. NewClaim calls this when the
// caller doesn't supply it explicitly.
func buildContributorEdge(c Contributor) (*edge, error) {
	if c == nil {
		return nil, errors.New("nil contributor")
	}
	e, err := NewEdge(EdgeConfig{
		Reference: c.ID(),
		TypeClass: EdgeContribution,
		TypeSub:   "contributor",
	})
	if err != nil {
		return nil, err
	}
	return e.(*edge), nil
}
