// package: ranke / claim
// type:    logic
// job:     ClaimBuilder — assembles, validates, attributes, and signs a claim into an immutable Claim
// limits:  pure construction helpers live in claim_helpers.go; signing primitives in sign.go
package ranke

import (
	"bytes"
	"context"
	"crypto"
	"sort"
	"time"
)

// ClaimBuilder is the data-only input to ClaimBuilder{...}.Sign(): Type is
// required, and a Contributor except on the root contributor claim (§4.3).
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
	// DiffOf makes this claim a diff over the referenced predecessor; the loader
	// materialises the chain.
	DiffOf Id
	Edges  []Edge
	Fields map[string]string
	// Height is the claim's generation number (§4.1): 0 on an initial claim, else
	// 1 + max(reference heights), which a referencing claim must declare.
	Height uint64
	// autoHeight* back WithAutoHeight — a Universe plus context, so the mode is
	// reachable only through the chained setter.
	autoHeightU   Universe
	autoHeightCtx context.Context
	// SigningKey signs this claim's id; nil plus an empty pubkey is identity Sign
	// (§5.7). A contributor claim's pubkey is its InlineContent, multikey-encoded.
	SigningKey crypto.Signer
}

// NewClaim seeds a ClaimBuilder with the required type and attributing
// contributor. Chain With* setters for the optionals, then call .Sign().
func NewClaim(typ string, contributor Contributor) ClaimBuilder {
	return ClaimBuilder{
		Type:        typ,
		Contributor: contributor,
	}
}

// Sign finalizes a ClaimBuilder into an immutable Claim. A variadic key
// overrides SigningKey, which otherwise comes from the Contributor (§5.7).
func (b ClaimBuilder) Sign(signingKey ...crypto.Signer) (Claim, error) {
	if len(signingKey) > 0 && signingKey[0] != nil {
		b.SigningKey = signingKey[0]
	}
	return buildClaim(b)
}

// --- chained setters ---

// WithType sets the claim type ("class/sub").
func (b ClaimBuilder) WithType(t string) ClaimBuilder { b.Type = t; return b }

// WithEncoding sets the content media type ("class/sub").
func (b ClaimBuilder) WithEncoding(e string) ClaimBuilder { b.Encoding = e; return b }

// WithDiff makes this claim a diff over the predecessor at id, restating only
// what differs, and adds the contribution/diff edge.
func (b ClaimBuilder) WithDiff(id Id) ClaimBuilder { b.DiffOf = id; return b }

// WithInlineContent sets the content bytes the claim itself carries.
func (b ClaimBuilder) WithInlineContent(c []byte) ClaimBuilder { b.InlineContent = c; return b }

// WithExternalContent references content stored elsewhere by hash and byte
// size, exclusive with WithInlineContent.
func (b ClaimBuilder) WithExternalContent(hash Id, size uint64) ClaimBuilder {
	b.ContentHash = hash
	b.ContentSize = size
	return b
}

// WithCreatedAt sets the creation timestamp, which defaults to now in UTC.
func (b ClaimBuilder) WithCreatedAt(t time.Time) ClaimBuilder { b.CreatedAt = t; return b }

// WithHeight sets the generation number (§4.1) — 1 + max over the referenced
// heights, or 0 for an initial claim. The verifier re-derives and enforces it.
func (b ClaimBuilder) WithHeight(h uint64) ClaimBuilder { b.Height = h; return b }

// WithAutoHeight makes Sign read each referenced claim's committed height from
// u and set 1 + max (0 with no references), exclusive with WithHeight.
func (b ClaimBuilder) WithAutoHeight(ctx context.Context, u Universe) ClaimBuilder {
	b.autoHeightCtx, b.autoHeightU = ctx, u
	return b
}

// WithContributor sets the attributing contributor.
func (b ClaimBuilder) WithContributor(c Contributor) ClaimBuilder { b.Contributor = c; return b }

// WithSigningKey sets the key used to sign the claim id.
func (b ClaimBuilder) WithSigningKey(k crypto.Signer) ClaimBuilder { b.SigningKey = k; return b }

// WithEdges appends the given edges to the builder's Edges slice.
func (b ClaimBuilder) WithEdges(edges ...Edge) ClaimBuilder {
	b.Edges = append(b.Edges, edges...)
	return b
}

// WithField sets one implementation-defined node field (§4.1) in a copied map.
func (b ClaimBuilder) WithField(key, value string) ClaimBuilder {
	f := make(map[string]string, len(b.Fields)+1)
	for k, v := range b.Fields {
		f[k] = v
	}
	f[key] = value
	b.Fields = f
	return b
}

// buildClaim constructs a Claim atomically per §4.3: resolve type, content mode
// and encoding; assemble and validate the edge set; build the node; sign it.
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
	if err := checkFields(cfg.Fields); err != nil {
		return nil, err
	}
	if err := CheckDeletable(cfg.TypeClass, cfg.TypeSub, cfg.Fields); err != nil {
		return nil, err
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

	height, err := resolveHeight(cfg, edges)
	if err != nil {
		return nil, err
	}
	n.height = height

	if err := signNode(n, edges, &cfg, isRootContributor); err != nil {
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

// resolveType fills TypeClass/TypeSub from the combined Type, which wins, and
// validates the class vocabulary and the subtype.
func resolveType(cfg *ClaimBuilder) error {
	if cfg.Type != "" {
		class, sub, err := splitType(cfg.Type)
		if err != nil {
			return WrapDetail(errNewClaim, "Type", err)
		}
		cfg.TypeClass = NodeClass(class)
		cfg.TypeSub = sub
	}
	if cfg.TypeClass == "" || cfg.TypeSub == "" {
		return errClaimTypeRequired
	}
	if !validNodeClass(cfg.TypeClass) {
		return WithDetail(errUnknownNodeClass, string(cfg.TypeClass))
	}
	return checkSubtype(cfg.TypeSub)
}

// resolveContentState reports the content mode — none / inline / external.
// Inline and external are mutually exclusive; setting both is an error.
func resolveContentState(cfg *ClaimBuilder) (hasInline, hasExternal bool, err error) {
	hasInline = cfg.InlineContent != nil
	hasExternal = cfg.ContentHash != nil
	if hasInline && hasExternal {
		return false, false, errClaimContentXOR
	}
	if hasInline && len(cfg.InlineContent) > maxInlineContent {
		return false, false, errInlineContentTooLarge
	}
	return hasInline, hasExternal, nil
}

// maxInlineContent caps inline content at construction only (NewClaim/NewEdge);
// larger blobs belong in external content (dedup + streaming).
const maxInlineContent = 1 << 20 // 1 MiB

// resolveEncoding fills EncodingClass/Sub from the combined or split Encoding
func resolveEncoding(cfg *ClaimBuilder, hasContent bool) error {
	enc := cfg.Encoding
	if enc == "" && (cfg.EncodingClass != "" || cfg.EncodingSub != "") {
		enc = string(cfg.EncodingClass) + "/" + cfg.EncodingSub
	}
	class, sub, err := resolveContentEncoding(enc, hasContent)
	if err != nil {
		return err
	}
	cfg.EncodingClass, cfg.EncodingSub = class, sub
	return nil
}

// resolveContentEncoding parses "class/sub" and enforces the content⇔encoding
// coupling shared by nodes (NewClaim) and edges (NewEdge)
func resolveContentEncoding(encoding string, hasContent bool) (EncodingClass, string, error) {
	var class EncodingClass
	var sub string
	if encoding != "" {
		c, s, err := splitType(encoding)
		if err != nil {
			return "", "", WrapDetail(errNewClaim, "Encoding", err)
		}
		class, sub = EncodingClass(c), s
	}
	if !hasContent {
		if class != "" || sub != "" {
			return "", "", errEncodingWithoutContent
		}
		return "", "", nil
	}
	if class == "" {
		return "", "", errContentWithoutEncoding
	}
	if !validEncodingClass(class) {
		return "", "", WithDetail(errUnknownEncodingClass, string(class))
	}
	if err := checkEncodingSubtype(sub); err != nil {
		return "", "", err
	}
	return class, sub, nil
}

// assembleEdges builds the edge set (caller edges, the auto contributor edge, a
// diff edge), enforces diff naming, cardinality and §3.5 provenance, and returns
// the edges in canonical (raw-multihash) order.
func assembleEdges(cfg ClaimBuilder, isRootContributor bool) ([]*edge, error) {
	edges := make([]*edge, 0, len(cfg.Edges)+1)
	for _, e := range cfg.Edges {
		ce, err := asConcreteEdge(e)
		if err != nil {
			return nil, Wrap(errNewClaim, err)
		}
		edges = append(edges, ce)
	}
	if !isRootContributor {
		ce, err := buildContributorEdge(cfg.Contributor)
		if err != nil {
			return nil, WrapDetail(errNewClaim, "build contribution/contributor edge", err)
		}
		edges = append(edges, ce)
	}
	// A diff claim overlays a predecessor: name it with a contribution/diff edge.
	if cfg.DiffOf != nil {
		de, err := newEdge(EdgeConfig{
			Reference: cfg.DiffOf,
			TypeClass: EdgeClassContribution,
			TypeSub:   string(EdgeSubtypeDiff),
		})
		if err != nil {
			return nil, WrapDetail(errNewClaim, "diff edge", err)
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
		return nil, WithDetail(errProvenanceRequired, string(cfg.TypeClass)+"/"+cfg.TypeSub)
	}
	sort.SliceStable(edges, func(i, j int) bool {
		return bytes.Compare(idBytes(edges[i].id), idBytes(edges[j].id)) < 0
	})
	return edges, nil
}

// checkDiffEdgeNames requires a unique, non-empty name on every edge of a diff
// claim beyond the singletons (contributor, diff) — overlay is name-keyed (`V-DIFFEDGE`).
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
			return WithDetail(errDiffEdgeDupName, name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

// checkEdgeCardinality enforces the per-claim singletons: at most one
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

// applyContent sets the node's content slots for the chosen mode: inline holds
// the bytes the id commits to (§Content), external the caller's hash+size.
func applyContent(n *node, cfg ClaimBuilder, hasInline, hasExternal bool) error {
	switch {
	case hasInline:
		n.content = cfg.InlineContent
		n.contentSize = uint64(len(cfg.InlineContent))
	case hasExternal:
		n.contentHash = cfg.ContentHash
		n.contentSize = cfg.ContentSize
	}
	return nil
}

// resolveHeight derives the generation number (§4.1) from the assembled edges:
// 0 on an initial claim, a declared non-zero Height on a referencing claim, or
// the WithAutoHeight lookup, which rejects a conflicting explicit Height.
func resolveHeight(cfg ClaimBuilder, edges []*edge) (uint64, error) {
	if cfg.autoHeightU != nil {
		if cfg.Height != 0 {
			return 0, errHeightWithAuto
		}
		return computeAutoHeight(cfg.autoHeightCtx, cfg.autoHeightU, edges)
	}
	if len(edges) == 0 {
		if cfg.Height != 0 {
			return 0, errHeightOnInitial
		}
		return 0, nil
	}
	if cfg.Height == 0 {
		return 0, errHeightRequired
	}
	return cfg.Height, nil
}

// computeAutoHeight returns 1 + max of the referenced claims' committed heights
// from u (0 with no references) — one level, since each carries its own height.
func computeAutoHeight(ctx context.Context, u Universe, edges []*edge) (uint64, error) {
	if len(edges) == 0 {
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ids := make([]Id, len(edges))
	for i, e := range edges {
		ids[i] = e.reference
	}
	heights, err := u.GetClaimHeights(ctx, ids)
	if err != nil {
		return 0, Wrap(errHeightResolve, err)
	}
	var max uint64
	for _, h := range heights {
		if h > max {
			max = h
		}
	}
	return max + 1, nil
}

// normalizeCreatedAt defaults a zero timestamp to now and normalises to UTC.
func normalizeCreatedAt(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t.UTC()
}

// signNode computes id = Sign(H(S(node))) and stores it on n, falling back to
// the Contributor's session key and rejecting a key/pubkey mismatch (§5.7).
func signNode(n *node, edges []*edge, cfg *ClaimBuilder, isRootContributor bool) error {
	if cfg.SigningKey == nil && cfg.Contributor != nil {
		cfg.SigningKey = cfg.Contributor.SigningKey()
	}
	if err := checkSigningConsistency(*cfg, isRootContributor); err != nil {
		return Wrap(errNewClaim, err)
	}
	encoded, err := encodeNode(n, edges)
	if err != nil {
		return WrapDetail(errNewClaim, "canonical encode", err)
	}
	hash, err := hashContent(encoded)
	if err != nil {
		return WrapDetail(errNewClaim, "hash", err)
	}
	idPayload, err := signHash(cfg.SigningKey, hash.raw)
	if err != nil {
		return WrapDetail(errNewClaim, "sign", err)
	}
	nodeID, err := idFromBytes(idPayload)
	if err != nil {
		return WrapDetail(errNewClaim, "wrap id", err)
	}
	n.id = nodeID
	return nil
}
