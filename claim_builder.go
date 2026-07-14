// package: ranke / claim
// type:    logic
// job:     ClaimBuilder — assembles, validates, attributes, and signs a claim into an immutable Claim
// limits:  pure construction helpers live in claim_helpers.go; signing primitives in sign.go
package ranke

import (
	"bytes"
	"crypto"
	"fmt"
	"sort"
	"time"
)

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

// NewClaim seeds a ClaimBuilder with the three most common required
// fields. Chain With* setters to add optionals, then call .Sign()
// to finalize:
//
//	c, err := ranke.NewClaim("source/email", alice, body).
//	    WithEncoding(ranke.EncodingMessage("rfc822")).
//	    WithCreatedAt(at).
//	    Sign()
//
// For full struct-literal control, build a ClaimBuilder directly
// and call .Sign() on it.
//
//deadcode:keep
func NewClaim(typ string, contributor Contributor, content []byte) ClaimBuilder {
	return ClaimBuilder{
		Type:        typ,
		Contributor: contributor,
		Content:     content,
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
//
//deadcode:keep
func (b ClaimBuilder) WithType(t string) ClaimBuilder { b.Type = t; return b }

// WithEncoding sets the content media type ("class/sub").
//
//deadcode:keep
func (b ClaimBuilder) WithEncoding(e string) ClaimBuilder { b.Encoding = e; return b }

// WithTitle sets the node's optional title.
//
//deadcode:keep
func (b ClaimBuilder) WithTitle(t string) ClaimBuilder { b.Title = t; return b }

// WithContent sets the inline content bytes.
//
//deadcode:keep
func (b ClaimBuilder) WithContent(c []byte) ClaimBuilder { b.Content = c; return b }

// WithContentHash sets the content hash (for content stored out of band).
//
//deadcode:keep
func (b ClaimBuilder) WithContentHash(h Id) ClaimBuilder { b.ContentHash = h; return b }

// WithCreatedAt sets the creation timestamp.
//
//deadcode:keep
func (b ClaimBuilder) WithCreatedAt(t time.Time) ClaimBuilder { b.CreatedAt = t; return b }

// WithContributor sets the attributing contributor.
//
//deadcode:keep
func (b ClaimBuilder) WithContributor(c Contributor) ClaimBuilder { b.Contributor = c; return b }

// WithPubkey sets the contributor pubkey (§5.7).
//
//deadcode:keep
func (b ClaimBuilder) WithPubkey(p []byte) ClaimBuilder { b.Pubkey = p; return b }

// WithSigningKey sets the key used to sign the claim id.
//
//deadcode:keep
func (b ClaimBuilder) WithSigningKey(k crypto.Signer) ClaimBuilder { b.SigningKey = k; return b }

// WithEdges appends the given edges to the builder's Edges slice.
//
//deadcode:keep
func (b ClaimBuilder) WithEdges(edges ...Edge) ClaimBuilder {
	b.Edges = append(b.Edges, edges...)
	return b
}

// WithField sets one implementation-defined node field (§4.1),
// copying the existing map so the receiver stays unchanged.
//
//deadcode:keep
func (b ClaimBuilder) WithField(key, value string) ClaimBuilder {
	f := make(map[string]string, len(b.Fields)+1)
	for k, v := range b.Fields {
		f[k] = v
	}
	f[key] = value
	b.Fields = f
	return b
}

// buildClaim constructs a Claim atomically per §4.3 from a fully
// populated ClaimBuilder: validates type/encoding/content, enforces
// the §3.5 provenance invariant, auto-builds the contribution/contributor
// edge, sorts edges canonically, and computes the node id as
// Sign(H(canonical(node))). The returned Claim is immutable.
func buildClaim(cfg ClaimBuilder) (Claim, error) {
	// Type takes precedence over the split TypeClass + TypeSub form.
	if cfg.Type != "" {
		class, sub, err := splitType(cfg.Type)
		if err != nil {
			return nil, fmt.Errorf("ranke.NewClaim: Type: %w", err)
		}
		cfg.TypeClass = NodeClass(class)
		cfg.TypeSub = sub
	}
	if cfg.TypeClass == "" || cfg.TypeSub == "" {
		return nil, errClaimTypeRequired
	}
	if !validNodeClass(cfg.TypeClass) {
		return nil, withDetail(errUnknownNodeClass, string(cfg.TypeClass))
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
	// scenarios don't have to spell it out on every structural claim.
	if cfg.EncodingClass == "" {
		cfg.EncodingClass = encText
		cfg.EncodingSub = "plain"
	}
	if !validEncodingClass(cfg.EncodingClass) {
		return nil, withDetail(errUnknownEncodingClass, string(cfg.EncodingClass))
	}
	if cfg.Content != nil && cfg.ContentHash != nil {
		return nil, errClaimContentXOR
	}

	isRootContributor := cfg.TypeClass == NodeContribution &&
		cfg.TypeSub == "contributor" &&
		cfg.Contributor == nil

	if !isRootContributor && cfg.Contributor == nil {
		return nil, errClaimContributorRequired
	}

	// Collect edges; auto-build the contribution/contributor edge unless root.
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

	// Provenance invariant (§3.5).
	if requiresProvenance(cfg.TypeClass) && !hasDerivationEdge(edges) {
		return nil, fmt.Errorf("ranke.NewClaim: %s/%s claims must carry at least one derivation/* edge (§3.5 provenance invariant)", cfg.TypeClass, cfg.TypeSub)
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

	// Content hash + size. Size always tracks len(content); with only
	// ContentHash supplied (no Content), size is 0.
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

	n.edges = make([]Id, len(edges))
	for i, e := range edges {
		n.edges[i] = e.id
	}

	// Resolve the pubkey this claim's signature must match (§4.1, §5.7),
	// then fall back to the Contributor's wrapped key when no explicit
	// SigningKey was given (a bare contributor returns nil → identity Sign).
	resolvedPubkey, err := resolveSigningPubkey(isRootContributor, cfg)
	if err != nil {
		return nil, fmt.Errorf("ranke.NewClaim: %w", err)
	}
	if cfg.SigningKey == nil && cfg.Contributor != nil {
		cfg.SigningKey = cfg.Contributor.SigningKey()
	}
	if err := checkSigningConsistency(cfg.SigningKey, resolvedPubkey); err != nil {
		return nil, fmt.Errorf("ranke.NewClaim: %w", err)
	}

	// id = Sign(H(S(node))). Identity-Sign (no key, empty pubkey) leaves
	// the hash bytes unchanged, so id is just the multihash.
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
	nodeID, err := idFromBytes(idPayload)
	if err != nil {
		return nil, fmt.Errorf("ranke.NewClaim: wrap id: %w", err)
	}
	n.id = nodeID

	c := &claim{node: n, edges: edges}
	if isRootContributor {
		c.contributor = c // self-attribute
	} else {
		c.contributor = cfg.Contributor
	}
	return c, nil
}
