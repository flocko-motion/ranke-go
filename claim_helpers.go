// package: ranke / claim
// type:    logic
// job:     pure helpers for claim construction — type parsing, pubkey resolution, edge checks
// limits:  no I/O or signing; the builder that calls them is in claim_builder.go
package ranke

import (
	"bytes"
	"crypto"
)

// splitType parses "class/sub" into its two non-empty segments.
func splitType(s string) (class, sub string, err error) {
	if s == "" {
		return "", "", errEmptyType
	}
	slash := -1
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			slash = i
			break
		}
	}
	if slash <= 0 || slash == len(s)-1 {
		return "", "", WithDetail(errExpectedClassSub, s)
	}
	return s[:slash], s[slash+1:], nil
}

// matchAll reports whether e satisfies every edge filter (AND).
func matchAll(e Edge, filters []Filter) bool {
	for _, f := range filters {
		if f.IsEdgeFilter() && !f.MatchEdge(e) {
			return false
		}
	}
	return true
}

// checkSigningConsistency verifies the SigningKey matches the pubkey this claim
// declares (§5.7) and classifies the identity-Sign case. Resolved pubkeys match
// on bytes, an external root declaration on H(key.Public()) — so no Universe.
func checkSigningConsistency(cfg ClaimBuilder, isRoot bool) error {
	hasSigner := cfg.SigningKey != nil && !isTypedNil(cfg.SigningKey)
	if !isRoot {
		return checkKeyAgainstPubkey(hasSigner, cfg.SigningKey, cfg.Contributor.Pubkey())
	}
	switch {
	case cfg.InlineContent != nil:
		return checkKeyAgainstPubkey(hasSigner, cfg.SigningKey, cfg.InlineContent)
	case cfg.ContentHash != nil:
		// External pubkey declaration — match by hash, no fetch.
		if !hasSigner {
			return errResolvedNoKey
		}
		encoded, err := EncodePublicKey(cfg.SigningKey.Public())
		if err != nil {
			return Wrap(errEncodeSigningKey, err)
		}
		h, err := HashContent(encoded)
		if err != nil {
			return err
		}
		if !h.Equal(cfg.ContentHash) {
			return errResolvedMismatch
		}
		return nil
	default:
		// No declared pubkey: identity-Sign, or a key with nothing to attest to.
		if hasSigner {
			return errResolvedNoPubkey
		}
		return nil
	}
}

// checkKeyAgainstPubkey matches a signing key against a pubkey given as bytes.
// Neither half present is identity-Sign; one half alone is an error.
func checkKeyAgainstPubkey(hasSigner bool, key crypto.Signer, pubkey []byte) error {
	hasPubkey := len(pubkey) > 0
	switch {
	case !hasSigner && !hasPubkey:
		return nil // identity-Sign case
	case hasSigner && !hasPubkey:
		return errResolvedNoPubkey
	case !hasSigner && hasPubkey:
		return errResolvedNoKey
	}
	encoded, err := EncodePublicKey(key.Public())
	if err != nil {
		return Wrap(errEncodeSigningKey, err)
	}
	if !bytes.Equal(encoded, pubkey) {
		return errResolvedMismatch
	}
	return nil
}

// isTypedNil reports whether i is a nil interface or wraps a nil signer
// (a nil ed25519.PrivateKey would otherwise pass `!= nil`).
func isTypedNil(i any) bool {
	if i == nil {
		return true
	}
	if s, ok := i.(interface{ Public() crypto.PublicKey }); ok {
		// Public() on a nil signer panics; a usable signer yields a pubkey.
		defer func() { _ = recover() }()
		if s.Public() == nil {
			return true
		}
	}
	return false
}

// HeightOf returns the generation number a new claim referencing refs must
// carry: 1 + max(refs' heights), or 0 for an initial node. Pass every claim the
// new one references — contributor and predecessor edges count too.
func HeightOf(refs ...Claim) uint64 {
	var max uint64
	for _, r := range refs {
		if r == nil {
			continue
		}
		if h := r.Node().Height(); h > max {
			max = h
		}
	}
	if len(refs) == 0 {
		return 0
	}
	return max + 1
}

// requiresProvenance reports whether claims of this class need at least
// one derivation/* edge (§3.5, `V-PROV`).
func requiresProvenance(c NodeClass) bool {
	switch c {
	case NodeClassDerivation, NodeClassEntity, NodeClassRelation:
		return true
	}
	return false
}

// hasDerivationEdge reports whether edges contains a derivation/* edge.
func hasDerivationEdge(edges []*edge) bool {
	for _, e := range edges {
		if e.typeClass == EdgeClassDerivation {
			return true
		}
	}
	return false
}

// asConcreteEdge unwraps an Edge into the concrete *edge, rejecting nil.
func asConcreteEdge(e Edge) (*edge, error) {
	if e == nil {
		return nil, errNilEdge
	}
	return e.unwrap(), nil
}

// buildContributorEdge constructs the contribution/contributor edge that
// references c.
func buildContributorEdge(c Contributor) (*edge, error) {
	if c == nil {
		return nil, errNilContributor
	}
	return newEdge(EdgeConfig{
		Reference: c.ID(),
		TypeClass: EdgeClassContribution,
		TypeSub:   "contributor",
	})
}
