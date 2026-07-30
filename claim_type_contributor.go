// package: ranke / claim_type_contributor
// type:    logic
// job:     the Contributor extension of Claim — a contribution/contributor claim, plus a
// session-scoped signing key
// limits:  the base Claim + concrete claim live in claim.go; construction/signing in claim_builder.go
package ranke

import (
	"context"
	"crypto"
	"io"
)

// Contributor is a typed view over a Claim whose node type is
// "contribution/contributor". Obtain via Claim.AsContributor or
// Claim.Contributor.
type Contributor interface {
	Claim
	// SigningKey returns the private key matching this contributor's
	// pubkey, or nil for identity-Sign (§5.7) or unbound contributors
	// loaded from disk. Attach a key via WithSigningKey for a session.
	SigningKey() crypto.Signer
	// Pubkey returns this contributor's multikey-encoded public key
	// (§5.7). AsContributor resolves it once — transparently, inline or
	// external via the Universe — and caches it here, so signing can check
	// a key against it without a Universe. Empty for an identity
	// contributor.
	Pubkey() []byte
}

// SigningKey on a bare *claim is always nil — the claim is the persisted
// data structure and stores no private keys. Wrap with WithSigningKey
// for a session-scoped signer.
func (c *claim) SigningKey() crypto.Signer { return nil }

// Pubkey on a bare *claim is its inline content (§5.7: a contributor
// carries its pubkey as content). Nil when the content is external and has
// not been resolved through AsContributor — obtain the contributor that
// way to make an external pubkey available here.
func (c *claim) Pubkey() []byte { return c.node.content }

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
	pubkey      []byte // resolved pubkey (inline or external), cached at AsContributor
}

// Pubkey returns the resolved pubkey cached at AsContributor, falling back
// to the wrapped contributor's (a plain WithSigningKey wrap carries none).
func (s *signedContributor) Pubkey() []byte {
	if s.pubkey != nil {
		return s.pubkey
	}
	return s.contributor.Pubkey()
}

// Forward every Claim/Contributor method to the wrapped Contributor.
// Embedding the interface bare would shadow it with a same-named field —
// Go's method promotion doesn't fire on a *named* field.
func (s *signedContributor) Node() Node { return s.contributor.Node() }

func (s *signedContributor) Edges(filters ...Filter) []Edge { return s.contributor.Edges(filters...) }
func (s *signedContributor) Contributor() Contributor       { return s.contributor.Contributor() }
func (s *signedContributor) IsContributor() bool            { return s.contributor.IsContributor() }
func (s *signedContributor) AsContributor(ctx context.Context, u Universe, key ...crypto.Signer) (Contributor, error) {
	return s.contributor.AsContributor(ctx, u, key...)
}
func (s *signedContributor) ID() Id { return s.contributor.ID() }
func (s *signedContributor) GetContent(ctx context.Context, u Universe) (io.Reader, error) {
	return s.contributor.GetContent(ctx, u)
}
func (s *signedContributor) Tags() map[string]string { return s.contributor.Tags() }
func (s *signedContributor) Tag(key string) string   { return s.contributor.Tag(key) }
func (s *signedContributor) HasTag(key string) bool  { return s.contributor.HasTag(key) }
func (s *signedContributor) SetTag(ctx context.Context, u Universe) error {
	return s.contributor.SetTag(ctx, u)
}
func (s *signedContributor) unwrap() *claim { return s.contributor.unwrap() }
func (s *signedContributor) verifyID(pubkey, raw []byte) error {
	return s.contributor.verifyID(pubkey, raw)
}
func (s *signedContributor) EncodeCBOR(f Form) ([]byte, error) { return s.contributor.EncodeCBOR(f) }
func (s *signedContributor) EncodeJSON(f Form) ([]byte, error) { return s.contributor.EncodeJSON(f) }
func (s *signedContributor) SigningKey() crypto.Signer         { return s.key }
