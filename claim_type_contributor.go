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

// Contributor is a typed view over a "contribution/contributor" claim, from
// Claim.AsContributor or Claim.Contributor.
type Contributor interface {
	Claim
	// SigningKey is the private key matching this contributor's pubkey (§5.7), nil
	// on one loaded for reading, which verifies claims rather than making them.
	SigningKey() crypto.Signer
	// Pubkey is the multikey public key (§5.7), which AsContributor resolves once
	// and caches, so signing can check a key without a Universe.
	Pubkey() []byte
}

// SigningKey on a bare *claim is nil: a claim is persisted data and holds no
// private keys. WithSigningKey attaches one for a session.
func (c *claim) SigningKey() crypto.Signer { return nil }

// Pubkey on a bare *claim is its inline content (§5.7). An external pubkey becomes
// available by obtaining the contributor through AsContributor.
func (c *claim) Pubkey() []byte { return c.node.content }

// WithSigningKey returns a Contributor carrying the key matching c's pubkey, so
// later calls sign on its behalf. A nil key leaves it able to read, not to build.
func WithSigningKey(c Contributor, key crypto.Signer) Contributor {
	return &signedContributor{contributor: c, key: key}
}

type signedContributor struct {
	contributor Contributor
	key         crypto.Signer
	pubkey      []byte // resolved pubkey (inline or external), cached at AsContributor
}

// Pubkey is the pubkey cached at AsContributor, else the wrapped contributor's.
func (s *signedContributor) Pubkey() []byte {
	if s.pubkey != nil {
		return s.pubkey
	}
	return s.contributor.Pubkey()
}

// Forward every Claim/Contributor method to the wrapped Contributor: method
// promotion does not fire on a named field, so embedding bare would shadow it.
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
func (s *signedContributor) verifyID(raw []byte) error {
	return s.contributor.verifyID(raw)
}

func (s *signedContributor) verifySignature(pubkey, raw []byte) error {
	return s.contributor.verifySignature(pubkey, raw)
}
func (s *signedContributor) EncodeCBOR(f Form) ([]byte, error) { return s.contributor.EncodeCBOR(f) }
func (s *signedContributor) Envelope() ([]byte, error)         { return s.contributor.Envelope() }
func (s *signedContributor) EncodeJSON(f Form) ([]byte, error) { return s.contributor.EncodeJSON(f) }
func (s *signedContributor) SigningKey() crypto.Signer         { return s.key }
