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
		return "", "", withDetail(errExpectedClassSub, s)
	}
	return s[:slash], s[slash+1:], nil
}

// matchAll reports whether e satisfies every edge filter (AND). Node-only
// filters are skipped — checking IsEdgeFilter once avoids a MatchEdge call
// per edge for filters that don't apply to edges at all.
func matchAll(e Edge, filters []Filter) bool {
	for _, f := range filters {
		if f.IsEdgeFilter() && !f.MatchEdge(e) {
			return false
		}
	}
	return true
}

// resolveSigningPubkey returns the pubkey whose private key must produce
// this claim's signature: cfg.Pubkey for the root contributor, else the
// referenced contributor's node pubkey.
func resolveSigningPubkey(isRootContributor bool, cfg ClaimBuilder) ([]byte, error) {
	if isRootContributor {
		return cfg.Pubkey, nil
	}
	if cfg.Contributor == nil {
		return nil, errMissingContributor
	}
	return cfg.Contributor.Node().Pubkey(), nil
}

// checkSigningConsistency rejects mismatches between the supplied
// SigningKey and the resolved contributor pubkey — key-without-pubkey,
// pubkey-without-key, or a key whose public part names a different
// contributor. Both absent is the identity-Sign case.
func checkSigningConsistency(signingKey interface{ Public() crypto.PublicKey }, resolvedPubkey []byte) error {
	hasSigner := signingKey != nil && !isTypedNil(signingKey)
	hasPubkey := len(resolvedPubkey) > 0
	switch {
	case !hasSigner && !hasPubkey:
		return nil // identity-Sign case
	case hasSigner && !hasPubkey:
		return errResolvedNoPubkey
	case !hasSigner && hasPubkey:
		return errResolvedNoKey
	}
	encoded, err := EncodePublicKey(signingKey.Public())
	if err != nil {
		return wrap(errEncodeSigningKey, err)
	}
	if !bytes.Equal(encoded, resolvedPubkey) {
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

// requiresProvenance reports whether claims of this class need at least
// one derivation/* edge (§3.5).
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

// asConcreteEdge unwraps an Edge into the concrete *edge type.
func asConcreteEdge(e Edge) (*edge, error) {
	if e == nil {
		return nil, errNilEdge
	}
	ce, ok := e.(*edge)
	if !ok {
		return nil, errForeignEdge
	}
	return ce, nil
}

// buildContributorEdge constructs the contribution/contributor edge that
// references c.
func buildContributorEdge(c Contributor) (*edge, error) {
	if c == nil {
		return nil, errNilContributor
	}
	e, err := NewEdge(EdgeConfig{
		Reference: c.ID(),
		TypeClass: EdgeClassContribution,
		TypeSub:   "contributor",
	})
	if err != nil {
		return nil, err
	}
	return e.(*edge), nil
}
