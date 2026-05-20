package ranke

import (
	"bytes"
	"context"
	"crypto"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Graph materializes the claim's provenance. For claims loaded from
// a Universe, walks via that Universe. For in-memory-constructed
// claims (no Universe binding) the result contains only the claim
// itself plus any contributor chain wired during construction.
func (c *claim) Graph(ctx context.Context) (Graph, error) {
	g := &graph{
		claims:     make(map[string]*claim),
		referenced: make(map[string]struct{}),
	}
	queue := []*claim{c}
	for len(queue) > 0 {
		_ = ctx // walked below via loadClaimAs
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
				// Best effort for in-memory claims: include the
				// resolved contributor if it's the edge target.
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

// ClaimBuilder is the data-only input to ClaimBuilder{...}.Sign().
//
// Required: a Type (either Type or TypeClass+TypeSub). Content and
// ContentHash are mutually exclusive: set Content to have build
// hash the bytes for you, or set ContentHash directly when the
// content lives outside the graph. CreatedAt defaults to
// time.Now().UTC() when zero. Contributor is required for every
// claim except the root contribution/contributor claim — see §4.3.
type ClaimBuilder struct {
	Type          string
	TypeClass     NodeClass
	TypeSub       string
	Encoding      string
	EncodingClass EncodingClass
	EncodingSub   string
	Title         string
	Content       []byte
	ContentHash   Id
	CreatedAt     time.Time
	Contributor   Contributor
	Edges         []Edge
	Fields        map[string]string
	// Pubkey is the multikey-encoded public key for a contributor
	// claim (§4.1, §5.7). Empty for non-contributor claims and for
	// unsigned contributors (identity-Sign case).
	Pubkey []byte
	// SigningKey is the private key used to sign this claim's id.
	// Optional; nil + empty resolved pubkey = identity Sign per §5.7.
	SigningKey crypto.Signer
}

// claim is the concrete implementation of Claim. A claim is its node
// together with the full edge records (the node only carries edge ids).
// Atomically created at Sign; immutable after.
//
// universe is the Universe the claim was loaded from, used by
// Claim.Graph() to walk the provenance. nil for claims constructed
// in-memory (via ClaimBuilder.Sign()) — their .Graph() returns just
// what's reachable via in-memory contributor wiring.
type claim struct {
	node        *node
	edges       []*edge // same order as node.edges
	contributor Contributor
	universe    Universe
}

// Edge ids stay as plain H(S(e)) multihashes regardless of signing.
// The claim's signed id covers every edge id via canonical
// serialization (encNode.Edges), so tampering with any edge changes
// the claim's hash input and breaks its signature. The paper's
// "id(e) = Sign(H(S(e)))" reduces to this in the identity-Sign case
// and is functionally equivalent in the signed case: the claim's
// signature transitively authenticates the edges it owns.

// NewClaim seeds a ClaimBuilder with the three most common required
// fields. Chain With* setters to add optionals, then call .Sign()
// to finalize:
//
//	c, err := ranke.NewClaim("source/email", alice, body).
//	    WithEncoding(ranke.EncodingMessageRFC822).
//	    WithCreatedAt(at).
//	    Sign()
//
// For full struct-literal control, build a ClaimBuilder directly
// and call .Sign() on it.
func NewClaim(typ string, contributor Contributor, content []byte) ClaimBuilder {
	return ClaimBuilder{
		Type:        typ,
		Contributor: contributor,
		Content:     content,
	}
}

// buildClaim constructs a Claim atomically per §4.3 from a fully
// populated ClaimBuilder. Internal — callers reach it via
// ClaimBuilder.Sign() or via NewClaim(...).WithX().Sign().
//
//   - validates type, encoding, content/contenthash exclusion
//   - rejects derivation/, entity/, relation/* nodes that lack a
//     derivation/* edge (provenance invariant, §3.5)
//   - auto-builds the contribution/contributor edge from
//     cfg.Contributor (omitted only for the root contribution/contributor
//     claim, which self-attributes per §4.3)
//   - sorts edges canonically (by id bytes), computes the node id
//     as Sign(H(canonical(node)))
//
// The returned Claim is immutable.
func buildClaim(cfg ClaimBuilder) (Claim, error) {
	// Type takes precedence over the split TypeClass + TypeSub form
	// — see ClaimConfig docs.
	if cfg.Type != "" {
		class, sub, err := splitType(cfg.Type)
		if err != nil {
			return nil, fmt.Errorf("ranke.NewClaim: Type: %w", err)
		}
		cfg.TypeClass = NodeClass(class)
		cfg.TypeSub = sub
	}
	if cfg.TypeClass == "" || cfg.TypeSub == "" {
		return nil, errors.New("ranke.NewClaim: Type (or TypeClass + TypeSub) is required")
	}
	if !validNodeClass(cfg.TypeClass) {
		return nil, fmt.Errorf("ranke.NewClaim: unknown NodeClass %q", cfg.TypeClass)
	}
	if cfg.Encoding != "" {
		class, sub, err := splitType(cfg.Encoding)
		if err != nil {
			return nil, fmt.Errorf("ranke.NewClaim: Encoding: %w", err)
		}
		cfg.EncodingClass = EncodingClass(class)
		cfg.EncodingSub = sub
	}
	// Encoding is required per §4.1 — fall back to text/plain so
	// scenarios don't have to spell it out on every structural
	// claim. Explicit Encoding always wins.
	if cfg.EncodingClass == "" {
		cfg.EncodingClass = encText
		cfg.EncodingSub = "plain"
	}
	if !validEncodingClass(cfg.EncodingClass) {
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
		title:         cfg.Title,
		createdAt:     createdAt,
		fields:        cloneFields(cfg.Fields),
		content:       cfg.Content,
		pubkey:        cfg.Pubkey,
	}

	// Resolve content hash + size. Size always tracks len(content); if
	// only ContentHash was supplied (no Content), size is 0.
	if cfg.ContentHash != nil {
		n.contentHash = cfg.ContentHash
		n.size = uint64(len(cfg.Content))
	} else if cfg.Content != nil {
		ch, err := hashContent(cfg.Content)
		if err != nil {
			return nil, fmt.Errorf("ranke.NewClaim: content hash: %w", err)
		}
		n.contentHash = ch
		n.size = uint64(len(cfg.Content))
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

// SigningKey on a bare *claim is always nil — the claim type is the
// persisted data structure and stores no private keys. Wrap with
// WithSigningKey for a session-scoped signer.
func (c *claim) SigningKey() crypto.Signer { return nil }

// --- ClaimBuilder fluent API ---

// Sign finalizes a ClaimBuilder into an immutable Claim. Optional
// variadic key overrides ClaimBuilder.SigningKey; both are
// alternatives to letting Sign pull the key from the Contributor's
// wrapped signer (see WithSigningKey). Empty resolved pubkey
// collapses to identity Sign per §5.7.
func (b ClaimBuilder) Sign(signingKey ...crypto.Signer) (Claim, error) {
	if len(signingKey) > 0 && signingKey[0] != nil {
		b.SigningKey = signingKey[0]
	}
	return buildClaim(b)
}

// --- ClaimBuilder chained setters ---
//
// Each returns the builder by value so calls chain cleanly. Use
// the struct literal form when you want every field at once;
// chain when you want to layer optionals onto NewBuilder.

func (b ClaimBuilder) WithType(t string) ClaimBuilder             { b.Type = t; return b }
func (b ClaimBuilder) WithEncoding(e string) ClaimBuilder         { b.Encoding = e; return b }
func (b ClaimBuilder) WithTitle(t string) ClaimBuilder            { b.Title = t; return b }
func (b ClaimBuilder) WithContent(c []byte) ClaimBuilder          { b.Content = c; return b }
func (b ClaimBuilder) WithContentHash(h Id) ClaimBuilder          { b.ContentHash = h; return b }
func (b ClaimBuilder) WithCreatedAt(t time.Time) ClaimBuilder     { b.CreatedAt = t; return b }
func (b ClaimBuilder) WithContributor(c Contributor) ClaimBuilder { b.Contributor = c; return b }
func (b ClaimBuilder) WithPubkey(p []byte) ClaimBuilder           { b.Pubkey = p; return b }
func (b ClaimBuilder) WithSigningKey(k crypto.Signer) ClaimBuilder { b.SigningKey = k; return b }

// WithEdges appends the given edges to the builder's Edges slice.
// Pass once with all edges, or chain multiple WithEdges calls.
func (b ClaimBuilder) WithEdges(edges ...Edge) ClaimBuilder {
	b.Edges = append(b.Edges, edges...)
	return b
}

// WithField sets one additional implementation-defined field on
// the node (§4.1). Copies the existing Fields map so the receiver
// stays unchanged.
func (b ClaimBuilder) WithField(key, value string) ClaimBuilder {
	f := make(map[string]string, len(b.Fields)+1)
	for k, v := range b.Fields {
		f[k] = v
	}
	f[key] = value
	b.Fields = f
	return b
}

// --- helpers ---

// splitType parses a "class/sub" string into its two segments. Both
// parts must be non-empty; the separator is a single "/".
func splitType(s string) (class, sub string, err error) {
	if s == "" {
		return "", "", errors.New("empty")
	}
	slash := -1
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			slash = i
			break
		}
	}
	if slash <= 0 || slash == len(s)-1 {
		return "", "", fmt.Errorf("expected \"class/sub\", got %q", s)
	}
	return s[:slash], s[slash+1:], nil
}

// resolveSigningPubkey returns the multikey-encoded pubkey whose
// matching private key must produce this claim's signature. For the
// initial (root) contributor, the pubkey is set on this very claim
// via cfg.Pubkey. For every other claim, the pubkey is read from
// the contributor referenced by cfg.Contributor.
func resolveSigningPubkey(isRootContributor bool, cfg ClaimBuilder) ([]byte, error) {
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
// matching c's pubkey, so subsequent NewClaim/AddGraph/Consolidate
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
func (s *signedContributor) AsContributor(key ...crypto.Signer) (Contributor, error) {
	return s.contributor.AsContributor(key...)
}
func (s *signedContributor) ID() Id                              { return s.contributor.ID() }
func (s *signedContributor) Graph(ctx context.Context) (Graph, error) {
	return s.contributor.Graph(ctx)
}
func (s *signedContributor) Validate(ctx context.Context) error {
	return s.contributor.Validate(ctx)
}
func (s *signedContributor) SigningKey() crypto.Signer { return s.key }

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
