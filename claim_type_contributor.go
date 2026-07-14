// package: ranke / claim_type_contributor
// type:    logic
// job:     the Contributor extension of Claim — a contribution/contributor claim, plus a session-scoped signing key
// limits:  the base Claim + concrete claim live in claim.go; construction/signing in claim_builder.go
package ranke

import "crypto"

// Contributor is a typed view over a Claim whose node type is
// "contribution/contributor". Obtain via Claim.AsContributor or
// Claim.Contributor.
type Contributor interface {
	Claim
	// SigningKey returns the private key matching this contributor's
	// pubkey, or nil for identity-Sign (§5.7) or unbound contributors
	// loaded from disk. Attach a key via WithSigningKey for a session.
	SigningKey() crypto.Signer
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
func (s *signedContributor) Node() Node { return s.contributor.Node() }

func (s *signedContributor) Edges(filters ...Filter) []Edge { return s.contributor.Edges(filters...) }
func (s *signedContributor) Contributor() Contributor       { return s.contributor.Contributor() }
func (s *signedContributor) IsContributor() bool            { return s.contributor.IsContributor() }
func (s *signedContributor) AsContributor(key ...crypto.Signer) (Contributor, error) {
	return s.contributor.AsContributor(key...)
}
func (s *signedContributor) ID() Id                    { return s.contributor.ID() }
func (s *signedContributor) Encode() ([]byte, error)   { return s.contributor.Encode() }
func (s *signedContributor) SigningKey() crypto.Signer { return s.key }
