// package: ranke / claim
// type:    logic
// job:     ClaimBuilder — assembles, validates, attributes, and signs a claim into an immutable Claim
// limits:  pure construction helpers live in claim_helpers.go; signing primitives in sign.go
package ranke

import (
	"bytes"
	"crypto"
	"sort"
	"time"
)

// ClaimBuilder is the data-only input to ClaimBuilder{...}.Sign().
//
// Required: a Type (either Type or TypeClass+TypeSub). Content is
// optional — a claim may carry none (structural claims: heads,
// branches, contributors). InlineContent holds the bytes directly;
// ContentHash+ContentSize instead reference external content stored
// elsewhere. InlineContent and ContentHash are mutually exclusive —
// set at most one. Encoding applies only when there is content.
// CreatedAt defaults to time.Now().UTC() when zero. Contributor is
// required for every claim except the root contribution/contributor
// claim — see §4.3.
type ClaimBuilder struct {
	Type          string
	TypeClass     NodeClass
	TypeSub       string
	Encoding      string
	EncodingClass EncodingClass
	EncodingSub   string
	InlineContent []byte
	ContentHash   Id
	ContentSize   uint64
	CreatedAt     time.Time
	Contributor   Contributor
	// DiffOf, when set, makes this claim a diff over the referenced
	// predecessor: NewClaim adds a contribution/diff edge to it. The claim
	// restates only what differs; the full claim is materialised by
	// applying the diff chain (done transparently by the loader).
	DiffOf Id
	Edges  []Edge
	Fields map[string]string
	// SigningKey is the private key used to sign this claim's id.
	// Optional; nil + empty resolved pubkey = identity Sign per §5.7.
	// A contributor claim carries its pubkey as its content (§5.7), so
	// there is no separate pubkey input — set InlineContent to the
	// multikey-encoded public key.
	SigningKey crypto.Signer
}

// NewClaim seeds a ClaimBuilder with the two required fields: the type
// and the attributing contributor. Content is optional — add it (or
// not) via WithInlineContent / WithExternalContent. Chain other With*
// setters for optionals, then call .Sign() to finalize:
//
//	c, err := ranke.NewClaim("source/email", alice).
//	    WithInlineContent(body).
//	    WithEncoding(ranke.EncodingMessage("rfc822")).
//	    WithCreatedAt(at).
//	    Sign()
//
// For full struct-literal control, build a ClaimBuilder directly
// and call .Sign() on it.
func NewClaim(typ string, contributor Contributor) ClaimBuilder {
	return ClaimBuilder{
		Type:        typ,
		Contributor: contributor,
	}
}

// Sign finalizes a ClaimBuilder into an immutable Claim. An optional
// variadic key overrides ClaimBuilder.SigningKey; both are alternatives
// to letting Sign pull the key from the Contributor's wrapped signer
// (see WithSigningKey). Empty resolved pubkey collapses to identity
// Sign per §5.7.
func (b ClaimBuilder) Sign(signingKey ...crypto.Signer) (Claim, error) {
	if len(signingKey) > 0 && signingKey[0] != nil {
		b.SigningKey = signingKey[0]
	}
	return buildClaim(b)
}

// --- chained setters ---
//
// Each returns the builder by value so calls chain cleanly, an
// alternative to the struct-literal form. All are public API.

// WithType sets the claim type ("class/sub").
func (b ClaimBuilder) WithType(t string) ClaimBuilder { b.Type = t; return b }

// WithEncoding sets the content media type ("class/sub").
func (b ClaimBuilder) WithEncoding(e string) ClaimBuilder { b.Encoding = e; return b }

// WithDiff makes this claim a diff over the predecessor at id (adds a
// contribution/diff edge). The claim restates only what differs.
func (b ClaimBuilder) WithDiff(id Id) ClaimBuilder { b.DiffOf = id; return b }

// WithInlineContent sets the inline content bytes (the claim carries the
// content itself). Mutually exclusive with WithExternalContent.
func (b ClaimBuilder) WithInlineContent(c []byte) ClaimBuilder { b.InlineContent = c; return b }

// WithExternalContent references content stored elsewhere by its hash and
// byte size (the claim carries only the reference). Mutually exclusive
// with WithInlineContent.
func (b ClaimBuilder) WithExternalContent(hash Id, size uint64) ClaimBuilder {
	b.ContentHash = hash
	b.ContentSize = size
	return b
}

// WithCreatedAt sets the creation timestamp.
func (b ClaimBuilder) WithCreatedAt(t time.Time) ClaimBuilder { b.CreatedAt = t; return b }

// WithContributor sets the attributing contributor.
func (b ClaimBuilder) WithContributor(c Contributor) ClaimBuilder { b.Contributor = c; return b }

// WithSigningKey sets the key used to sign the claim id.
func (b ClaimBuilder) WithSigningKey(k crypto.Signer) ClaimBuilder { b.SigningKey = k; return b }

// WithEdges appends the given edges to the builder's Edges slice.
func (b ClaimBuilder) WithEdges(edges ...Edge) ClaimBuilder {
	b.Edges = append(b.Edges, edges...)
	return b
}

// WithField sets one implementation-defined node field (§4.1),
// copying the existing map so the receiver stays unchanged.
func (b ClaimBuilder) WithField(key, value string) ClaimBuilder {
	f := make(map[string]string, len(b.Fields)+1)
	for k, v := range b.Fields {
		f[k] = v
	}
	f[key] = value
	b.Fields = f
	return b
}

// buildClaim constructs a Claim atomically per §4.3. It reads as the
// pipeline it is: resolve the type, content mode, and encoding; assemble and
// validate the edge set (contributor, diff, invariants); build the node; then
// sign it into an id. Each step is a helper below.
func buildClaim(cfg ClaimBuilder) (Claim, error) {
	if err := resolveType(&cfg); err != nil {
		return nil, err
	}
	hasInline, hasExternal, err := resolveContentState(&cfg)
	if err != nil {
		return nil, err
	}
	if err := resolveEncoding(&cfg, hasInline || hasExternal); err != nil {
		return nil, err
	}

	isRootContributor := cfg.TypeClass == NodeClassContribution &&
		cfg.TypeSub == "contributor" &&
		cfg.Contributor == nil
	if !isRootContributor && cfg.Contributor == nil {
		return nil, errClaimContributorRequired
	}

	edges, err := assembleEdges(cfg, isRootContributor)
	if err != nil {
		return nil, err
	}

	for k := range cfg.Fields {
		if err := checkUserFieldName(k); err != nil {
			return nil, err
		}
	}

	n := &node{
		typeClass:     cfg.TypeClass,
		typeSub:       cfg.TypeSub,
		encodingClass: cfg.EncodingClass,
		encodingSub:   cfg.EncodingSub,
		createdAt:     normalizeCreatedAt(cfg.CreatedAt),
		fields:        cloneFields(cfg.Fields),
	}
	if err := applyContent(n, cfg, hasInline, hasExternal); err != nil {
		return nil, err
	}
	n.edges = make([]Id, len(edges))
	for i, e := range edges {
		n.edges[i] = e.id
	}

	if err := signNode(n, &cfg, isRootContributor); err != nil {
		return nil, err
	}

	c := &claim{node: n, edges: edges}
	if isRootContributor {
		c.contributor = c // self-attribute
	} else {
		c.contributor = cfg.Contributor
	}
	return c, nil
}

// resolveType fills TypeClass/TypeSub from the combined Type (which takes
// precedence) and validates the class (closed vocabulary) and subtype.
func resolveType(cfg *ClaimBuilder) error {
	if cfg.Type != "" {
		class, sub, err := splitType(cfg.Type)
		if err != nil {
			return wrapDetail(errNewClaim, "Type", err)
		}
		cfg.TypeClass = NodeClass(class)
		cfg.TypeSub = sub
	}
	if cfg.TypeClass == "" || cfg.TypeSub == "" {
		return errClaimTypeRequired
	}
	if !validNodeClass(cfg.TypeClass) {
		return withDetail(errUnknownNodeClass, string(cfg.TypeClass))
	}
	return checkSubtype(cfg.TypeSub)
}

// resolveContentState reports the content mode — none / inline / external.
// Content is manual and mutually exclusive; setting both is an error.
// Structural claims (heads, branches, contributors) carry none.
func resolveContentState(cfg *ClaimBuilder) (hasInline, hasExternal bool, err error) {
	hasInline = cfg.InlineContent != nil
	hasExternal = cfg.ContentHash != nil
	if hasInline && hasExternal {
		return false, false, errClaimContentXOR
	}
	return hasInline, hasExternal, nil
}

// resolveEncoding fills EncodingClass/Sub from the combined Encoding and
// validates it. Encoding is the content media type, so it applies only with
// content — optional then (validated only when set, so binary content like a
// pubkey needn't carry a bogus type), and forbidden without content.
func resolveEncoding(cfg *ClaimBuilder, hasContent bool) error {
	if cfg.Encoding != "" {
		class, sub, err := splitType(cfg.Encoding)
		if err != nil {
			return wrapDetail(errNewClaim, "Encoding", err)
		}
		cfg.EncodingClass = EncodingClass(class)
		cfg.EncodingSub = sub
	}
	if !hasContent {
		if cfg.EncodingClass != "" || cfg.EncodingSub != "" {
			return errEncodingWithoutContent
		}
		return nil
	}
	if cfg.EncodingClass == "" {
		return nil // optional, none set
	}
	if !validEncodingClass(cfg.EncodingClass) {
		return withDetail(errUnknownEncodingClass, string(cfg.EncodingClass))
	}
	return checkEncodingSubtype(cfg.EncodingSub)
}

// assembleEdges builds the claim's edge set: the caller's edges, the
// auto-built contribution/contributor edge (unless root), and — for a diff
// claim — the contribution/diff edge. It enforces the diff naming rule, the
// per-claim edge cardinality, and the §3.5 provenance invariant, then returns
// the edges in canonical (raw-multihash) order.
func assembleEdges(cfg ClaimBuilder, isRootContributor bool) ([]*edge, error) {
	edges := make([]*edge, 0, len(cfg.Edges)+1)
	for _, e := range cfg.Edges {
		ce, err := asConcreteEdge(e)
		if err != nil {
			return nil, wrap(errNewClaim, err)
		}
		edges = append(edges, ce)
	}
	if !isRootContributor {
		ce, err := buildContributorEdge(cfg.Contributor)
		if err != nil {
			return nil, wrapDetail(errNewClaim, "build contribution/contributor edge", err)
		}
		edges = append(edges, ce)
	}
	// A diff claim overlays a predecessor: add the naming contribution/diff
	// edge. Materialisation of the delta happens at load time.
	if cfg.DiffOf != nil {
		de, err := newEdge(EdgeConfig{
			Reference: cfg.DiffOf,
			TypeClass: EdgeClassContribution,
			TypeSub:   string(EdgeSubtypeDiff),
		})
		if err != nil {
			return nil, wrapDetail(errNewClaim, "diff edge", err)
		}
		edges = append(edges, de)
		if err := checkDiffEdgeNames(edges); err != nil {
			return nil, err
		}
	}
	if err := checkEdgeCardinality(edges); err != nil {
		return nil, err
	}
	if requiresProvenance(cfg.TypeClass) && !hasDerivationEdge(edges) {
		return nil, withDetail(errProvenanceRequired, string(cfg.TypeClass)+"/"+cfg.TypeSub)
	}
	sort.SliceStable(edges, func(i, j int) bool {
		return bytes.Compare(idBytes(edges[i].id), idBytes(edges[j].id)) < 0
	})
	return edges, nil
}

// checkDiffEdgeNames enforces the diff naming rule: on a diff claim every
// edge except the two per-claim singletons (contributor, diff) must carry a
// unique, non-empty name — diff overlay is name-keyed.
func checkDiffEdgeNames(edges []*edge) error {
	seen := make(map[string]struct{}, len(edges))
	for _, e := range edges {
		if e.typeClass == EdgeClassContribution &&
			(e.typeSub == "contributor" || e.typeSub == string(EdgeSubtypeDiff)) {
			continue
		}
		name, ok := e.fields[FieldName]
		if !ok || name == "" {
			return errDiffEdgeUnnamed
		}
		if _, dup := seen[name]; dup {
			return withDetail(errDiffEdgeDupName, name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

// checkEdgeCardinality enforces the per-claim singletons: a claim has exactly
// one contributor and overlays at most one predecessor, so at most one
// contribution/contributor edge and one contribution/diff edge.
func checkEdgeCardinality(edges []*edge) error {
	var nContrib, nDiff int
	for _, e := range edges {
		if e.typeClass != EdgeClassContribution {
			continue
		}
		switch e.typeSub {
		case "contributor":
			nContrib++
		case string(EdgeSubtypeDiff):
			nDiff++
		}
	}
	if nContrib > 1 {
		return errTwoContributors
	}
	if nDiff > 1 {
		return errTwoDiffEdges
	}
	return nil
}

// applyContent sets the node's content slots for the chosen mode: inline
// content is hashed here (its address is H(content)); external carries the
// caller's hash+size; none leaves the slots zero.
func applyContent(n *node, cfg ClaimBuilder, hasInline, hasExternal bool) error {
	switch {
	case hasInline:
		ch, err := hashContent(cfg.InlineContent)
		if err != nil {
			return wrapDetail(errNewClaim, "content hash", err)
		}
		n.content = cfg.InlineContent
		n.contentHash = ch
		n.contentSize = uint64(len(cfg.InlineContent))
	case hasExternal:
		n.contentHash = cfg.ContentHash
		n.contentSize = cfg.ContentSize
	}
	return nil
}

// normalizeCreatedAt defaults a zero timestamp to now and normalises to UTC.
func normalizeCreatedAt(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t.UTC()
}

// signNode computes id = Sign(H(S(node))) and stores it on n. It falls back
// to the Contributor's session key when no explicit SigningKey was given (a
// bare contributor returns nil → identity Sign), rejects a key/pubkey
// mismatch before signing (§5.7), then signs. Identity-Sign (no key, no
// pubkey) leaves the hash unchanged, so the id is just the multihash.
func signNode(n *node, cfg *ClaimBuilder, isRootContributor bool) error {
	if cfg.SigningKey == nil && cfg.Contributor != nil {
		cfg.SigningKey = cfg.Contributor.SigningKey()
	}
	if err := checkSigningConsistency(*cfg, isRootContributor); err != nil {
		return wrap(errNewClaim, err)
	}
	encoded, err := encodeNode(n)
	if err != nil {
		return wrapDetail(errNewClaim, "canonical encode", err)
	}
	hash, err := hashContent(encoded)
	if err != nil {
		return wrapDetail(errNewClaim, "hash", err)
	}
	idPayload, err := signHash(cfg.SigningKey, hash.raw)
	if err != nil {
		return wrapDetail(errNewClaim, "sign", err)
	}
	nodeID, err := idFromBytes(idPayload)
	if err != nil {
		return wrapDetail(errNewClaim, "wrap id", err)
	}
	n.id = nodeID
	return nil
}
